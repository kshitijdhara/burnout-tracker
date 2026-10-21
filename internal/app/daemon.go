// Package app runs the daemon's main loop. It owns all domain state on a
// single goroutine (collector ticks, hook messages and flushes are funnelled
// through one select loop), so the pure domain packages never need locks.
// Adapters are reached only through the small interfaces declared here.
package app

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/attention"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/classify"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/timeline"
	"github.com/kshitijdhara/burnout-tracker/internal/wire"
)

// Sink persists what the daemon produces. The SQLite store implements it.
type Sink interface {
	WriteRollups(ctx context.Context, rows []model.MinuteRollup) error
	WriteIntervals(ctx context.Context, ivs []model.Interval) error
	WriteMonitorSnapshot(ctx context.Context, at time.Time, topology string) error
	WriteHealth(ctx context.Context, logicalDay, slisJSON string) error
}

// Hasher produces keyed, non-reversible identities (privacy & security §4.5).
type Hasher interface {
	Key(parts ...string) string
}

// AgentKind maps an agent's hook event name to a domain event kind.
type AgentKind func(agent, event string) (model.AgentEventKind, bool)

// Config holds the daemon's tunables.
type Config struct {
	TickInterval  time.Duration // collector sampling, 1 s
	FlushInterval time.Duration // batched writes, 15 s
	Rollover      time.Duration // logical-day rollover, 4 h
	Attention     attention.Params
	Timeline      timeline.Params
	AggregateOnly bool // drop app ids from rollups (privacy & security §5.1)
	MaxPending    int  // bound on unflushed intervals before dropping
	// MaintenanceInterval spaces runs of Deps.Maintain, 24 h.
	MaintenanceInterval time.Duration
}

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{
		TickInterval:  time.Second,
		FlushInterval: 15 * time.Second,
		Rollover:      4 * time.Hour,
		Attention:     attention.DefaultParams(),
		Timeline:      timeline.DefaultParams(),
		MaxPending:    50_000,

		MaintenanceInterval: 24 * time.Hour,
	}
}

// Deps are the adapters the daemon talks to. Collector may be nil on
// platforms without one; the daemon then records hook data only.
type Deps struct {
	Clock       ports.Clock
	Collector   ports.Collector
	Sink        Sink
	Hasher      Hasher
	ProjectRoot func(dir string) string
	Procs       ports.ProcessInfo
	Rules       *classify.Rules
	AgentKind   AgentKind
	Messages    <-chan wire.Message
	Log         *slog.Logger
	// Maintain runs retention, vacuum and recovery copies. It is called on
	// the loop goroutine (the store has a single writer) at start and then
	// every MaintenanceInterval. Optional.
	Maintain func(ctx context.Context, now time.Time) error
}

// Daemon is the main loop. Create it with New and call Run once.
type Daemon struct {
	cfg  Config
	deps Deps

	engine  *attention.Engine
	rollups *attention.Rollups
	tracker *timeline.Tracker
	windows *sessionWindows
	health  *health

	pendingRollups   []model.MinuteRollup
	pendingIntervals []model.Interval
	lastTopology     string
}

// New wires a daemon. It does not start anything.
func New(cfg Config, deps Deps) *Daemon {
	cfg.Timeline.Rollover = cfg.Rollover
	return &Daemon{
		cfg:     cfg,
		deps:    deps,
		engine:  attention.New(cfg.Attention),
		rollups: attention.NewRollups(cfg.Rollover),
		tracker: timeline.New(cfg.Timeline),
		windows: newSessionWindows(deps.Procs),
		health:  newHealth(),
	}
}

// Run blocks until ctx is cancelled, then flushes everything it holds.
func (d *Daemon) Run(ctx context.Context) error {
	tick := time.NewTicker(d.cfg.TickInterval)
	defer tick.Stop()
	flush := time.NewTicker(d.cfg.FlushInterval)
	defer flush.Stop()

	maint := time.NewTicker(d.cfg.MaintenanceInterval)
	defer maint.Stop()
	d.maintain(ctx)

	var ticks <-chan time.Time
	if d.deps.Collector != nil {
		ticks = tick.C
	}
	for {
		select {
		case <-ctx.Done():
			return d.shutdown()
		case <-ticks:
			d.onTick(ctx)
		case m, ok := <-d.deps.Messages:
			if !ok {
				d.deps.Messages = nil // ingest stopped; keep sampling
				continue
			}
			d.onMessage(m)
		case <-flush.C:
			d.flush(ctx, d.deps.Clock.Now())
		case <-maint.C:
			d.maintain(ctx)
		}
	}
}

func (d *Daemon) onTick(ctx context.Context) {
	snap, err := d.deps.Collector.Snapshot(ctx)
	if err != nil {
		d.health.collectorErrors++
		d.deps.Log.Debug("collector snapshot failed", "err", err)
		return
	}
	cats := make(map[model.WindowID]model.Category, len(snap.Windows))
	for _, w := range snap.Windows {
		cats[w.ID] = d.deps.Rules.App(w.AppID)
	}
	sessions := d.windows.resolve(snap, d.tracker.Sessions())
	res := d.engine.Tick(attention.Input{
		Snapshot:    snap,
		Categories:  cats,
		FreshOutput: d.tracker.FreshOutput(snap.At, sessions),
	})
	d.rollups.Add(res, snap, func(w model.Window) attention.Identity { return d.identify(w, cats[w.ID]) })
	d.tracker.Advance(snap.At)
	d.tracker.OnTick(res, sessions)
	d.health.observe(res)
	d.recordTopology(ctx, snap)
}

func (d *Daemon) identify(w model.Window, cat model.Category) attention.Identity {
	id := attention.Identity{
		Key:      d.deps.Hasher.Key("window", w.AppID, strconv.FormatUint(uint64(w.ID), 10)),
		AppID:    w.AppID,
		Category: cat,
	}
	if cat == model.CategoryPrivate || d.cfg.AggregateOnly {
		id.AppID = ""
	}
	if cat == model.CategoryPrivate {
		// Private windows share one identity so no per-window trail exists.
		id.Key = d.deps.Hasher.Key("window", "private")
	}
	return id
}

func (d *Daemon) recordTopology(ctx context.Context, snap model.Snapshot) {
	topo := topology(snap)
	if topo == d.lastTopology {
		return
	}
	d.lastTopology = topo
	if err := d.deps.Sink.WriteMonitorSnapshot(ctx, snap.At, topo); err != nil {
		d.deps.Log.Warn("monitor snapshot write failed", "err", err)
	}
}

func (d *Daemon) onMessage(m wire.Message) {
	switch {
	case m.Shell != nil:
		d.onShell(m.Shell)
	case m.Agent != nil:
		d.onAgent(m.Agent)
	}
}

func (d *Daemon) onShell(s *wire.ShellCommand) {
	d.health.shellEvents++
	cmd := model.ShellCommand{
		Argv0:      s.Argv0,
		Subcommand: classify.Subcommand(s.Argv0, s.Subcmd),
		ExitCode:   s.Exit,
		Start:      time.UnixMilli(s.StartMs),
		End:        time.UnixMilli(s.EndMs),
		ProjectKey: d.projectKey(s.Cwd),
	}
	// An unresolved command (a typo or a pasted token) arrives with an empty
	// argv0. It is kept as an unnamed failure, so failure loops still count,
	// without recording anything the user typed.
	d.queue(timeline.ShellInterval(cmd, string(classify.Command(cmd.Argv0)), d.cfg.Rollover)...)
}

func (d *Daemon) onAgent(a *wire.AgentHook) {
	d.health.agentEvents++
	kind, ok := d.deps.AgentKind(a.Agent, a.Event)
	if !ok || a.SessionID == "" {
		return
	}
	d.tracker.OnAgent(model.AgentEvent{
		Agent:      a.Agent,
		Kind:       kind,
		SessionKey: d.deps.Hasher.Key("session", a.Agent, a.SessionID),
		ToolName:   a.ToolName,
		At:         time.UnixMilli(a.AtMs),
		ProjectKey: d.projectKey(a.Cwd),
		AgentPID:   a.AgentPID,
	})
}

func (d *Daemon) projectKey(cwd string) string {
	if cwd == "" {
		return ""
	}
	return d.deps.Hasher.Key("project", d.deps.ProjectRoot(cwd))
}

func (d *Daemon) queue(ivs ...model.Interval) {
	d.pendingIntervals = append(d.pendingIntervals, ivs...)
	if over := len(d.pendingIntervals) - d.cfg.MaxPending; over > 0 {
		d.pendingIntervals = d.pendingIntervals[over:]
		d.health.dropped += over
	}
}

// flush writes completed minutes and closed intervals. On failure the data
// stays queued (bounded) and is retried on the next flush.
func (d *Daemon) flush(ctx context.Context, now time.Time) {
	d.pendingRollups = append(d.pendingRollups, d.rollups.Flush(now)...)
	d.queue(d.tracker.Drain()...)

	var errs []error
	if len(d.pendingRollups) > 0 {
		if err := d.deps.Sink.WriteRollups(ctx, d.pendingRollups); err != nil {
			errs = append(errs, err)
		} else {
			d.pendingRollups = d.pendingRollups[:0]
		}
	}
	if len(d.pendingIntervals) > 0 {
		if err := d.deps.Sink.WriteIntervals(ctx, d.pendingIntervals); err != nil {
			errs = append(errs, err)
		} else {
			d.pendingIntervals = d.pendingIntervals[:0]
		}
	}
	day := model.LogicalDay(now, d.cfg.Rollover)
	if err := d.deps.Sink.WriteHealth(ctx, day, d.health.json(day)); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		d.health.flushErrors++
		d.deps.Log.Warn("flush failed; will retry", "err", err)
	}
}

func (d *Daemon) maintain(ctx context.Context) {
	if d.deps.Maintain == nil {
		return
	}
	if err := d.deps.Maintain(ctx, d.deps.Clock.Now()); err != nil {
		d.deps.Log.Warn("maintenance failed", "err", err)
	}
}

func (d *Daemon) shutdown() error {
	d.tracker.Flush()
	// Use a fresh context: the run context is already cancelled.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d.flush(ctx, d.deps.Clock.Now().Add(24*time.Hour))
	if len(d.pendingRollups)+len(d.pendingIntervals) > 0 {
		return errors.New("app: unflushed data at shutdown")
	}
	return nil
}
