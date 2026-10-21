package windows

import (
	"math"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

func rect(x, y, w, h int) model.Rect { return model.Rect{X: x, Y: y, W: w, H: h} }

func TestBuildSnapshot(t *testing.T) {
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	monitors := []model.Rect{rect(0, 0, 1000, 1000), rect(1000, 0, 1000, 1000)}
	s := rawState{
		at:       at,
		monitors: monitors,
		windows: []rawWindow{
			{handle: 1, pid: 10, appID: "Code.exe", bounds: rect(0, 0, 500, 1000)},                // left half of monitor 0
			{handle: 2, pid: 20, appID: "chrome.exe", bounds: rect(0, 0, 1000, 1000)},             // half covered by 1
			{handle: 3, pid: 30, appID: "slack.exe", bounds: rect(100, 100, 200, 200)},            // fully covered by 1
			{handle: 4, pid: 40, appID: "WindowsTerminal.exe", bounds: rect(1000, 0, 1000, 1000)}, // monitor 1
		},
		foreground:    2,
		pointerRoot:   4,
		pointerMoved:  true,
		idleFor:       3 * time.Second,
		keys:          5,
		scrolls:       2,
		inputCountsOn: true,
	}
	got := buildSnapshot(s)

	if !got.At.Equal(at) || got.IdleFor != 3*time.Second || got.Keys != 5 || got.Scrolls != 2 || !got.InputCountsOn || !got.PointerMoved {
		t.Fatalf("scalar fields not copied: %+v", got)
	}
	if len(got.Windows) != 3 {
		t.Fatalf("want fully covered window dropped, got %d windows", len(got.Windows))
	}
	want := []struct {
		id      model.WindowID
		visible float64
		monitor int
		focused bool
	}{
		{1, 1, 0, false},
		{2, 0.5, 0, true},
		{4, 1, 1, false},
	}
	for i, w := range want {
		g := got.Windows[i]
		if g.ID != w.id || math.Abs(g.Visible-w.visible) > 1e-9 || g.Monitor != w.monitor || g.Focused != w.focused {
			t.Errorf("window %d: got id=%d visible=%v monitor=%d focused=%v, want %+v",
				i, g.ID, g.Visible, g.Monitor, g.Focused, w)
		}
	}
	if got.PointerWindow != 4 {
		t.Errorf("PointerWindow = %d, want 4", got.PointerWindow)
	}
}

func TestBuildSnapshotKeepsCoveredFocusedWindow(t *testing.T) {
	s := rawState{
		monitors: []model.Rect{rect(0, 0, 100, 100)},
		windows: []rawWindow{
			{handle: 1, bounds: rect(0, 0, 100, 100)},
			{handle: 2, bounds: rect(10, 10, 10, 10)},
		},
		foreground: 2,
	}
	got := buildSnapshot(s)
	if len(got.Windows) != 2 || !got.Windows[1].Focused || got.Windows[1].Visible != 0 {
		t.Fatalf("focused covered window should be kept with Visible 0: %+v", got.Windows)
	}
}

func TestBuildSnapshotPointerOutsideKnownWindows(t *testing.T) {
	s := rawState{
		monitors:    []model.Rect{rect(0, 0, 100, 100)},
		windows:     []rawWindow{{handle: 1, bounds: rect(0, 0, 50, 50)}},
		pointerRoot: 99, // e.g. the taskbar, which isn't a candidate window
	}
	if got := buildSnapshot(s); got.PointerWindow != 0 {
		t.Fatalf("PointerWindow = %d, want 0", got.PointerWindow)
	}
}

func TestBuildSnapshotNeverSetsMedia(t *testing.T) {
	s := rawState{
		monitors: []model.Rect{rect(0, 0, 100, 100)},
		windows:  []rawWindow{{handle: 1, bounds: rect(0, 0, 100, 100)}},
	}
	for _, w := range buildSnapshot(s).Windows {
		if w.HasVideo || w.InCall {
			t.Fatal("video/call detection is not implemented in Phase 0")
		}
	}
}

func TestRectFromLTRB(t *testing.T) {
	if got := rectFromLTRB(-10, 20, 90, 220); got != rect(-10, 20, 100, 200) {
		t.Fatalf("got %+v", got)
	}
}

func TestIdleFor(t *testing.T) {
	tests := []struct {
		name string
		now  uint64
		last uint32
		want time.Duration
	}{
		{"simple", 10_000, 7_000, 3 * time.Second},
		{"no idle", 5_000, 5_000, 0},
		{"32-bit wrap", 1<<32 + 500, 1<<32 - 500, time.Second},
		{"high bits ignored", 7<<32 + 2_000, 1_000, time.Second},
	}
	for _, tc := range tests {
		if got := idleFor(tc.now, tc.last); got != tc.want {
			t.Errorf("%s: idleFor = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsLocked(t *testing.T) {
	tests := []struct {
		desktopOK bool
		app       string
		want      bool
	}{
		{true, "Code.exe", false},
		{false, "Code.exe", true},
		{true, "LockApp.exe", true},
		{true, "lockapp.exe", true},
	}
	for _, tc := range tests {
		if got := isLocked(tc.desktopOK, tc.app); got != tc.want {
			t.Errorf("isLocked(%v, %q) = %v", tc.desktopOK, tc.app, got)
		}
	}
}

func TestCountMouseFlags(t *testing.T) {
	tests := []struct {
		flags uint16
		want  int
	}{
		{0, 0},               // movement only
		{0x0002, 0},          // left button up
		{riMouseLeftDown, 1}, // click
		{riMouseWheel, 1},    // scroll
		{riMouseHWheel | riMouseLeftDown, 2},
		{0xFFFF, 7},
	}
	for _, tc := range tests {
		if got := countMouseFlags(tc.flags); got != tc.want {
			t.Errorf("countMouseFlags(%#x) = %d, want %d", tc.flags, got, tc.want)
		}
	}
}

func TestPressesFromEvents(t *testing.T) {
	for _, tc := range []struct{ events, presses, carry int64 }{
		{0, 0, 0}, {1, 0, 1}, {2, 1, 0}, {7, 3, 1},
	} {
		p, c := pressesFromEvents(tc.events)
		if p != tc.presses || c != tc.carry {
			t.Errorf("pressesFromEvents(%d) = %d, %d", tc.events, p, c)
		}
	}
}

func TestCapabilities(t *testing.T) {
	c := capabilities(Options{Titles: true, InputCounts: true}, false)
	if c.Name != "windows" || !c.Windows || !c.Pointer || !c.Idle || !c.Titles || c.InputCounts {
		t.Fatalf("unexpected capabilities: %+v", c)
	}
	if c := capabilities(Options{InputCounts: true}, true); !c.InputCounts || c.Titles {
		t.Fatalf("unexpected capabilities: %+v", c)
	}
}
