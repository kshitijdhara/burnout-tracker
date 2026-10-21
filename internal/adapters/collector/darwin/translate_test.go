package darwin

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

var at = time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)

func rect(x, y, w, h int) model.Rect { return model.Rect{X: x, Y: y, W: w, H: h} }

func bundles(m map[int]string) func(int) string {
	return func(pid int) string { return m[pid] }
}

// twoMonitorState: an editor on monitor 0 (front app), docs on monitor 1,
// and a browser fully hidden behind the editor.
func twoMonitorState() rawState {
	return rawState{
		Windows: []rawWindow{
			{Number: 10, PID: 100, Layer: 0, Bounds: rect(0, 0, 1000, 800), Owner: "Code"},
			{Number: 20, PID: 200, Layer: 0, Bounds: rect(1000, 0, 1000, 800), Owner: "Preview"},
			{Number: 30, PID: 300, Layer: 0, Bounds: rect(100, 100, 500, 500), Owner: "Safari"},
		},
		Displays:  []model.Rect{rect(0, 0, 1000, 800), rect(1000, 0, 1000, 800)},
		Pointer:   point{1500, 400},
		PointerOK: true,
	}
}

func TestSnapshotMultiMonitor(t *testing.T) {
	tr := newTranslator(bundles(map[int]string{100: "com.microsoft.VSCode", 200: "com.apple.Preview"}))
	snap := tr.snapshot(at, twoMonitorState(), inputCounts{})

	if len(snap.Windows) != 2 {
		t.Fatalf("want 2 visible windows (hidden browser dropped), got %+v", snap.Windows)
	}
	editor, docs := snap.Windows[0], snap.Windows[1]
	if editor.AppID != "com.microsoft.VSCode" || !editor.Focused || editor.Monitor != 0 || editor.Visible != 1 {
		t.Errorf("editor = %+v", editor)
	}
	if docs.AppID != "com.apple.Preview" || docs.Focused || docs.Monitor != 1 || docs.Visible != 1 {
		t.Errorf("docs = %+v", docs)
	}
	if snap.PointerWindow != 20 {
		t.Errorf("pointer window = %d, want the docs window on monitor 1", snap.PointerWindow)
	}
	if snap.PointerMoved {
		t.Error("first snapshot cannot report pointer movement")
	}
	if snap.Locked || snap.At != at {
		t.Errorf("snapshot = %+v", snap)
	}
	if fmt.Sprint(snap.Monitors) != fmt.Sprint(twoMonitorState().Displays) {
		t.Errorf("monitors = %v, want the display rectangles", snap.Monitors)
	}
}

func TestSnapshotFiltersAndFocus(t *testing.T) {
	st := rawState{
		Windows: []rawWindow{
			{Number: 1, PID: 9, Layer: 25, Bounds: rect(0, 0, 100, 20), Owner: "Menu"}, // status item: not layer 0
			{Number: 0, PID: 9, Layer: 0, Bounds: rect(0, 0, 10, 10), Owner: "NoNumber"},
			{Number: 2, PID: 9, Layer: 0, Bounds: rect(0, 0, 0, 10), Owner: "Empty"},
			{Number: 3, PID: 7, Layer: 0, Bounds: rect(0, 0, 100, 100), Owner: "Front"},
			{Number: 4, PID: 8, Layer: 0, Bounds: rect(-500, -500, 100, 100), Owner: "Offscreen"},
		},
		Displays: []model.Rect{rect(0, 0, 100, 100)},
	}
	snap := newTranslator(bundles(nil)).snapshot(at, st, inputCounts{})
	var ids []model.WindowID
	for _, w := range snap.Windows {
		ids = append(ids, w.ID)
	}
	// The status item, numberless and empty windows are filtered out; the
	// off-screen window is dropped because it is neither visible nor focused.
	if fmt.Sprint(ids) != "[3]" {
		t.Fatalf("windows = %v, want [3]", ids)
	}
	if !snap.Windows[0].Focused {
		t.Errorf("topmost normal window should be focused: %+v", snap.Windows[0])
	}
	if snap.Windows[0].AppID != "Front" {
		t.Errorf("owner-name fallback failed: %q", snap.Windows[0].AppID)
	}
}

// Focus is read from z-order every tick, so it follows the user between
// apps (NSWorkspace.frontmostApplication went stale off the main run loop).
func TestFocusFollowsZOrder(t *testing.T) {
	tr := newTranslator(bundles(nil))
	st := twoMonitorState()
	focused := func(s model.Snapshot) model.WindowID {
		for _, w := range s.Windows {
			if w.Focused {
				return w.ID
			}
		}
		return 0
	}
	if got := focused(tr.snapshot(at, st, inputCounts{})); got != 10 {
		t.Fatalf("focused = %d, want the editor (10)", got)
	}
	// The user clicks the docs window: it moves to the front.
	st.Windows[0], st.Windows[1] = st.Windows[1], st.Windows[0]
	if got := focused(tr.snapshot(at.Add(time.Second), st, inputCounts{})); got != 20 {
		t.Fatalf("focused = %d after z-order change, want the docs window (20)", got)
	}
}

func TestNoNormalWindowsNothingFocused(t *testing.T) {
	st := rawState{
		Windows:  []rawWindow{{Number: 1, PID: 9, Layer: 25, Bounds: rect(0, 0, 100, 20), Owner: "Menu"}},
		Displays: []model.Rect{rect(0, 0, 100, 100)},
	}
	snap := newTranslator(bundles(nil)).snapshot(at, st, inputCounts{})
	if len(snap.Windows) != 0 {
		t.Fatalf("windows = %+v, want none", snap.Windows)
	}
}

func TestPointerMoved(t *testing.T) {
	tr := newTranslator(bundles(nil))
	st := twoMonitorState()
	tr.snapshot(at, st, inputCounts{})
	if tr.snapshot(at, st, inputCounts{}).PointerMoved {
		t.Error("pointer did not move")
	}
	st.Pointer = point{10, 10}
	snap := tr.snapshot(at, st, inputCounts{})
	if !snap.PointerMoved || snap.PointerWindow != 10 {
		t.Errorf("moved=%v window=%d", snap.PointerMoved, snap.PointerWindow)
	}
	st.PointerOK = false
	if snap := tr.snapshot(at, st, inputCounts{}); snap.PointerMoved || snap.PointerWindow != 0 {
		t.Errorf("unknown pointer must not attribute: %+v", snap)
	}
}

func TestTitlesOnlyWhenRead(t *testing.T) {
	const secret = "Inbox (3) – canary@example.com"
	st := twoMonitorState()
	st.Windows[0].Title, st.Windows[0].HasTitle = secret, true
	st.Windows[1].Title = "not read" // HasTitle false: must be ignored
	snap := newTranslator(bundles(nil)).snapshot(at, st, inputCounts{})

	if got := snap.Windows[0].Title.Reveal(); got != secret {
		t.Errorf("title = %q", got)
	}
	if got := snap.Windows[1].Title.Reveal(); got != "" {
		t.Errorf("unread title leaked through: %q", got)
	}
	if s := fmt.Sprintf("%+v", snap); strings.Contains(s, "canary") {
		t.Errorf("title appears when the snapshot is formatted: %s", s)
	}
}

func TestInputCountDeltas(t *testing.T) {
	tr := newTranslator(bundles(nil))
	st := twoMonitorState()
	steps := []struct {
		cnt         inputCounts
		keys, scrls int
		on          bool
	}{
		{inputCounts{On: true, Keys: 100, Scrolls: 50}, 0, 0, true}, // first reading has no baseline
		{inputCounts{On: true, Keys: 130, Scrolls: 55}, 30, 5, true},
		{inputCounts{On: true, Keys: 10, Scrolls: 2}, 0, 0, true}, // tap restarted: counters reset
		{inputCounts{On: true, Keys: 12, Scrolls: 2}, 2, 0, true},
		{inputCounts{}, 0, 0, false},
		{inputCounts{On: true, Keys: 50}, 0, 0, true}, // re-enabled: new baseline
	}
	for i, s := range steps {
		snap := tr.snapshot(at, st, s.cnt)
		if snap.Keys != s.keys || snap.Scrolls != s.scrls || snap.InputCountsOn != s.on {
			t.Errorf("step %d: keys=%d scrolls=%d on=%v", i, snap.Keys, snap.Scrolls, snap.InputCountsOn)
		}
	}
}

func TestLockedAndIdle(t *testing.T) {
	tr := newTranslator(bundles(nil))
	st := twoMonitorState()
	st.Locked = true
	if !tr.snapshot(at, st, inputCounts{}).Locked {
		t.Error("lock flag lost")
	}
	st.Locked, st.Displays = false, nil
	if !tr.snapshot(at, st, inputCounts{}).Locked {
		t.Error("no active displays must read as locked/inactive")
	}

	tests := []struct {
		in   float64
		want time.Duration
	}{
		{1.5, 1500 * time.Millisecond},
		{-3, 0},
		{math.NaN(), 0},
		{math.Inf(1), 30 * 24 * time.Hour},
	}
	for _, tc := range tests {
		if got := idleDuration(tc.in); got != tc.want {
			t.Errorf("idleDuration(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestAppIDCacheAndCleaning(t *testing.T) {
	calls := 0
	tr := newTranslator(func(int) string { calls++; return " com.example.App\xff " })
	for range 3 {
		if got := tr.appID(1, "Owner"); got != "com.example.App" {
			t.Fatalf("appID = %q", got)
		}
	}
	if calls != 1 {
		t.Errorf("bundle lookup called %d times, want 1 (cached)", calls)
	}
	for pid := range maxAppIDCache + 10 {
		tr.appID(pid+10, "x")
	}
	if len(tr.appIDs) > maxAppIDCache {
		t.Errorf("cache grew to %d", len(tr.appIDs))
	}
	if got := cleanLabel(strings.Repeat("é", 200)); len(got) > maxLabelLen || !strings.HasPrefix(got, "é") {
		t.Errorf("cleanLabel did not cap safely: %d bytes", len(got))
	}
}

func TestCounterDelta(t *testing.T) {
	if counterDelta(5, 3) != 0 || counterDelta(3, 5) != 2 || counterDelta(0, math.MaxUint64) != math.MaxInt32 {
		t.Error("counterDelta wrong")
	}
}
