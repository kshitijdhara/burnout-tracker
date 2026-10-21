package claude

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/procinfo"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/wire"
)

// AgentName identifies Claude Code in wire messages.
const AgentName = "claude"

// ancestorLimit bounds the walk from the hook process to the claude process
// (hook → shell → claude is typical).
const ancestorLimit = 8

// Procs answers the process-tree question Run needs: this hook's ancestors
// with their names, from one view of the process table (one snapshot on
// Windows, which keeps the hook within its time budget).
type Procs interface {
	Lineage(pid, limit int) ([]procinfo.Proc, error)
}

var kinds = map[string]model.AgentEventKind{
	"SessionStart":      model.AgentSessionStart,
	"SessionEnd":        model.AgentSessionEnd,
	"UserPromptSubmit":  model.AgentPromptSubmit,
	"PreToolUse":        model.AgentToolStart,
	"PostToolUse":       model.AgentToolEnd,
	"PermissionRequest": model.AgentPermissionAsk,
	"Notification":      model.AgentNotification,
	"Stop":              model.AgentStop,
	"StopFailure":       model.AgentStopFailure,
}

// KindFor maps a Claude Code hook event name to the domain's event kind.
func KindFor(event string) (model.AgentEventKind, bool) {
	k, ok := kinds[event]
	return k, ok
}

// Run handles one hook invocation: parse the payload from stdin, find the
// claude process, and send a minimal event to the daemon.
//
// It never writes to stdout or stderr (for some events Claude Code adds hook
// stdout to the model's context) and swallows every error and panic: a hook
// must never disturb the user's session.
func Run(stdin io.Reader, now func() time.Time, send func(wire.Message) error, procs Procs) {
	defer func() { _ = recover() }()

	p, err := ParsePayload(stdin)
	if err != nil {
		return
	}
	if _, ok := KindFor(p.Event); !ok {
		return
	}
	at := now().UnixMilli()
	_ = send(wire.Message{
		V:      wire.Version,
		Kind:   wire.KindAgentHook,
		SentMs: at,
		Agent: &wire.AgentHook{
			Agent:     AgentName,
			Event:     p.Event,
			SessionID: p.SessionID,
			ToolName:  p.ToolName,
			Cwd:       p.Cwd.Reveal(), // HMAC'd by the daemon on receipt
			AgentPID:  agentPID(procs),
			AtMs:      at,
		},
	})
}

// agentPID finds the nearest ancestor named "claude", falling back to the
// hook's parent.
func agentPID(procs Procs) int {
	if procs != nil {
		if lineage, err := procs.Lineage(os.Getpid(), ancestorLimit); err == nil {
			for _, p := range lineage {
				if isClaude(p.Name) {
					return p.PID
				}
			}
		}
	}
	return os.Getppid()
}

func isClaude(name string) bool {
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(name)), ".exe")
	return base == AgentName
}
