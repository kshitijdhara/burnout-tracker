// Package claude integrates Claude Code through its hooks: it parses hook
// payloads with a strict field allowlist, forwards a minimal event to the
// daemon, and installs or removes our hook entries in Claude Code's
// settings. See docs/system-design.md §4.3 and
// docs/privacy-and-security.md §4.3.
package claude

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

const (
	maxKeyBytes   = 256  // longer keys can't be ours; they are skipped
	maxValueBytes = 4096 // longest allowlisted value we keep
)

var errPayload = errors.New("claude: malformed payload")

// Payload holds the only hook fields we ever keep.
type Payload struct {
	SessionID string
	Event     string
	ToolName  string
	Cwd       model.Sensitive[string]
}

// Drain reads r to the end and discards it, so a writer (Claude Code)
// never sees a broken pipe however large its payload.
func Drain(r io.Reader) { _, _ = io.Copy(io.Discard, r) }

// ParsePayload reads a Claude Code hook payload and keeps only session_id,
// hook_event_name, tool_name and cwd (string values up to 4 KiB). Every
// other value (prompt, tool_input, tool_response, transcript_path, …) is
// skipped byte by byte without ever being stored (privacy & security §4.3).
// Afterwards the rest of r is drained.
func ParsePayload(r io.Reader) (Payload, error) {
	defer Drain(r)
	s := scanner{r: bufio.NewReaderSize(r, 4096)}
	var p Payload
	if err := s.object(func(key string, first byte) error {
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
		if dst == nil || first != '"' {
			return s.skipValue(first) // not ours, or not a string
		}
		v, ok, err := s.stringValue(maxValueBytes)
		if err != nil || !ok {
			return err // too long: ignored
		}
		*dst = v
		if key == "cwd" {
			p.Cwd = model.NewSensitive(cwd)
		}
		return nil
	}); err != nil {
		return Payload{}, err
	}
	return p, nil
}

// scanner is a minimal JSON reader for one top-level object. It only ever
// holds allowlisted keys and values (bounded); everything else streams past.
type scanner struct{ r *bufio.Reader }

// object walks a top-level object, calling field with each key and the
// first byte of its value. field must consume the whole value.
func (s scanner) object(field func(key string, first byte) error) error {
	if c, err := s.next(); err != nil || c != '{' {
		return fmt.Errorf("%w: expected object", errPayload)
	}
	c, err := s.next()
	if err != nil {
		return err
	}
	if c == '}' {
		return nil
	}
	for {
		if c != '"' {
			return fmt.Errorf("%w: expected key", errPayload)
		}
		key, _, err := s.stringValue(maxKeyBytes)
		if err != nil {
			return err
		}
		if c, err := s.next(); err != nil || c != ':' {
			return fmt.Errorf("%w: expected ':'", errPayload)
		}
		first, err := s.next()
		if err != nil {
			return err
		}
		if err := field(key, first); err != nil {
			return err
		}
		if c, err = s.next(); err != nil {
			return err
		}
		switch c {
		case '}':
			return nil
		case ',':
			if c, err = s.next(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: expected ',' or '}'", errPayload)
		}
	}
}

// next returns the next non-whitespace byte.
func (s scanner) next() (byte, error) {
	for {
		c, err := s.r.ReadByte()
		if err != nil {
			return 0, fmt.Errorf("%w: %w", errPayload, err)
		}
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return c, nil
	}
}

// stringValue reads the rest of a string whose opening quote was consumed.
// It returns the unescaped value if the raw form fits in limit bytes;
// longer strings are consumed and reported with ok == false.
func (s scanner) stringValue(limit int) (v string, ok bool, err error) {
	raw := make([]byte, 1, 64)
	raw[0] = '"'
	ok = true
	for {
		c, err := s.r.ReadByte()
		if err != nil {
			return "", false, fmt.Errorf("%w: %w", errPayload, err)
		}
		if c < 0x20 {
			return "", false, fmt.Errorf("%w: control character in string", errPayload)
		}
		if ok && len(raw) > limit {
			ok, raw = false, nil // too long: stop keeping bytes, keep consuming
		}
		if ok {
			raw = append(raw, c)
		}
		switch c {
		case '"':
			if !ok {
				return "", false, nil
			}
			if err := json.Unmarshal(raw, &v); err != nil {
				return "", false, fmt.Errorf("%w: %w", errPayload, err)
			}
			return v, true, nil
		case '\\':
			e, err := s.r.ReadByte()
			if err != nil {
				return "", false, fmt.Errorf("%w: %w", errPayload, err)
			}
			if ok {
				raw = append(raw, e)
			}
		}
	}
}

// skipString consumes the rest of a string without keeping any of it.
func (s scanner) skipString() error {
	for {
		c, err := s.r.ReadByte()
		if err != nil {
			return fmt.Errorf("%w: %w", errPayload, err)
		}
		if c < 0x20 {
			return fmt.Errorf("%w: control character in string", errPayload)
		}
		switch c {
		case '"':
			return nil
		case '\\':
			if _, err := s.r.ReadByte(); err != nil {
				return fmt.Errorf("%w: %w", errPayload, err)
			}
		}
	}
}

// skipValue consumes one value whose first byte has been read, tracking
// nesting and strings but storing nothing.
func (s scanner) skipValue(first byte) error {
	switch first {
	case '"':
		return s.skipString()
	case '{', '[':
		return s.skipNested()
	default:
		return s.skipLiteral(first)
	}
}

func (s scanner) skipNested() error {
	for depth := 1; depth > 0; {
		c, err := s.r.ReadByte()
		if err != nil {
			return fmt.Errorf("%w: %w", errPayload, err)
		}
		switch c {
		case '"':
			if err := s.skipString(); err != nil {
				return err
			}
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
	}
	return nil
}

// skipLiteral consumes a number, true, false or null, leaving the following
// delimiter unread.
func (s scanner) skipLiteral(first byte) error {
	if !isLiteralByte(first) {
		return fmt.Errorf("%w: unexpected %q", errPayload, first)
	}
	for {
		c, err := s.r.ReadByte()
		if err != nil {
			return fmt.Errorf("%w: %w", errPayload, err)
		}
		if !isLiteralByte(c) {
			return s.r.UnreadByte()
		}
	}
}

func isLiteralByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c == '-' || c == '+' || c == '.' || c == 'E'
}
