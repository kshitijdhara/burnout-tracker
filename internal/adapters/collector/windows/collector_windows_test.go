//go:build windows

package windows

import (
	"context"
	"testing"
	"time"
)

// TestSnapshotInvariants runs against the real desktop of the CI runner (or
// a developer machine). It checks invariants only; window contents vary.
func TestSnapshotInvariants(t *testing.T) {
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	c, err := New(Options{Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	for range 2 { // the second call exercises the pid cache and pointer tracking
		s, err := c.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !s.At.Equal(at) {
			t.Fatalf("At = %v, want injected clock", s.At)
		}
		if s.IdleFor < 0 {
			t.Fatalf("negative idle: %v", s.IdleFor)
		}
		if s.InputCountsOn || s.Keys != 0 || s.Scrolls != 0 {
			t.Fatal("input counts must be off unless opted in")
		}
		focused := 0
		for _, w := range s.Windows {
			if w.Visible < 0 || w.Visible > 1 {
				t.Fatalf("visible fraction out of range: %v", w.Visible)
			}
			if w.ID == 0 || w.Bounds.Area() == 0 || w.AppID == "" {
				t.Fatalf("incomplete window: %+v", w)
			}
			if w.Title.Reveal() != "" {
				t.Fatal("titles must be empty unless opted in")
			}
			if w.Focused {
				focused++
			}
		}
		if focused > 1 {
			t.Fatalf("%d focused windows", focused)
		}
	}
	caps := c.Capabilities()
	if caps.Name != "windows" || caps.Titles || caps.InputCounts {
		t.Fatalf("unexpected capabilities: %+v", caps)
	}
}

func TestSnapshotHonoursCancelledContext(t *testing.T) {
	c, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Snapshot(ctx); err == nil {
		t.Fatal("expected context error")
	}
}

// Input counting may be unavailable in a non-interactive session; either
// way New must succeed and Close must not hang.
func TestInputCountsStartAndStop(t *testing.T) {
	c, err := New(Options{InputCounts: true})
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.InputCountsOn != c.Capabilities().InputCounts {
		t.Fatalf("InputCountsOn=%v disagrees with capabilities", s.InputCountsOn)
	}
	done := make(chan struct{})
	go func() { _ = c.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung")
	}
}
