package app

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/classify"
	"github.com/kshitijdhara/burntout/internal/domain/model"
	"github.com/kshitijdhara/burntout/internal/wire"
)

// Reasons a hook message is not applied. They are counted, never logged
// with content.
var (
	errSkew     = errors.New("app: timestamp too far from receipt time")
	errDuration = errors.New("app: command duration out of range")
	errUnknown  = errors.New("app: unknown agent event")
)

// counters are updated by Submit on ingest goroutines and read by the loop.
type counters struct {
	accepted, rejected, queueFull, paused atomic.Uint64
}

func (c *counters) snapshot() map[string]uint64 {
	return map[string]uint64{
		"accepted":   c.accepted.Load(),
		"rejected":   c.rejected.Load(),
		"queue_full": c.queueFull.Load(),
		"paused":     c.paused.Load(),
	}
}

// Submit validates a hook message against the daemon's clock (system
// design §5: the daemon stamps receipt time and rejects skew), enriches it
// (project root, keyed identities) and hands it to the loop. It is safe for
// concurrent use and never blocks: if the loop is far behind, the event is
// dropped and counted. It reports whether the event was queued.
func (d *Daemon) Submit(m wire.Message) bool {
	if d.paused.Load() {
		d.counters.paused.Add(1)
		return true // discarded: nothing is recorded while paused
	}
	ev, err := d.accept(m, d.deps.Clock.Now())
	if err != nil {
		d.counters.rejected.Add(1)
		return false
	}
	if ev == (event{}) {
		return true // nothing to apply (a ping)
	}
	select {
	case d.inbox <- ev:
		d.counters.accepted.Add(1)
		return true
	default:
		d.counters.queueFull.Add(1)
		return false
	}
}

func (d *Daemon) accept(m wire.Message, now time.Time) (event, error) {
	switch {
	case m.Shell != nil:
		return d.acceptShell(m.Shell, now)
	case m.Agent != nil:
		return d.acceptAgent(m.Agent, now)
	default:
		return event{}, nil
	}
}

func (d *Daemon) acceptShell(s *wire.ShellCommand, now time.Time) (event, error) {
	start, end := time.UnixMilli(s.StartMs), time.UnixMilli(s.EndMs)
	if !d.withinSkew(end, now) {
		return event{}, errSkew
	}
	if end.Sub(start) > d.cfg.MaxCommandDuration {
		return event{}, errDuration
	}
	return event{shell: &model.ShellCommand{
		Argv0:      s.Argv0,
		Subcommand: classify.Subcommand(s.Argv0, s.Subcmd),
		ExitCode:   s.Exit,
		Start:      start,
		End:        end,
		ProjectKey: d.projectKey(s.Cwd),
	}}, nil
}

func (d *Daemon) acceptAgent(a *wire.AgentHook, now time.Time) (event, error) {
	at := time.UnixMilli(a.AtMs)
	if !d.withinSkew(at, now) {
		return event{}, errSkew
	}
	kind, ok := d.deps.AgentKind(a.Agent, a.Event)
	if !ok || a.SessionID == "" {
		return event{}, errUnknown
	}
	return event{agent: &model.AgentEvent{
		Agent:      a.Agent,
		Kind:       kind,
		SessionKey: d.deps.Hasher.Key("session", a.Agent, a.SessionID),
		ToolName:   a.ToolName,
		At:         at,
		ProjectKey: d.projectKey(a.Cwd),
		AgentPID:   a.AgentPID,
	}}, nil
}

func (d *Daemon) withinSkew(t, now time.Time) bool {
	diff := t.Sub(now)
	return diff <= d.cfg.MaxSkew && diff >= -d.cfg.MaxSkew
}

func (d *Daemon) projectKey(cwd string) string {
	if cwd == "" {
		return ""
	}
	root := d.deps.ProjectRoot(cwd)
	if root == "" {
		return ""
	}
	return d.deps.Hasher.Key("project", root)
}
