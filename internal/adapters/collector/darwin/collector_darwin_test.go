//go:build darwin && cgo

package darwin

import (
	"context"
	"testing"
	"time"
)

// TestLiveSnapshot runs against the real window server (CI macOS runners
// have a logged-in GUI session). It checks invariants, not specific windows.
func TestLiveSnapshot(t *testing.T) {
	at := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	c, err := New(Options{Now: func() time.Time { return at }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	if caps := c.Capabilities(); caps.Name != "darwin" || !caps.Windows || caps.InputCounts || caps.Titles {
		t.Errorf("capabilities = %+v", caps)
	}
	for range 2 {
		snap, err := c.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !snap.At.Equal(at) || snap.IdleFor < 0 || snap.InputCountsOn {
			t.Errorf("snapshot = %+v", snap)
		}
		focused := 0
		for _, w := range snap.Windows {
			if w.ID == 0 || w.Visible < 0 || w.Visible > 1 || w.Bounds.Area() == 0 {
				t.Errorf("bad window %+v", w)
			}
			if w.Title.Reveal() != "" {
				t.Errorf("title read without the Titles option: window %d", w.ID)
			}
			if w.Focused {
				focused++
			}
		}
		if focused > 1 {
			t.Errorf("%d focused windows", focused)
		}
	}

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Snapshot(context.Background()); err == nil {
		t.Error("Snapshot after Close must fail")
	}
}

func TestToRect(t *testing.T) {
	r := toRect(10.5, 20.25, 100.5, 50)
	if r.X != 10 || r.Y != 20 || r.W != 102 || r.H != 51 {
		t.Errorf("toRect = %+v", r)
	}
}
