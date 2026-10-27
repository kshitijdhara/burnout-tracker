package x11

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

func rect(x, y, w, h int) model.Rect { return model.Rect{X: x, Y: y, W: w, H: h} }

// win is a viewable window on every workspace, the common case in tests.
func win(id uint32, app string, bounds model.Rect) rawWindow {
	return rawWindow{id: id, pid: int(id) * 10, appID: app, bounds: bounds, viewable: true, desktop: noDesktop}
}

func TestBuildSnapshot(t *testing.T) {
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	monitors := []model.Rect{rect(0, 0, 1000, 1000), rect(1000, 0, 1000, 1000)}
	s := rawState{
		at:       at,
		monitors: monitors,
		// Bottom to top, as _NET_CLIENT_LIST_STACKING lists them.
		stacking: []rawWindow{
			win(4, "kitty", rect(1000, 0, 1000, 1000)), // monitor 1
			win(3, "Slack", rect(100, 100, 200, 200)),  // fully covered by 1
			win(2, "firefox", rect(0, 0, 1000, 1000)),  // half covered by 1
			win(1, "Code", rect(0, 0, 500, 1000)),      // left half of monitor 0, topmost
		},
		currentDesktop: noDesktop,
		active:         2,
		pointerX:       1500,
		pointerY:       500,
		havePointer:    true,
		pointerMoved:   true,
		idleFor:        3 * time.Second,
		locked:         true,
	}
	got := buildSnapshot(s)

	if !got.At.Equal(at) || got.IdleFor != 3*time.Second || !got.PointerMoved || !got.Locked {
		t.Fatalf("scalar fields not copied: %+v", got)
	}
	if got.InputCountsOn || got.Keys != 0 || got.Scrolls != 0 {
		t.Fatal("input counts are not collected on X11")
	}
	if len(got.Windows) != 3 {
		t.Fatalf("want fully covered window dropped, got %d windows", len(got.Windows))
	}
	want := []struct {
		id      model.WindowID
		app     string
		pid     int
		visible float64
		monitor int
		focused bool
	}{
		{1, "Code", 10, 1, 0, false},
		{2, "firefox", 20, 0.5, 0, true},
		{4, "kitty", 40, 1, 1, false},
	}
	for i, w := range want {
		g := got.Windows[i]
		if g.ID != w.id || g.AppID != w.app || g.PID != w.pid || math.Abs(g.Visible-w.visible) > 1e-9 ||
			g.Monitor != w.monitor || g.Focused != w.focused {
			t.Errorf("window %d: got %+v, want %+v", i, g, w)
		}
	}
	if got.PointerWindow != 4 {
		t.Errorf("PointerWindow = %d, want 4", got.PointerWindow)
	}
}

func TestBuildSnapshotKeepsCoveredFocusedWindow(t *testing.T) {
	s := rawState{
		monitors: []model.Rect{rect(0, 0, 100, 100)},
		stacking: []rawWindow{
			win(2, "b", rect(10, 10, 10, 10)),
			win(1, "a", rect(0, 0, 100, 100)),
		},
		active: 2,
	}
	got := buildSnapshot(s)
	if len(got.Windows) != 2 || !got.Windows[1].Focused || got.Windows[1].Visible != 0 {
		t.Fatalf("focused covered window should be kept with Visible 0: %+v", got.Windows)
	}
}

func TestBuildSnapshotSkipsHiddenAndUnmapped(t *testing.T) {
	minimised := win(2, "b", rect(0, 0, 100, 100))
	minimised.hidden = true
	unmapped := win(3, "c", rect(0, 0, 100, 100))
	unmapped.viewable = false
	s := rawState{
		monitors: []model.Rect{rect(0, 0, 100, 100)},
		stacking: []rawWindow{win(1, "a", rect(0, 0, 100, 100)), minimised, unmapped},
		active:   2, // focus on a minimised window doesn't make it visible
	}
	got := buildSnapshot(s)
	if len(got.Windows) != 1 || got.Windows[0].ID != 1 || got.Windows[0].Visible != 1 {
		t.Fatalf("hidden and unmapped windows must neither appear nor occlude: %+v", got.Windows)
	}
}

func TestBuildSnapshotShellWindows(t *testing.T) {
	desktop := win(1, "nautilus", rect(0, 0, 1000, 1000))
	desktop.types = []string{typeDesktop}
	panel := win(3, "panel", rect(0, 0, 1000, 40))
	panel.types = []string{typeDock}
	s := rawState{
		monitors: []model.Rect{rect(0, 0, 1000, 1000)},
		stacking: []rawWindow{desktop, win(2, "Code", rect(0, 0, 1000, 1000)), panel},
		pointerX: 500, pointerY: 10, havePointer: true, // over the panel
	}
	got := buildSnapshot(s)
	if len(got.Windows) != 1 || got.Windows[0].ID != 2 {
		t.Fatalf("desktop and dock windows must not be reported: %+v", got.Windows)
	}
	if v := got.Windows[0].Visible; math.Abs(v-0.96) > 1e-9 {
		t.Errorf("the panel covers the top 4%% of the window: Visible = %v", v)
	}
	if got.PointerWindow != 0 {
		t.Errorf("pointer over the panel: PointerWindow = %d, want 0", got.PointerWindow)
	}
}

func TestBuildSnapshotWorkspaces(t *testing.T) {
	other := win(1, "other", rect(0, 0, 100, 100))
	other.desktop = 1
	here := win(2, "here", rect(0, 0, 50, 50))
	here.desktop = 0
	sticky := win(3, "sticky", rect(50, 50, 50, 50))
	sticky.desktop = allDesktops
	s := rawState{
		monitors:       []model.Rect{rect(0, 0, 100, 100)},
		stacking:       []rawWindow{here, sticky, other}, // other is topmost but on workspace 1
		currentDesktop: 0,
	}
	var ids []model.WindowID
	for _, w := range buildSnapshot(s).Windows {
		ids = append(ids, w.ID)
		if w.Visible != 1 {
			t.Errorf("window %d: a window on another workspace must not occlude: Visible = %v", w.ID, w.Visible)
		}
	}
	if !slices.Equal(ids, []model.WindowID{3, 2}) {
		t.Fatalf("windows = %v, want the sticky window and the one on this workspace", ids)
	}
}

func TestBuildSnapshotPointer(t *testing.T) {
	s := rawState{
		monitors: []model.Rect{rect(0, 0, 100, 100)},
		stacking: []rawWindow{win(1, "a", rect(0, 0, 50, 50))},
		pointerX: 80, pointerY: 80, havePointer: true, // over the bare root window
	}
	if got := buildSnapshot(s); got.PointerWindow != 0 {
		t.Fatalf("PointerWindow = %d, want 0", got.PointerWindow)
	}
	s.pointerX, s.pointerY = 10, 10
	if got := buildSnapshot(s); got.PointerWindow != 1 {
		t.Fatalf("PointerWindow = %d, want 1", got.PointerWindow)
	}
	s.havePointer = false // pointer on another X screen
	if got := buildSnapshot(s); got.PointerWindow != 0 {
		t.Fatalf("PointerWindow = %d without a pointer, want 0", got.PointerWindow)
	}
}

func TestBuildSnapshotNeverSetsMedia(t *testing.T) {
	s := rawState{
		monitors: []model.Rect{rect(0, 0, 100, 100)},
		stacking: []rawWindow{win(1, "a", rect(0, 0, 100, 100))},
	}
	for _, w := range buildSnapshot(s).Windows {
		if w.HasVideo || w.InCall {
			t.Fatal("video/call detection is not implemented on X11")
		}
	}
}

func TestOnDesktop(t *testing.T) {
	tests := []struct {
		win, current int64
		want         bool
	}{
		{0, 0, true},
		{1, 0, false},
		{allDesktops, 3, true},
		{noDesktop, 0, true},
		{2, noDesktop, true},
	}
	for _, tc := range tests {
		if got := onDesktop(tc.win, tc.current); got != tc.want {
			t.Errorf("onDesktop(%d, %d) = %v, want %v", tc.win, tc.current, got, tc.want)
		}
	}
}

func TestFrameBounds(t *testing.T) {
	tests := []struct {
		name    string
		extents []uint32
		want    model.Rect
	}{
		{"no frame", nil, rect(100, 200, 640, 480)},
		{"short value", []uint32{1, 2}, rect(100, 200, 640, 480)},
		// left 2, right 3, top 30 (title bar), bottom 4.
		{"decorated", []uint32{2, 3, 30, 4}, rect(98, 170, 645, 514)},
	}
	for _, tc := range tests {
		if got := frameBounds(100, 200, 640, 480, tc.extents); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestCardinals(t *testing.T) {
	got := cardinals([]byte{1, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 9})
	if !slices.Equal(got, []uint32{1, allDesktops}) {
		t.Fatalf("got %v (a trailing partial value must be ignored)", got)
	}
	if len(cardinals(nil)) != 0 {
		t.Fatal("empty property")
	}
}

func TestAppIDFromClass(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"code\x00Code\x00", "Code"},
		{"Navigator\x00firefox\x00", "firefox"},
		{"kitty\x00\x00", "kitty"}, // empty class
		{"xterm", "xterm"},         // malformed: no terminators
		{"", ""},
		{"\x00\x00", ""},
	}
	for _, tc := range tests {
		if got := appIDFromClass([]byte(tc.in)); got != tc.want {
			t.Errorf("appIDFromClass(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsLocked(t *testing.T) {
	tests := []struct{ hint, active, want bool }{
		{false, true, false},
		{true, true, true},
		{false, false, true}, // another session is in the foreground
	}
	for _, tc := range tests {
		if got := isLocked(tc.hint, tc.active); got != tc.want {
			t.Errorf("isLocked(%v, %v) = %v", tc.hint, tc.active, got)
		}
	}
}

func TestCapabilities(t *testing.T) {
	c := capabilities(Options{Titles: true}, true)
	if c.Name != "x11" || !c.Windows || !c.Pointer || !c.Idle || !c.Titles || c.InputCounts {
		t.Fatalf("unexpected capabilities: %+v", c)
	}
	if c := capabilities(Options{}, false); c.Idle || c.Titles {
		t.Fatalf("unexpected capabilities: %+v", c)
	}
	if len(c.Limitations) == 0 {
		t.Fatal("gaps (video/call detection) must be reported as limitations")
	}
}
