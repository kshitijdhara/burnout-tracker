// Package claude integrates Claude Code through its hooks: it parses hook
// payloads with a strict field allowlist, forwards a minimal event to the
// daemon, and installs or removes our hook entries in Claude Code's
// settings. See docs/system-design.md §4.3 and
// docs/privacy-and-security.md §4.3.
package claude

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// maxDrain bounds how much of an oversized payload we read and discard.
const maxDrain = 64 << 20

// Payload holds the only hook fields we ever keep.
type Payload struct {
	SessionID string
	Event     string
	ToolName  string
	Cwd       model.Sensitive[string]
}

// ParsePayload reads a Claude Code hook payload and keeps only session_id,
// hook_event_name, tool_name and cwd. Every other value (prompt, tool_input,
// tool_response, transcript_path, …) is skipped token by token and never
// stored. Afterwards the rest of r is drained, so Claude Code never sees a
// broken pipe when it writes a large payload.
func ParsePayload(r io.Reader) (Payload, error) {
	lr := &io.LimitedReader{R: r, N: maxDrain}
	defer func() { _, _ = io.Copy(io.Discard, lr) }()

	dec := json.NewDecoder(lr)
	if err := expectDelim(dec, '{'); err != nil {
		return Payload{}, err
	}
	var p Payload
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return Payload{}, fmt.Errorf("claude: payload key: %w", err)
		}
		key, _ := tok.(string)
		var dst *string
		var cwd string
		switch key {
		case "session_id":
			dst = &p.SessionID
		case "hook_event_name":
			dst = &p.Event
		case "tool_name":
			dst = &p.ToolName
		case "cwd":
			dst = &cwd
		}
		if dst == nil {
			if err := skipValue(dec); err != nil {
				return Payload{}, err
			}
			continue
		}
		s, err := readString(dec)
		if err != nil {
			return Payload{}, err
		}
		*dst = s
		if key == "cwd" {
			p.Cwd = model.NewSensitive(cwd)
		}
	}
	if err := expectDelim(dec, '}'); err != nil {
		return Payload{}, err
	}
	return p, nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("claude: payload: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("claude: payload: expected %q", want)
	}
	return nil
}

// readString returns a string value, or "" (after skipping it) for any
// other JSON type.
func readString(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", fmt.Errorf("claude: payload value: %w", err)
	}
	switch t := tok.(type) {
	case string:
		return t, nil
	case json.Delim:
		return "", skipRest(dec, 1)
	default:
		return "", nil
	}
}

// skipValue consumes one value of any type without keeping it.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("claude: payload value: %w", err)
	}
	if d, ok := tok.(json.Delim); ok && (d == '{' || d == '[') {
		return skipRest(dec, 1)
	}
	return nil
}

func skipRest(dec *json.Decoder, depth int) error {
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("claude: payload value: %w", err)
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}
