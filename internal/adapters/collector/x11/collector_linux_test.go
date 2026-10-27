//go:build linux

package x11

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// These tests run against the X server in DISPLAY: Xvfb with a window
// manager on CI, or a developer's desktop. They check invariants only;
// what else is on screen varies.

func needDisplay(t *testing.T) {
	t.Helper()
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY not set; no X server to test against")
	}
}

func TestSnapshotInvariants(t *testing.T) {
	needDisplay(t)
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
		if len(s.Monitors) == 0 {
			t.Fatal("no monitors")
		}
		if s.InputCountsOn || s.Keys != 0 || s.Scrolls != 0 {
			t.Fatal("input counts are not collected on X11")
		}
		focused := 0
		for _, w := range s.Windows {
			if w.Visible < 0 || w.Visible > 1 {
				t.Fatalf("visible fraction out of range: %v", w.Visible)
			}
			if w.ID == 0 || w.Bounds.Area() == 0 {
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
	if caps.Name != "x11" || caps.Titles || caps.InputCounts {
		t.Fatalf("unexpected capabilities: %+v", caps)
	}
}

// TestSnapshotSeesOwnWindow maps a window with known properties and waits
// for the collector to report it, so the test can't pass on an empty
// desktop.
func TestSnapshotSeesOwnWindow(t *testing.T) {
	needDisplay(t)
	const title = "burnout x11 test"
	id := mapTestWindow(t, title)

	for _, titles := range []bool{false, true} {
		c, err := New(Options{Titles: titles})
		if err != nil {
			t.Fatal(err)
		}
		w := waitForWindow(t, c, id)
		_ = c.Close()

		if w.AppID != "BurnoutTest" || w.PID != os.Getpid() {
			t.Errorf("AppID=%q PID=%d, want BurnoutTest and %d", w.AppID, w.PID, os.Getpid())
		}
		if w.Bounds.W < 320 || w.Bounds.H < 240 {
			t.Errorf("bounds %+v smaller than the 320x240 client", w.Bounds)
		}
		if w.Visible <= 0 || w.Monitor < 0 {
			t.Errorf("a fresh topmost window should be visible on a monitor: %+v", w)
		}
		if got, want := w.Title.Reveal(), map[bool]string{false: "", true: title}[titles]; got != want {
			t.Errorf("Titles=%v: title %q, want %q", titles, got, want)
		}
	}
}

func TestSnapshotHonoursCancelledContext(t *testing.T) {
	needDisplay(t)
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

// mapTestWindow creates and maps a 320x240 window with WM_CLASS,
// _NET_WM_PID and _NET_WM_NAME set, on its own connection that lives until
// the test ends.
func mapTestWindow(t *testing.T, title string) uint32 {
	t.Helper()
	conn, err := xgb.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	scr := xproto.Setup(conn).DefaultScreen(conn)
	if !hasWM(conn, scr.Root) {
		t.Skip("no EWMH window manager running")
	}
	wid, err := xproto.NewWindowId(conn)
	if err != nil {
		t.Fatal(err)
	}
	err = xproto.CreateWindowChecked(conn, scr.RootDepth, wid, scr.Root, 10, 10, 320, 240, 0,
		xproto.WindowClassInputOutput, scr.RootVisual, 0, nil).Check()
	if err != nil {
		t.Fatal(err)
	}
	set := func(prop, typ xproto.Atom, format byte, data []byte) {
		t.Helper()
		n := uint32(len(data)) / uint32(format/8) //nolint:gosec // G115: tiny test values
		if err := xproto.ChangePropertyChecked(conn, xproto.PropModeReplace, wid, prop, typ, format, n, data).Check(); err != nil {
			t.Fatal(err)
		}
	}
	pid := make([]byte, 4)
	xgb.Put32(pid, uint32(os.Getpid())) //nolint:gosec // G115: pids fit in 32 bits
	set(xproto.AtomWmClass, xproto.AtomString, 8, []byte("burnout-test\x00BurnoutTest\x00"))
	set(atom(t, conn, "_NET_WM_PID"), xproto.AtomCardinal, 32, pid)
	set(atom(t, conn, "_NET_WM_NAME"), atom(t, conn, "UTF8_STRING"), 8, []byte(title))
	if err := xproto.MapWindowChecked(conn, wid).Check(); err != nil {
		t.Fatal(err)
	}
	return uint32(wid)
}

func waitForWindow(t *testing.T, c *Collector, id uint32) model.Window {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, err := c.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range s.Windows {
			if uint32(w.ID) == id { //nolint:gosec // G115: X window ids are 32-bit
				return w
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("window %#x never appeared; snapshot: %+v", id, s.Windows)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func hasWM(conn *xgb.Conn, root xproto.Window) bool {
	a, err := xproto.InternAtom(conn, true, uint16(len("_NET_SUPPORTING_WM_CHECK")), "_NET_SUPPORTING_WM_CHECK").Reply()
	if err != nil || a.Atom == 0 {
		return false
	}
	r, err := xproto.GetProperty(conn, false, root, a.Atom, xproto.AtomWindow, 0, 1).Reply()
	return err == nil && len(r.Value) == 4
}

func atom(t *testing.T, conn *xgb.Conn, name string) xproto.Atom {
	t.Helper()
	r, err := xproto.InternAtom(conn, false, uint16(len(name)), name).Reply() //nolint:gosec // G115: short constant
	if err != nil {
		t.Fatal(err)
	}
	return r.Atom
}
