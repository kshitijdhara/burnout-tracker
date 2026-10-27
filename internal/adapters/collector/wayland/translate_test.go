package wayland

import (
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/model"
)

func decodeFixture(t *testing.T, name string) reply {
	t.Helper()
	r, err := decodeReply(fixtures(t, Version)[name])
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBuildSnapshot(t *testing.T) {
	at := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	snap := buildSnapshot(decodeFixture(t, "snapshot.json"), extra{at: at, pointerMoved: true, idleFor: 3 * time.Second})

	if !snap.At.Equal(at) || !snap.PointerMoved || snap.IdleFor != 3*time.Second || snap.Locked {
		t.Fatalf("readings not carried: %+v", snap)
	}
	// The desktop (10), the minimised editor (13) and the menu (14) are
	// not reported; the terminal and browser split the screen.
	if len(snap.Windows) != 2 {
		t.Fatalf("windows = %+v", snap.Windows)
	}
	byID := map[model.WindowID]model.Window{}
	for _, w := range snap.Windows {
		byID[w.ID] = w
	}
	term, web := byID[11], byID[12]
	if term.AppID != "org.gnome.Ptyxis" || term.PID != 2001 || term.Visible != 1 || term.Focused {
		t.Errorf("terminal = %+v", term)
	}
	if web.AppID != "firefox" || !web.Focused || web.Visible != 1 || web.Bounds != (model.Rect{X: 960, W: 960, H: 1080}) {
		t.Errorf("browser = %+v", web)
	}
	// The menu at (100,100) is ignored, so the pointer is over the terminal.
	if snap.PointerWindow != 11 {
		t.Errorf("pointer window = %d, want 11", snap.PointerWindow)
	}
	if len(snap.Monitors) != 1 || snap.Monitors[0] != (model.Rect{W: 1920, H: 1080}) {
		t.Errorf("monitors = %+v", snap.Monitors)
	}
}

func TestBuildSnapshotTitlesOnlyWhenAsked(t *testing.T) {
	r := decodeFixture(t, "snapshot-titles.json")
	off := buildSnapshot(r, extra{})
	if title := off.Windows[0].Title.Reveal(); title != "" {
		t.Errorf("title kept with titles off: %q", title)
	}
	on := buildSnapshot(r, extra{titles: true})
	if title := on.Windows[0].Title.Reveal(); title != "Release notes — Mozilla Firefox" {
		t.Errorf("title = %q", title)
	}
	// Second monitor holds the pointer, but no window is there.
	if on.PointerWindow != 0 || on.Windows[0].Monitor != 0 {
		t.Errorf("pointer window %d, monitor %d", on.PointerWindow, on.Windows[0].Monitor)
	}
}

func TestBuildSnapshotOcclusionAndWorkspaces(t *testing.T) {
	win := func(id uint64, typ string, minimised, onWS bool, f rect) window {
		return window{ID: id, WMClass: "app", Type: typ, Minimised: minimised, OnWorkspace: onWS, Frame: f}
	}
	full := rect{W: 1000, H: 1000}
	r := reply{
		V: Version, Op: "snapshot",
		Monitors: []rect{full},
		Pointer:  &point{X: 500, Y: 990},
		Focus:    1,
		Windows: []window{ // bottom first
			win(1, typeNormal, false, true, full),                         // focused but fully covered: kept
			win(2, typeNormal, false, true, rect{W: 500, H: 1000}),        // covered by 3: dropped
			win(3, typeNormal, false, true, full),                         // half covered by the dock
			win(4, typeNormal, false, false, full),                        // another workspace
			win(5, typeDock, false, true, rect{Y: 980, W: 1000, H: 20}),   // covers, catches the pointer
			win(6, typeDialog, false, true, rect{X: 0, Y: 0, W: 0, H: 0}), // zero area
		},
	}
	snap := buildSnapshot(r, extra{})
	got := map[model.WindowID]float64{}
	for _, w := range snap.Windows {
		got[w.ID] = w.Visible
	}
	if len(got) != 2 || got[1] != 0 || got[3] <= 0.97 || got[3] >= 0.99 {
		t.Errorf("visible = %v", got)
	}
	if snap.PointerWindow != 0 {
		t.Errorf("pointer over the dock, got window %d", snap.PointerWindow)
	}
}

func TestAppID(t *testing.T) {
	tests := []struct {
		w    window
		want string
	}{
		{window{WMClass: "firefox", App: "org.mozilla.firefox.desktop"}, "firefox"},
		{window{App: "org.gnome.Nautilus.desktop"}, "org.gnome.Nautilus"},
		{window{App: "window:12"}, "window:12"},
		{window{}, ""},
	}
	for _, tc := range tests {
		if got := appID(tc.w); got != tc.want {
			t.Errorf("appID(%+v) = %q, want %q", tc.w, got, tc.want)
		}
	}
}

func TestLockedSnapshot(t *testing.T) {
	s := lockedSnapshot(extra{at: time.Unix(1, 0), idleFor: time.Minute})
	if !s.Locked || len(s.Windows) != 0 || s.IdleFor != time.Minute || !s.At.Equal(time.Unix(1, 0)) {
		t.Fatalf("%+v", s)
	}
}

func TestCapabilities(t *testing.T) {
	c := capabilities(Options{Titles: true}, true)
	if c.Name != "gnome-wayland" || !c.Windows || !c.Pointer || !c.Idle || !c.Titles || c.InputCounts {
		t.Fatalf("unexpected capabilities: %+v", c)
	}
	if c := capabilities(Options{}, false); c.Idle || c.Titles || len(c.Limitations) == 0 {
		t.Fatalf("unexpected capabilities: %+v", c)
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		e         Extension
		answering bool
		ok        bool
		want      string
	}{
		{Extension{Installed: true, State: stateActive}, true, true, "answering"},
		{Extension{}, false, false, "not installed"},
		{Extension{Installed: true, State: 2}, false, false, EnableCommand},
		{Extension{Installed: true, State: 6}, false, false, EnableCommand},
		{Extension{Installed: true, State: stateError, Error: "SyntaxError"}, false, false, "SyntaxError"},
		{Extension{Installed: true, State: stateOutOfDate}, false, false, "update burntout"},
		{Extension{Installed: true, State: stateActive}, false, false, "not answering"},
	}
	for _, tc := range tests {
		ok, detail := tc.e.Describe(tc.answering)
		if ok != tc.ok || !strings.Contains(detail, tc.want) {
			t.Errorf("%+v answering=%v: %v %q, want %v %q", tc.e, tc.answering, ok, detail, tc.ok, tc.want)
		}
	}
}
