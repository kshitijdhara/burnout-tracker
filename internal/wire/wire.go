// Package wire defines the versioned messages that hook processes send to
// the daemon over the local socket or pipe. It is a cross-process contract:
// the daemon accepts the current version and the one before it, ignores
// unknown fields, and rejects anything oversized or malformed.
//
// The JSON Schema for each version lives in schemas/v<N>/; a test keeps it
// in step with the validation here.
package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

// Version is the message version this build sends.
const Version = 1

// MinVersion is the oldest version this build accepts (N-1 policy).
const MinVersion = 1

// MaxMessageBytes caps a single message. Larger input is rejected unread.
const MaxMessageBytes = 4096

// MaxPathBytes caps the cwd field.
const MaxPathBytes = 2048

// Kinds of message.
const (
	KindShellCommand = "shell.cmd"
	KindAgentHook    = "agent.hook"
	// KindPing carries no body. `burnout doctor` sends it to check the
	// daemon is listening; the daemon drops it without counting an error.
	KindPing = "ping"
)

// Message is the envelope for every hook-to-daemon message.
type Message struct {
	V      int           `json:"v"`
	Kind   string        `json:"kind"`
	SentMs int64         `json:"sent_ms"`
	Shell  *ShellCommand `json:"shell,omitempty"`
	Agent  *AgentHook    `json:"agent,omitempty"`
}

// ShellCommand is sent by `burnout emit shell` after each interactive command.
// It never contains the command line: the shell hook extracts argv0 and a
// candidate subcommand itself.
type ShellCommand struct {
	Argv0    string `json:"argv0"`
	Subcmd   string `json:"subcmd,omitempty"`
	Exit     int    `json:"exit"`
	StartMs  int64  `json:"start_ms"`
	EndMs    int64  `json:"end_ms"`
	Cwd      string `json:"cwd"` // converted to an HMAC'd project key by the daemon, then discarded
	TTY      string `json:"tty,omitempty"`
	ShellPID int    `json:"shell_pid,omitempty"`
}

// AgentHook is sent by `burnout hook <agent>` for each agent hook event.
// Only allowlisted fields of the agent's payload are ever copied here.
type AgentHook struct {
	Agent     string `json:"agent"`
	Event     string `json:"event"`
	SessionID string `json:"session_id"` // HMAC'd by the daemon on receipt
	ToolName  string `json:"tool_name,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	AgentPID  int    `json:"agent_pid,omitempty"`
	AtMs      int64  `json:"at_ms"`
}

// Errors returned by Decode. Callers count them; they are never fatal.
var (
	ErrTooLarge    = errors.New("wire: message too large")
	ErrBadVersion  = errors.New("wire: unsupported version")
	ErrMalformed   = errors.New("wire: malformed message")
	ErrInvalidData = errors.New("wire: invalid field")
)

// Encode marshals m. It fails if the result would exceed MaxMessageBytes.
func Encode(m Message) ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxMessageBytes {
		return nil, ErrTooLarge
	}
	return b, nil
}

// Decode reads one message from r, enforcing the size cap, version window
// and field validation.
func Decode(r io.Reader) (Message, error) {
	buf, err := io.ReadAll(io.LimitReader(r, MaxMessageBytes+1))
	if err != nil {
		return Message{}, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if len(buf) > MaxMessageBytes {
		return Message{}, ErrTooLarge
	}
	var m Message
	dec := json.NewDecoder(bytes.NewReader(buf))
	if err := dec.Decode(&m); err != nil {
		return Message{}, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if dec.More() {
		return Message{}, fmt.Errorf("%w: trailing data", ErrMalformed)
	}
	if m.V < MinVersion || m.V > Version {
		return Message{}, ErrBadVersion
	}
	if err := m.validate(); err != nil {
		return Message{}, err
	}
	return m, nil
}

var (
	// Command names are short and drawn from a conservative character set.
	// Anything else (for example a pasted token) is rejected.
	reCommand = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
	reEvent   = regexp.MustCompile(`^[A-Za-z]{1,40}$`)
	reAgent   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	reTool    = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
	reSession = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
)

func (m Message) validate() error {
	switch m.Kind {
	case KindPing:
		if m.Shell != nil || m.Agent != nil {
			return fmt.Errorf("%w: ping body", ErrMalformed)
		}
		return nil
	case KindShellCommand:
		if m.Shell == nil || m.Agent != nil {
			return fmt.Errorf("%w: shell body", ErrMalformed)
		}
		return m.Shell.validate()
	case KindAgentHook:
		if m.Agent == nil || m.Shell != nil {
			return fmt.Errorf("%w: agent body", ErrMalformed)
		}
		return m.Agent.validate()
	default:
		return fmt.Errorf("%w: kind %q", ErrMalformed, m.Kind)
	}
}

func (s *ShellCommand) validate() error {
	if s.Argv0 != "" && !reCommand.MatchString(s.Argv0) {
		return fmt.Errorf("%w: argv0", ErrInvalidData)
	}
	if s.Subcmd != "" && !reCommand.MatchString(s.Subcmd) {
		return fmt.Errorf("%w: subcmd", ErrInvalidData)
	}
	if s.StartMs <= 0 || s.EndMs < s.StartMs {
		return fmt.Errorf("%w: times", ErrInvalidData)
	}
	return validCwd(s.Cwd)
}

// validCwd accepts an empty or absolute, reasonably short path. Paths are
// only ever turned into an HMAC'd project key, never stored.
func validCwd(cwd string) error {
	if cwd == "" {
		return nil
	}
	if len(cwd) > MaxPathBytes || !filepath.IsAbs(cwd) || strings.ContainsRune(cwd, 0) {
		return fmt.Errorf("%w: cwd", ErrInvalidData)
	}
	return nil
}

func (a *AgentHook) validate() error {
	switch {
	case !reAgent.MatchString(a.Agent):
		return fmt.Errorf("%w: agent", ErrInvalidData)
	case !reEvent.MatchString(a.Event):
		return fmt.Errorf("%w: event", ErrInvalidData)
	case a.SessionID != "" && !reSession.MatchString(a.SessionID):
		return fmt.Errorf("%w: session_id", ErrInvalidData)
	case a.ToolName != "" && !reTool.MatchString(a.ToolName):
		return fmt.Errorf("%w: tool_name", ErrInvalidData)
	case a.AtMs <= 0:
		return fmt.Errorf("%w: at_ms", ErrInvalidData)
	}
	return validCwd(a.Cwd)
}
