package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		Indicator:   shown(),
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
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Indicator: shown(),
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

type fakeIndicator struct{ visible atomic.Bool }

func (f *fakeIndicator) Visible() bool { return f.visible.Load() }

func shown() *fakeIndicator {
	f := &fakeIndicator{}
	f.visible.Store(true)
	return f
}

// Without a visible indicator nothing is recorded (AC-1): hook events are
// dropped and counted, and the collector is not sampled.
func TestNoIndicatorRecordsNothing(t *testing.T) {
	clock := &fakeClock{now: t0}
	sink := &fakeSink{}
	col := &fakeCollector{clock: clock}
	ind := &fakeIndicator{} // hidden
	cfg := DefaultConfig()
	cfg.TickInterval = time.Millisecond
	cfg.FlushInterval = 5 * time.Millisecond
	d := New(cfg, Deps{
		Clock:       clock,
		Collector:   col,
		Sink:        sink,
		Hasher:      fakeHasher{},
		ProjectRoot: func(dir string) string { return dir },
		Procs:       fakeProcs{},
		Rules:       classify.New(nil, nil),
		AgentKind:   agentKind,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Indicator:   ind,
	})

	if !d.Submit(wire.Message{V: 1}) {
		t.Fatal("a ping must still be answered without an indicator")
	}
	if d.Submit(wire.Message{V: 1, Kind: wire.KindShellCommand, Shell: &wire.ShellCommand{
		Argv0: "git", StartMs: t0.UnixMilli(), EndMs: t0.UnixMilli() + 5,
	}}) {
		t.Fatal("shell event queued without an indicator")
	}
	if d.Submit(wire.Message{V: 1, Kind: wire.KindAgentHook, Agent: &wire.AgentHook{
		Agent: "claude", Event: "UserPromptSubmit", SessionID: "s", AtMs: t0.UnixMilli(),
	}}) {
		t.Fatal("agent event queued without an indicator")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if col.n != 0 || len(sink.rollups) != 0 || len(sink.intervals) != 0 || len(sink.topology) != 0 {
		t.Fatalf("recorded without an indicator: snapshots=%d rollups=%d intervals=%d topology=%d",
			col.n, len(sink.rollups), len(sink.intervals), len(sink.topology))
	}
	var h struct {
		NoIndicatorTicks int               `json:"no_indicator_ticks"`
		Hooks            map[string]uint64 `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(sink.health), &h); err != nil {
		t.Fatalf("health: %v", err)
	}
	if h.Hooks["no_indicator"] != 2 || h.NoIndicatorTicks == 0 {
		t.Fatalf("drops not counted: %s", sink.health)
	}
}

// Without an indicator the daemon records nothing and refuses to run (AC-1).
func TestNilIndicatorRefusesToRun(t *testing.T) {
	d := New(DefaultConfig(), Deps{
		Clock:       &fakeClock{now: t0},
		Hasher:      fakeHasher{},
		ProjectRoot: func(dir string) string { return dir },
	})
	if d.Submit(wire.Message{V: 1, Kind: wire.KindShellCommand, Shell: &wire.ShellCommand{
		Argv0: "git", StartMs: t0.UnixMilli(), EndMs: t0.UnixMilli() + 5,
	}}) {
		t.Fatal("shell event queued without an indicator")
	}
	if err := d.Run(context.Background()); !errors.Is(err, ErrNoIndicator) {
		t.Fatalf("Run without an indicator: %v", err)
	}
}
