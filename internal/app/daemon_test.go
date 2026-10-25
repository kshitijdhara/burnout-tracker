package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/attention"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/classify"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
	"github.com/kshitijdhara/burnout-tracker/internal/wire"
)

var t0 = time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) set(t time.Time) { c.mu.Lock(); c.now = t; c.mu.Unlock() }

// fakeCollector returns a terminal window (owned by pid 100) and a browser,
// one simulated second per call, with the user typing in the terminal.
type fakeCollector struct {
	clock *fakeClock
	n     int
}

func (f *fakeCollector) Snapshot(context.Context) (model.Snapshot, error) {
	at := t0.Add(time.Duration(f.n) * time.Second)
	f.n++
	f.clock.set(at)
	return model.Snapshot{
		At: at,
		Windows: []model.Window{
			{ID: 1, AppID: "com.googlecode.iterm2", PID: 100, Bounds: model.Rect{W: 800, H: 600}, Visible: 1, Focused: true},
			{ID: 2, AppID: "com.1password.1password", PID: 200, Bounds: model.Rect{W: 800, H: 600}, Visible: 1, Monitor: 1},
		},
		Monitors:      []model.Rect{{W: 1920, H: 1080}, {X: 1920, W: 2560, H: 1440}},
		PointerWindow: 1, Keys: 3, InputCountsOn: true,
	}, nil
}
func (f *fakeCollector) Capabilities() ports.Capabilities { return ports.Capabilities{Name: "fake"} }
func (f *fakeCollector) Close() error                     { return nil }

// The agent (pid 300) runs under the terminal (pid 100).
type fakeProcs struct{}

func (fakeProcs) Ancestors(pid, _ int) ([]int, error) {
	if pid == 300 {
		return []int{250, 100, 1}, nil
	}
	return nil, nil
}

type fakeSink struct {
	mu        sync.Mutex
	rollups   []model.MinuteRollup
	intervals []model.Interval
	health    string
	healthDay map[string]string
	topology  []string
}

func (s *fakeSink) WriteRollups(_ context.Context, r []model.MinuteRollup) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollups = append(s.rollups, r...)
	return nil
}
func (s *fakeSink) WriteIntervals(_ context.Context, iv []model.Interval) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.intervals = append(s.intervals, iv...)
	return nil
}
func (s *fakeSink) WriteMonitorSnapshot(_ context.Context, _ time.Time, topo string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.topology = append(s.topology, topo)
	return nil
}
func (s *fakeSink) WriteHealth(_ context.Context, day, j string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.health = j
	if s.healthDay == nil {
		s.healthDay = map[string]string{}
	}
	s.healthDay[day] = j
	return nil
}

type fakeHasher struct{}

// Key returns an obviously-hashed value that never contains its inputs.
func (fakeHasher) Key(parts ...string) string {
	return "h" + strings.Repeat("x", len(strings.Join(parts, "|"))%7)
}

func agentKind(_, event string) (model.AgentEventKind, bool) {
	m := map[string]model.AgentEventKind{"UserPromptSubmit": model.AgentPromptSubmit, "PostToolUse": model.AgentToolEnd, "Stop": model.AgentStop}
	k, ok := m[event]
	return k, ok
}

func TestDaemonEndToEnd(t *testing.T) {
	const canary = "CANARY-PATH-7781"
	clock := &fakeClock{now: t0}
	sink := &fakeSink{}
	cfg := DefaultConfig()
	cfg.TickInterval = time.Millisecond
	cfg.FlushInterval = 5 * time.Millisecond
	d := New(cfg, Deps{
		Clock:       clock,
		Collector:   &fakeCollector{clock: clock},
		Sink:        sink,
		Hasher:      fakeHasher{},
		ProjectRoot: func(dir string) string { return dir },
		Procs:       fakeProcs{},
		Rules:       classify.New(nil, nil),
		AgentKind:   agentKind,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	d.Submit(wire.Message{V: 1, Kind: wire.KindShellCommand, Shell: &wire.ShellCommand{
		Argv0: "git", Subcmd: "ghp_tokenlike", Exit: 1, StartMs: t0.UnixMilli(), EndMs: t0.Add(2 * time.Second).UnixMilli(),
		Cwd: "/home/u/" + canary,
	}})
	d.Submit(wire.Message{V: 1, Kind: wire.KindShellCommand, Shell: &wire.ShellCommand{Argv0: "", Exit: 127, StartMs: t0.UnixMilli(), EndMs: t0.UnixMilli() + 5}})
	d.Submit(wire.Message{V: 1, Kind: wire.KindAgentHook, Agent: &wire.AgentHook{
		Agent: "claude", Event: "UserPromptSubmit", SessionID: "sess-" + canary, AgentPID: 300, AtMs: t0.UnixMilli(),
	}})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	// Let it sample a few hundred simulated seconds.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c := clock.Now(); c.Sub(t0) > 200*time.Second {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	d.Submit(wire.Message{V: 1, Kind: wire.KindAgentHook, Agent: &wire.AgentHook{
		Agent: "claude", Event: "Stop", SessionID: "sess-" + canary, AtMs: t0.Add(150 * time.Second).UnixMilli(),
	}})
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	everything, _ := json.Marshal(struct {
		R []model.MinuteRollup
		I []model.Interval
		H string
		T []string
	}{sink.rollups, sink.intervals, sink.health, sink.topology})
	if strings.Contains(string(everything), canary) || strings.Contains(string(everything), "ghp_") {
		t.Fatalf("canary or token reached the sink: %s", everything)
	}

	var shell, agentWorking, composing int
	for _, iv := range sink.intervals {
		switch {
		case iv.Kind == model.IntervalShellCmd:
			shell++
			if iv.Attrs.Argv0 == "" {
				continue // the unresolved command: kept, unnamed
			}
			if iv.Attrs.Argv0 != "git" || iv.Attrs.Subcommand != "" || iv.Attrs.ProjectKey == "" {
				t.Fatalf("shell interval %+v", iv)
			}
		case iv.Kind == model.IntervalAgentState && iv.Attrs.State == "working":
			agentWorking++
		case iv.Kind == model.IntervalHumanState && iv.Attrs.State == "composing":
			composing++
		}
	}
	if shell != 2 {
		t.Fatalf("want 2 shell intervals (one unnamed), got %d", shell)
	}
	if agentWorking != 1 || composing == 0 {
		t.Fatalf("agent working=%d, human composing=%d; intervals: %+v", agentWorking, composing, sink.intervals)
	}

	attended := map[model.Category]float64{}
	for _, r := range sink.rollups {
		attended[r.Category] += r.AttendedS
		if r.Category == model.CategoryPrivate && r.AppID != "" {
			t.Fatalf("private window kept its app id: %+v", r)
		}
	}
	if attended[model.CategoryTerminal] < 100 {
		t.Fatalf("terminal attention too low: %v", attended)
	}
	if !strings.Contains(sink.health, `"coverage"`) || len(sink.topology) == 0 {
		t.Fatalf("health %q topology %v", sink.health, sink.topology)
	}
}

func newTestDaemon(t *testing.T, clock *fakeClock, sink Sink, collector ports.Collector, mutate func(*Config, *Deps)) *Daemon {
	t.Helper()
	cfg := DefaultConfig()
	cfg.TickInterval = time.Millisecond
	cfg.FlushInterval = 5 * time.Millisecond
	deps := Deps{
		Clock: clock, Collector: collector, Sink: sink, Hasher: fakeHasher{},
		ProjectRoot: func(dir string) string { return dir }, Procs: fakeProcs{},
		Rules: classify.New(nil, nil), AgentKind: agentKind,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if mutate != nil {
		mutate(&cfg, &deps)
	}
	return New(cfg, deps)
}

func runFor(t *testing.T, d *Daemon, dur time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()
	if err := d.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// Timestamps far from receipt time (a bad clock, or a forged message) are
// rejected before they can become thousands of intervals.
func TestSubmitRejectsSkewAndAbsurdDurations(t *testing.T) {
	clock := &fakeClock{now: t0}
	d := newTestDaemon(t, clock, &fakeSink{}, nil, nil)
	bad := []wire.Message{
		{V: 1, Kind: wire.KindShellCommand, Shell: &wire.ShellCommand{Argv0: "ls", StartMs: 1, EndMs: 2}},
		{V: 1, Kind: wire.KindShellCommand, Shell: &wire.ShellCommand{Argv0: "ls", StartMs: t0.Add(-30 * 24 * time.Hour).UnixMilli(), EndMs: t0.UnixMilli()}},
		{V: 1, Kind: wire.KindShellCommand, Shell: &wire.ShellCommand{Argv0: "ls", StartMs: t0.UnixMilli(), EndMs: t0.Add(time.Hour).UnixMilli()}},
		{V: 1, Kind: wire.KindAgentHook, Agent: &wire.AgentHook{Agent: "claude", Event: "Stop", SessionID: "s", AtMs: 1}},
	}
	for i, m := range bad {
		if d.Submit(m) {
			t.Errorf("message %d accepted", i)
		}
	}
	if got := d.counters.rejected.Load(); got != uint64(len(bad)) {
		t.Fatalf("rejected = %d", got)
	}
	if !d.Submit(wire.Message{V: 1, Kind: wire.KindShellCommand, Shell: &wire.ShellCommand{Argv0: "ls", StartMs: t0.Add(-time.Second).UnixMilli(), EndMs: t0.UnixMilli()}}) {
		t.Fatal("valid message rejected")
	}
	if !d.Submit(wire.Message{V: 1, Kind: wire.KindPing}) {
		t.Fatal("ping should be accepted and ignored")
	}
}

// Without a collector (Linux in Phase 0) timeouts must still fire.
func TestTimeoutsFireWithoutCollector(t *testing.T) {
	clock := &fakeClock{now: t0}
	sink := &fakeSink{}
	d := newTestDaemon(t, clock, sink, nil, nil)
	d.Submit(wire.Message{V: 1, Kind: wire.KindAgentHook, Agent: &wire.AgentHook{
		Agent: "claude", Event: "UserPromptSubmit", SessionID: "s1", AtMs: t0.UnixMilli(),
	}})
	clock.set(t0.Add(30 * time.Second))
	d.Submit(wire.Message{V: 1, Kind: wire.KindAgentHook, Agent: &wire.AgentHook{
		Agent: "claude", Event: "PostToolUse", SessionID: "s1", AtMs: t0.Add(30 * time.Second).UnixMilli(),
	}})
	go func() {
		time.Sleep(30 * time.Millisecond)
		clock.set(t0.Add(11 * time.Minute)) // past WorkingTimeout, no Stop ever arrives
	}()
	runFor(t, d, 200*time.Millisecond)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, iv := range sink.intervals {
		if iv.Kind == model.IntervalAgentState && iv.Attrs.State == "working" && iv.Start.Equal(t0) && iv.End.Equal(t0.Add(30*time.Second)) {
			return
		}
	}
	t.Fatalf("interrupted turn never timed out: %+v", sink.intervals)
}

// Health continues from today's stored row and is never written to
// tomorrow on shutdown.
func TestHealthSeedAndShutdownDay(t *testing.T) {
	clock := &fakeClock{now: t0}
	sink := &fakeSink{}
	day := model.LogicalDay(t0, 4*time.Hour)
	d := newTestDaemon(t, clock, sink, nil, func(_ *Config, deps *Deps) {
		deps.LoadHealth = func(_ context.Context, d string) (string, error) {
			if d != day {
				t.Errorf("loaded health for %s", d)
			}
			return `{"shell_events":5,"dropped":2}`, nil
		}
		deps.IngestStats = func() map[string]uint64 { return map[string]uint64{"malformed": 1} }
	})
	d.Submit(wire.Message{V: 1, Kind: wire.KindShellCommand, Shell: &wire.ShellCommand{Argv0: "ls", StartMs: t0.UnixMilli(), EndMs: t0.UnixMilli() + 1}})
	runFor(t, d, 50*time.Millisecond)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.healthDay) != 1 {
		t.Fatalf("health written for days %v, want only %s", sink.healthDay, day)
	}
	var h healthJSON
	if err := json.Unmarshal([]byte(sink.healthDay[day]), &h); err != nil {
		t.Fatal(err)
	}
	if h.ShellEvents != 6 || h.Dropped != 2 || h.Ingest["malformed"] != 1 || h.Hooks["accepted"] != 1 {
		t.Fatalf("health not continued: %+v", h)
	}
}

type failingSink struct{ fakeSink }

func (*failingSink) WriteRollups(context.Context, []model.MinuteRollup) error {
	return errors.New("disk full")
}

func TestPendingRollupsAreBounded(t *testing.T) {
	clock := &fakeClock{now: t0}
	d := newTestDaemon(t, clock, &failingSink{}, nil, func(c *Config, _ *Deps) { c.MaxPendingRollups = 10 })
	d.pendingRollups = make([]model.MinuteRollup, 25)
	d.flush(context.Background(), t0, t0)
	if len(d.pendingRollups) != 10 || d.health.dropped != 15 {
		t.Fatalf("pending %d dropped %d", len(d.pendingRollups), d.health.dropped)
	}
}

func TestTopologyIsStable(t *testing.T) {
	a := topology([]model.Rect{{W: 1, H: 1}, {X: 1, W: 2, H: 2}})
	b := topology([]model.Rect{{X: 1, W: 2, H: 2}, {W: 1, H: 1}})
	if a != b || !strings.Contains(a, `"count":2`) {
		t.Fatalf("topology %q vs %q", a, b)
	}
}

// scriptedCollector replays one browser window, one second per call, with
// idle time and lock state from the script.
type scriptedCollector struct {
	clock  *fakeClock
	idle   []time.Duration // IdleFor per tick
	locked []bool
	n      int
}

func (f *scriptedCollector) Snapshot(context.Context) (model.Snapshot, error) {
	i := f.n
	f.n++
	at := t0.Add(time.Duration(i) * time.Second)
	f.clock.set(at)
	return model.Snapshot{
		At:       at,
		Windows:  []model.Window{{ID: 1, AppID: "com.apple.Safari", Bounds: model.Rect{W: 800, H: 600}, Visible: 1, Focused: true}},
		Monitors: []model.Rect{{W: 1920, H: 1080}},
		IdleFor:  f.idle[i], Locked: f.locked[i],
	}, nil
}
func (f *scriptedCollector) Capabilities() ports.Capabilities {
	return ports.Capabilities{Name: "scripted"}
}
func (f *scriptedCollector) Close() error { return nil }

// readingScript: 10 s of input, then idle reading for idleS seconds, then
// one tick of input or of a locked screen.
func readingScript(idleS int, endLocked bool) *scriptedCollector {
	f := &scriptedCollector{}
	for range 10 {
		f.idle, f.locked = append(f.idle, 0), append(f.locked, false)
	}
	for s := 1; s <= idleS; s++ {
		f.idle, f.locked = append(f.idle, time.Duration(s)*time.Second), append(f.locked, false)
	}
	f.idle, f.locked = append(f.idle, 0), append(f.locked, endLocked)
	return f
}

// The daemon holds an idle stretch until it ends, then records it: in full
// when the user comes back (reading), by the grace rule when the screen
// locks. Nothing is lost either way, including at shutdown mid-stretch.
func TestDaemonResolvesIdleStretches(t *testing.T) {
	cases := []struct {
		name         string
		script       *scriptedCollector
		ticks        int // ticks to run before shutdown; 0 = the whole script
		minS, maxS   float64
		wantReadingS bool
	}{
		{"user returns", readingScript(300, false), 0, 309, 311, true},
		{"screen locks", readingScript(300, true), 0, 9 + 30 + 59, 9 + 30 + 61, false},
		{"shutdown mid-stretch", readingScript(300, false), 200, 9 + 30 + 59, 9 + 30 + 61, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &fakeClock{now: t0}
			tc.script.clock = clock
			sink := &fakeSink{}
			d := New(DefaultConfig(), Deps{
				Clock: clock, Collector: tc.script, Sink: sink, Hasher: fakeHasher{},
				ProjectRoot: func(dir string) string { return dir }, Procs: fakeProcs{},
				Rules: classify.New(nil, nil), AgentKind: agentKind,
				Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			n := tc.ticks
			if n == 0 {
				n = len(tc.script.idle)
			}
			for i := range n {
				d.onTick(context.Background())
				if i%40 == 0 { // the daemon flushes every 15 s, mid-stretch too
					d.flush(context.Background(), clock.Now(), clock.Now())
				}
			}
			if err := d.shutdown(); err != nil {
				t.Fatal(err)
			}
			// Minutes written early and topped up late add up, as the
			// store's additive upsert does.
			attended := 0.0
			for _, r := range sink.rollups {
				attended += r.AttendedS
			}
			if attended < tc.minS || attended > tc.maxS {
				t.Fatalf("attended %.1fs, want %.0f–%.0f", attended, tc.minS, tc.maxS)
			}
			var h healthJSON
			if err := json.Unmarshal([]byte(sink.health), &h); err != nil {
				t.Fatal(err)
			}
			if (h.ConfirmedIdleS > 0) != tc.wantReadingS {
				t.Fatalf("health confirmed_idle_s = %.0f, want reading=%v", h.ConfirmedIdleS, tc.wantReadingS)
			}
		})
	}
}

// Ticks held across the 04:00 rollover and released after it belong to the
// previous day, whose counters are already written: they are not added to
// the new day.
func TestHealthSkipsTicksFromEarlierDays(t *testing.T) {
	h := newHealth()
	h.json("2026-10-22", nil, nil) // the new day has started
	tick := attention.Result{Dt: time.Second, Presence: 1, Reason: attention.ReasonConfirmedIdle}
	h.observe(tick, "2026-10-21")
	h.observe(tick, "2026-10-22")
	if h.presentS != 1 || h.confirmedIdleS != 1 {
		t.Fatalf("present %v, confirmed idle %v: want only today's tick", h.presentS, h.confirmedIdleS)
	}
}

// fakeControls is a pause switch and at most one forget request.
type fakeControls struct {
	mu     sync.Mutex
	paused bool
	forget *time.Time
	done   []error
}

func (f *fakeControls) Paused(time.Time) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.paused }
func (f *fakeControls) set(p bool)            { f.mu.Lock(); f.paused = p; f.mu.Unlock() }
func (f *fakeControls) TakeForget() (time.Time, func(error), bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forget == nil {
		return time.Time{}, nil, false
	}
	since := *f.forget
	f.forget = nil
	return since, func(err error) { f.mu.Lock(); f.done = append(f.done, err); f.mu.Unlock() }, true
}

func agentMsg(event string, at time.Time) wire.Message {
	return wire.Message{V: 1, Kind: wire.KindAgentHook, Agent: &wire.AgentHook{
		Agent: "claude", Event: event, SessionID: "s1", AtMs: at.UnixMilli(),
	}}
}

// While paused, collectors aren't sampled and hook events are discarded.
func TestPausedDaemonCollectsNothing(t *testing.T) {
	clock := &fakeClock{now: t0}
	sink := &fakeSink{}
	collector := &fakeCollector{clock: clock}
	controls := &fakeControls{paused: true}
	d := newTestDaemon(t, clock, sink, collector, func(c *Config, deps *Deps) {
		c.ControlInterval = time.Millisecond
		deps.Controls = controls
		deps.Forget = func(context.Context, time.Time) error { return nil }
	})
	go func() {
		time.Sleep(20 * time.Millisecond)
		if !d.Submit(wire.Message{V: 1, Kind: wire.KindShellCommand, Shell: &wire.ShellCommand{
			Argv0: "ls", StartMs: t0.Add(-time.Second).UnixMilli(), EndMs: t0.UnixMilli(),
		}}) {
			t.Error("a hook got an error while paused")
		}
	}()
	runFor(t, d, 100*time.Millisecond)
	if collector.n != 0 {
		t.Errorf("collector sampled %d times while paused", collector.n)
	}
	if got := d.counters.paused.Load(); got != 1 {
		t.Errorf("paused counter %d", got)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.intervals) != 0 || len(sink.rollups) != 0 {
		t.Errorf("recorded while paused: %d intervals, %d rollups", len(sink.intervals), len(sink.rollups))
	}
}

// An agent session open across a pause records nothing for the paused span.
func TestPauseLeavesAGap(t *testing.T) {
	clock := &fakeClock{now: t0}
	sink := &fakeSink{}
	controls := &fakeControls{}
	d := newTestDaemon(t, clock, sink, nil, func(_ *Config, deps *Deps) {
		deps.Controls = controls
		deps.Forget = func(context.Context, time.Time) error { return nil }
	})
	ctx := context.Background()
	step := func(at time.Time, msg *wire.Message) {
		clock.set(at)
		if msg != nil {
			if !d.Submit(*msg) {
				t.Fatalf("message at %v not accepted", at)
			}
			d.apply(<-d.inbox)
		}
		d.checkControls(ctx)
	}
	start := agentMsg("UserPromptSubmit", t0)
	step(t0, &start)
	controls.set(true)
	step(t0.Add(time.Minute), nil) // pause
	controls.set(false)
	step(t0.Add(31*time.Minute), nil) // resume
	stop := agentMsg("Stop", t0.Add(32*time.Minute))
	step(t0.Add(32*time.Minute), &stop)
	d.flush(ctx, clock.Now(), clock.Now())

	sink.mu.Lock()
	defer sink.mu.Unlock()
	var working []string
	for _, iv := range sink.intervals {
		if iv.Kind == model.IntervalAgentState && iv.Attrs.State == "working" {
			working = append(working, iv.Start.Sub(t0).String()+"–"+iv.End.Sub(t0).String())
			if iv.Start.Before(t0.Add(31*time.Minute)) && iv.End.After(t0.Add(time.Minute)) {
				t.Errorf("interval covers the pause: %v–%v", iv.Start, iv.End)
			}
		}
	}
	if strings.Join(working, ",") != "0s–1m0s,31m0s–32m0s" {
		t.Errorf("working segments %v, want the minute before the pause and the one after", working)
	}
}

// A forget request writes out everything held in memory first, so the
// deletion removes it, and nothing older reaches the store afterwards.
func TestForgetSettlesThenDeletes(t *testing.T) {
	clock := &fakeClock{now: t0}
	sink := &fakeSink{}
	controls := &fakeControls{}
	var seenAtForget int
	var forgotSince time.Time
	d := newTestDaemon(t, clock, sink, nil, func(_ *Config, deps *Deps) {
		deps.Controls = controls
		deps.Forget = func(_ context.Context, since time.Time) error {
			sink.mu.Lock()
			seenAtForget = len(sink.intervals)
			sink.mu.Unlock()
			forgotSince = since
			return nil
		}
	})
	ctx := context.Background()
	start := agentMsg("UserPromptSubmit", t0)
	d.Submit(start)
	d.apply(<-d.inbox)
	clock.set(t0.Add(10 * time.Minute))
	since := t0.Add(5 * time.Minute)
	controls.mu.Lock()
	controls.forget = &since
	controls.mu.Unlock()
	d.checkControls(ctx)
	if seenAtForget == 0 || !forgotSince.Equal(since) {
		t.Fatalf("forget ran with %d intervals written, since %v", seenAtForget, forgotSince)
	}
	if len(controls.done) != 1 || controls.done[0] != nil {
		t.Fatalf("done: %v", controls.done)
	}
	if len(d.pendingIntervals)+len(d.pendingRollups) != 0 {
		t.Fatal("data still held in memory after forgetting")
	}
}
