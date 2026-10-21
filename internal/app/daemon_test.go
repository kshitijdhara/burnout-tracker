package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
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
func (s *fakeSink) WriteHealth(_ context.Context, _, j string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.health = j
	return nil
}

type fakeHasher struct{}

// Key returns an obviously-hashed value that never contains its inputs.
func (fakeHasher) Key(parts ...string) string {
	return "h" + strings.Repeat("x", len(strings.Join(parts, "|"))%7)
}

func agentKind(_, event string) (model.AgentEventKind, bool) {
	m := map[string]model.AgentEventKind{"UserPromptSubmit": model.AgentPromptSubmit, "Stop": model.AgentStop}
	k, ok := m[event]
	return k, ok
}

func TestDaemonEndToEnd(t *testing.T) {
	const canary = "CANARY-PATH-7781"
	clock := &fakeClock{now: t0}
	sink := &fakeSink{}
	msgs := make(chan wire.Message, 8)
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
		Messages:    msgs,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	msgs <- wire.Message{V: 1, Kind: wire.KindShellCommand, Shell: &wire.ShellCommand{
		Argv0: "git", Subcmd: "ghp_tokenlike", Exit: 1, StartMs: t0.UnixMilli(), EndMs: t0.Add(2 * time.Second).UnixMilli(),
		Cwd: "/home/u/" + canary,
	}}
	msgs <- wire.Message{V: 1, Kind: wire.KindShellCommand, Shell: &wire.ShellCommand{Argv0: "", Exit: 127, StartMs: t0.UnixMilli(), EndMs: t0.UnixMilli() + 5}}
	msgs <- wire.Message{V: 1, Kind: wire.KindAgentHook, Agent: &wire.AgentHook{
		Agent: "claude", Event: "UserPromptSubmit", SessionID: "sess-" + canary, AgentPID: 300, AtMs: t0.UnixMilli(),
	}}

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
	msgs <- wire.Message{V: 1, Kind: wire.KindAgentHook, Agent: &wire.AgentHook{
		Agent: "claude", Event: "Stop", SessionID: "sess-" + canary, AtMs: t0.Add(150 * time.Second).UnixMilli(),
	}}
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
