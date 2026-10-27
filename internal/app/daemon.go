// Package app runs the daemon's main loop. It owns all domain state on a
// single goroutine (collector ticks, hook events and flushes are funnelled
// through one select loop), so the pure domain packages never need locks.
// Hook messages arrive through Submit, which validates and enriches them on
// the caller's goroutine, so slow work never stalls the loop. Adapters are
// reached only through the small interfaces declared here.
package app

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/attention"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/classify"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/schedule"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/timeline"
)

// Sink persists what the daemon produces. The SQLite store implements it.
type Sink interface {
	WriteRollups(ctx context.Context, rows []model.MinuteRollup) error
	WriteIntervals(ctx context.Context, ivs []model.Interval) error
	WriteMonitorSnapshot(ctx context.Context, at time.Time, topology string) error
	WriteHealth(ctx context.Context, logicalDay, slisJSON string) error
}

// Hasher produces keyed, non-reversible identities (privacy & security §4.5).
// It must be safe for concurrent use: Submit calls it from ingest goroutines.
type Hasher interface {
	Key(parts ...string) string
}

// AgentKind maps an agent's hook event name to a domain event kind.
type AgentKind func(agent, event string) (model.AgentEventKind, bool)

// Config holds the daemon's tunables.
type Config struct {
	TickInterval  time.Duration // collector sampling, 1 s
	FlushInterval time.Duration // batched writes and timeline timeouts, 15 s
	Rollover      time.Duration // logical-day rollover, 4 h
	Attention     attention.Params
	Timeline      timeline.Params
	Settings      Settings // the user's settings at start; Deps.Reload changes them

	// Bounds on memory while the store is unavailable.
	MaxPending        int // unflushed intervals
	MaxPendingRollups int // unflushed minute rollups
	QueueSize         int // hook events waiting for the loop

	// Hook timestamps further than MaxSkew from the daemon's receipt time
	// are rejected (system design §5), and commands longer than
	// MaxCommandDuration are treated as bogus.
	MaxSkew            time.Duration
	MaxCommandDuration time.Duration

	// While the screen is locked, sample only every IdleStride ticks
	// (system design §4.1: sampling pauses when locked). Keep
	// IdleStride × TickInterval below the attention engine's MaxGap.
	IdleStride int

	// ControlInterval is how often pause and forget requests are checked.
	ControlInterval time.Duration // also how often settings and private hours are checked

	// Maintenance (retention, vacuum, recovery copies) runs on its own
	// goroutine, first MaintenanceDelay after start, then every
	// MaintenanceInterval.
	MaintenanceDelay    time.Duration
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

		MaxPending:        50_000,
		MaxPendingRollups: 20_000,
		QueueSize:         4096,

		MaxSkew:            5 * time.Minute,
		MaxCommandDuration: 7 * 24 * time.Hour,

		IdleStride: 4,

		ControlInterval:     time.Second,
		MaintenanceDelay:    2 * time.Minute,
		MaintenanceInterval: 24 * time.Hour,
	}
}

// Deps are the adapters the daemon talks to. Collector may be nil on
// platforms without one; the daemon then records hook data only.
type Deps struct {
	Clock     ports.Clock
	Collector ports.Collector
	Sink      Sink
	Hasher    Hasher
	// ProjectRoot maps a working directory to its project root. It is called
	// from ingest goroutines and must bound its own latency (a hung network
	// mount must not block the caller indefinitely).
	ProjectRoot func(dir string) string
	Procs       ports.ProcessInfo
	AgentKind   AgentKind
	Log         *slog.Logger

	// Optional.
	Maintain func(ctx context.Context, now time.Time) error // runs on its own goroutine
	// Controls carries the user's pause and forget requests; Forget deletes
	// stored data from a time on (privacy & security §5.1). Both or neither.
	Controls    Controls
	Forget      func(ctx context.Context, since time.Time) error
	LoadHealth  func(ctx context.Context, logicalDay string) (string, error)
	IngestStats func() map[string]uint64
	// Reload returns the user's settings when they have changed since the
	// last call. It is called every ControlInterval on the loop.
	Reload func() (Settings, bool)
	// Heartbeat is called on the loop every FlushInterval, paused or not,
	// to show a watchdog the loop is alive.
	Heartbeat func()
}

// Settings are the user's privacy settings the loop applies (privacy &
// security §5.1). They can change while the daemon runs.
type Settings struct {
	// PrivateHours are when nothing is recorded, as if paused.
	PrivateHours schedule.Schedule
	// Exclusions are app ids recorded as private, like the sensitive-apps
	// denylist.
	Exclusions []string
	// AggregateOnly keeps one row per category per minute, without app ids
	// or per-window keys.
	AggregateOnly bool
}

// Controls are the user's requests to the daemon, checked every
// ControlInterval.
type Controls interface {
	// Paused reports whether collection is paused at now.
	Paused(now time.Time) bool
	// TakeForget returns a pending request to forget everything from since
	// on, once, and done to report how it went.
	TakeForget() (since time.Time, done func(error), ok bool)
}

// Daemon is the main loop. Create it with New, feed it with Submit, and
// call Run once.
type Daemon struct {
	cfg  Config
	deps Deps
	// settings and rules change only on the loop (Deps.Reload).
	settings Settings
	rules    *classify.Rules

	inbox    chan event
	counters counters // updated by Submit on ingest goroutines

	// Loop-owned state.
	engine           *attention.Engine
	resolver         attention.Resolver[tickRecord]
	lastHeld         []heldWindow // reused while windows don't change
	rollups          *attention.Rollups
	tracker          *timeline.Tracker
	windows          *sessionWindows
	health           *health
	pendingRollups   []model.MinuteRollup
	pendingIntervals []model.Interval
	lastTopology     string
	locked           bool
	ticks            int
	// paused is read by Submit on ingest goroutines.
	paused atomic.Bool
	// pausedFor says why, for the log: "pause" or "private hours".
	pausedFor string
}

// tickRecord is what consuming a tick's result needs besides the result,
// kept while the resolver holds the tick: compact window facts (no titles,
// bounds or pointer state) and the agent context at sampling time.
type tickRecord struct {
	windows []heldWindow
	ctx     timeline.TickContext
}

// heldWindow is what recording a tick needs from one window.
type heldWindow struct {
	id      model.WindowID
	appID   string
	cat     model.Category
	monitor int
	visible float64
	focused bool
}

// heldWindows extracts the windows of snap, reusing the previous slice when
// nothing changed (the usual case while the user is idle).
func (d *Daemon) heldWindows(snap model.Snapshot, cats map[model.WindowID]model.Category) []heldWindow {
	ws := make([]heldWindow, len(snap.Windows))
	for i, w := range snap.Windows {
		ws[i] = heldWindow{id: w.ID, appID: w.AppID, cat: cats[w.ID], monitor: w.Monitor, visible: w.Visible, focused: w.Focused}
	}
	if slices.Equal(ws, d.lastHeld) {
		return d.lastHeld
	}
	d.lastHeld = ws
	return ws
}

// event is a validated, enriched hook message, ready for the loop.
type event struct {
	shell *model.ShellCommand
	agent *model.AgentEvent
}

// New wires a daemon. It does not start anything.
func New(cfg Config, deps Deps) *Daemon {
	cfg.Timeline.Rollover = cfg.Rollover
	return &Daemon{
		cfg:      cfg,
		deps:     deps,
		settings: cfg.Settings,
		rules:    classify.New(cfg.Settings.Exclusions, nil),
		inbox:    make(chan event, cfg.QueueSize),
		engine:   attention.New(cfg.Attention),
		rollups:  attention.NewRollups(cfg.Rollover),
		tracker:  timeline.New(cfg.Timeline),
		windows:  newSessionWindows(deps.Procs),
		health:   newHealth(),
	}
}

// Run blocks until ctx is cancelled, then flushes everything it holds.
func (d *Daemon) Run(ctx context.Context) error {
	d.seedHealth(ctx)

	var wg sync.WaitGroup
	if d.deps.Maintain != nil {
		wg.Add(1)
		go func() { defer wg.Done(); d.maintenanceLoop(ctx) }()
	}
	defer wg.Wait()

	tick := time.NewTicker(d.cfg.TickInterval)
	defer tick.Stop()
	flush := time.NewTicker(d.cfg.FlushInterval)
	defer flush.Stop()

	var ticks <-chan time.Time
	if d.deps.Collector != nil {
		ticks = tick.C
	}
	controls := time.NewTicker(d.cfg.ControlInterval)
	defer controls.Stop()
	d.checkControls(ctx)
	for {
		select {
		case <-ctx.Done():
			return d.shutdown()
		case <-ticks:
			if !d.paused.Load() {
				d.onTick(ctx)
			}
		case ev := <-d.inbox:
			d.apply(ev)
		case <-controls.C:
			d.checkControls(ctx)
		case <-flush.C:
			if d.deps.Heartbeat != nil {
				d.deps.Heartbeat()
			}
			if d.paused.Load() {
				continue // everything was written when the pause began
			}
			now := d.deps.Clock.Now()
			// Timeouts run here, not on collector ticks, so they also fire
			// on platforms without a collector.
			d.tracker.Advance(now)
			d.flush(ctx, now, now)
		}
	}
}

// checkControls applies changed settings, private hours and the user's
// pause and forget requests.
func (d *Daemon) checkControls(ctx context.Context) {
	now := d.deps.Clock.Now()
	d.reload(ctx, now)
	reason := ""
	switch {
	case d.deps.Controls != nil && d.deps.Controls.Paused(now):
		reason = "pause"
	case d.settings.PrivateHours.Contains(now):
		reason = "private hours"
	}
	switch paused := reason != ""; {
	case paused && !d.paused.Load():
		// Close and write everything open, then stop: collectors aren't
		// sampled and hook events are discarded until the pause ends.
		d.settle(ctx, now)
		d.paused.Store(true)
		d.pausedFor = reason
		d.deps.Log.Info("collection paused", "for", reason)
	case !paused && d.paused.Load():
		// Agent sessions stayed open through the pause; their segments
		// restart now, and the paused span they would claim is dropped.
		d.tracker.Flush(now)
		d.tracker.Drain()
		d.paused.Store(false)
		d.deps.Log.Info("collection resumed", "after", d.pausedFor)
		d.pausedFor = ""
	}
	if d.deps.Controls == nil {
		return
	}
	since, done, ok := d.deps.Controls.TakeForget()
	if !ok {
		return
	}
	// Write out everything held in memory first, so nothing from before
	// the request can reach the store after the deletion.
	d.settle(ctx, now)
	err := d.deps.Forget(ctx, since)
	if err == nil {
		d.health = newHealth() // its counters covered the forgotten time
	}
	d.deps.Log.Info("forgot recorded data", "ok", err == nil)
	done(err)
}

// reload applies changed settings. Everything recorded under the old
// settings is written out first, so a new exclusion or aggregate-only mode
// never mixes with minutes recorded before it.
func (d *Daemon) reload(ctx context.Context, now time.Time) {
	if d.deps.Reload == nil {
		return
	}
	s, changed := d.deps.Reload()
	if !changed {
		return
	}
	if !d.paused.Load() {
		d.settle(ctx, now)
	}
	d.settings = s
	d.rules = classify.New(s.Exclusions, nil)
	d.deps.Log.Info("settings changed", "private_hours", len(s.PrivateHours), "exclusions", len(s.Exclusions), "aggregate_only", s.AggregateOnly)
}

// settle closes every open span and held tick and writes them out, as at
// shutdown, without stopping.
func (d *Daemon) settle(ctx context.Context, now time.Time) {
	d.consume(d.resolver.Flush())
	d.tracker.Flush(now)
	d.flush(ctx, now, now.Add(time.Minute))
}

func (d *Daemon) onTick(ctx context.Context) {
	d.ticks++
	if d.locked && d.ticks%d.cfg.IdleStride != 0 {
		return
	}
	snap, err := d.deps.Collector.Snapshot(ctx)
	if err != nil {
		d.health.collectorErrors++
		d.deps.Log.Debug("collector snapshot failed", "err", err)
		return
	}
	d.locked = snap.Locked || snap.DisplayAsleep
	cats := make(map[model.WindowID]model.Category, len(snap.Windows))
	for _, w := range snap.Windows {
		cats[w.ID] = d.rules.App(w.AppID)
	}
	sessions := d.windows.resolve(snap, d.tracker.Sessions())
	res := d.engine.Tick(attention.Input{
		Snapshot:    snap,
		Categories:  cats,
		FreshOutput: d.tracker.FreshOutput(snap.At, sessions),
	})
	// Idle ticks are held until the stretch ends and its presence is known.
	rec := tickRecord{windows: d.heldWindows(snap, cats), ctx: d.tracker.Context(sessions)}
	d.consume(d.resolver.Push(res, rec))
	d.recordTopology(ctx, snap)
}

// consume records ticks whose presence is final, in order.
func (d *Daemon) consume(ticks []attention.Held[tickRecord]) {
	for _, t := range ticks {
		rec := t.Payload
		snap := model.Snapshot{At: t.Result.At, Windows: make([]model.Window, len(rec.windows))}
		cats := make(map[model.WindowID]model.Category, len(rec.windows))
		for i, w := range rec.windows {
			snap.Windows[i] = model.Window{ID: w.id, AppID: w.appID, Monitor: w.monitor, Visible: w.visible, Focused: w.focused}
			cats[w.id] = w.cat
		}
		d.rollups.Add(t.Result, snap, func(w model.Window) attention.Identity { return d.identify(w, cats[w.ID]) })
		d.tracker.OnTick(t.Result, rec.ctx)
		d.health.observe(t.Result, model.LogicalDay(t.Result.At, d.cfg.Rollover))
	}
}

func (d *Daemon) identify(w model.Window, cat model.Category) attention.Identity {
	id := attention.Identity{
		Key:      d.deps.Hasher.Key("window", w.AppID, strconv.FormatUint(uint64(w.ID), 10)),
		AppID:    w.AppID,
		Category: cat,
	}
	switch {
	case cat == model.CategoryPrivate:
		// Private windows share one identity so no per-window trail exists.
		id.AppID = ""
		id.Key = d.deps.Hasher.Key("window", "private")
	case d.settings.AggregateOnly:
		// One row per category per minute: no app, no window.
		id.AppID = ""
		id.Key = model.CategoryWindowKey(cat)
	}
	return id
}

func (d *Daemon) recordTopology(ctx context.Context, snap model.Snapshot) {
	topo := topology(snap.Monitors)
	if topo == d.lastTopology {
		return
	}
	if err := d.deps.Sink.WriteMonitorSnapshot(ctx, snap.At, topo); err != nil {
		d.deps.Log.Warn("monitor snapshot write failed", "err", err)
		return // retried on the next tick
	}
	d.lastTopology = topo
}

func (d *Daemon) apply(ev event) {
	switch {
	case ev.shell != nil:
		d.health.shellEvents++
		// An unresolved command (a typo or a pasted token) arrives with an
		// empty argv0. It is kept as an unnamed failure, so failure loops
		// still count, without recording anything the user typed.
		d.queue(timeline.ShellInterval(*ev.shell, classify.Command(ev.shell.Argv0), d.cfg.Rollover)...)
	case ev.agent != nil:
		d.health.agentEvents++
		d.tracker.OnAgent(*ev.agent)
	}
}

func (d *Daemon) queue(ivs ...model.Interval) {
	d.pendingIntervals = append(d.pendingIntervals, ivs...)
	if over := len(d.pendingIntervals) - d.cfg.MaxPending; over > 0 {
		d.pendingIntervals = d.pendingIntervals[over:]
		d.health.dropped += over
	}
}

// flush writes the minutes that ended before horizon and every closed
// interval. On failure the data stays queued (bounded, oldest dropped and
// counted) and is retried on the next flush. Health is always written for
// now's logical day.
func (d *Daemon) flush(ctx context.Context, now, horizon time.Time) {
	d.pendingRollups = append(d.pendingRollups, d.rollups.Flush(horizon)...)
	if over := len(d.pendingRollups) - d.cfg.MaxPendingRollups; over > 0 {
		d.pendingRollups = d.pendingRollups[over:]
		d.health.dropped += over
	}
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
	if err := errors.Join(errs...); err != nil {
		d.health.flushErrors++
		d.deps.Log.Warn("flush failed; will retry", "err", err)
	}
	day := model.LogicalDay(now, d.cfg.Rollover)
	if err := d.deps.Sink.WriteHealth(ctx, day, d.health.json(day, d.counters.snapshot(), d.ingestStats())); err != nil {
		d.deps.Log.Warn("health write failed", "err", err)
	}
}

func (d *Daemon) ingestStats() map[string]uint64 {
	if d.deps.IngestStats == nil {
		return nil
	}
	return d.deps.IngestStats()
}

// seedHealth continues today's counters from the stored row, so a restart
// doesn't reset the day's SLIs.
func (d *Daemon) seedHealth(ctx context.Context) {
	if d.deps.LoadHealth == nil {
		return
	}
	day := model.LogicalDay(d.deps.Clock.Now(), d.cfg.Rollover)
	stored, err := d.deps.LoadHealth(ctx, day)
	if err != nil {
		d.deps.Log.Warn("could not load today's health; starting from zero", "err", err)
		return
	}
	d.health.seed(day, stored)
}

func (d *Daemon) maintenanceLoop(ctx context.Context) {
	wait := d.cfg.MaintenanceDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if err := d.deps.Maintain(ctx, d.deps.Clock.Now()); err != nil && ctx.Err() == nil {
			d.deps.Log.Warn("maintenance failed", "err", err)
		}
		wait = d.cfg.MaintenanceInterval
	}
}

func (d *Daemon) shutdown() error {
	now := d.deps.Clock.Now()
	d.consume(d.resolver.Flush()) // an unfinished idle stretch keeps the grace rule
	d.tracker.Flush(now)
	// Use a fresh context: the run context is already cancelled.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Rollups are flushed past the current minute so nothing is held back;
	// health still goes to today's row.
	d.flush(ctx, now, now.Add(time.Minute))
	if len(d.pendingRollups)+len(d.pendingIntervals) > 0 {
		return errors.New("app: unflushed data at shutdown")
	}
	return nil
}
