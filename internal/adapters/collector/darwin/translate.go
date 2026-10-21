package darwin

import (
	"math"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/collector/geometry"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// rawWindow is one entry of CGWindowListCopyWindowInfo, copied out of C.
type rawWindow struct {
	Number   uint32 // kCGWindowNumber
	PID      int    // kCGWindowOwnerPID
	Layer    int    // kCGWindowLayer; 0 is the normal application layer
	Bounds   model.Rect
	Owner    string // kCGWindowOwnerName, the app's display name
	Title    string // kCGWindowName; only read when titles are enabled
	HasTitle bool
}

// point is a pointer location in global screen coordinates.
type point struct{ X, Y int }

// rawState is everything one tick reads from the OS, in front-to-back
// window order, before any interpretation.
type rawState struct {
	Windows     []rawWindow
	Displays    []model.Rect
	Pointer     point
	PointerOK   bool
	IdleSeconds float64
	Locked      bool
	Media       mediaState
}

// mediaState is which apps use the microphone or keep the display awake,
// and whether a camera is on. PIDs are of the responsible app, the process
// that owns the windows, when macOS can tell.
type mediaState struct {
	OK               bool  // media was read at all
	PerProcessMic    bool  // MicPIDs is meaningful (macOS 14.2+)
	Responsible      bool  // helper processes are mapped to their app
	MicPIDs          []int // regular apps (with a Dock icon) capturing audio
	MicAny           bool  // fallback: the default input device is running
	CameraOn         bool
	DisplayAwakePIDs []int // apps keeping the display awake, e.g. playing video
}

// inputCounts are cumulative event-tap counters since the tap started.
type inputCounts struct {
	On      bool
	Keys    uint64
	Scrolls uint64 // scroll-wheel and mouse-button events
}

// maxAppIDCache bounds the pid → app id cache. PIDs are reused over long
// uptimes, so the cache is dropped wholesale rather than growing forever.
const maxAppIDCache = 512

// maxLabelLen caps app ids and titles copied out of the OS.
const maxLabelLen = 256

// translator turns raw OS readings into snapshots. It keeps the little
// state that spans ticks: the previous pointer position and counters.
type translator struct {
	bundleID func(pid int) string // returns "" when the process has no bundle id

	appIDs      map[int]string
	prevPointer point
	havePointer bool
	prevCounts  inputCounts
	haveCounts  bool
}

func newTranslator(bundleID func(pid int) string) *translator {
	return &translator{bundleID: bundleID, appIDs: make(map[int]string)}
}

// snapshot builds a model.Snapshot from one tick's raw readings.
//
// Focus is inferred from z-order without the Accessibility permission: the
// focused window is the frontmost on-screen normal (layer 0) window, which
// belongs to the frontmost application. CGWindowList is read fresh every
// tick, unlike NSWorkspace.frontmostApplication, which only updates when
// the main AppKit run loop runs (this daemon never runs it). It differs
// from the true key window only for apps whose key window is a panel above
// layer 0.
func (t *translator) snapshot(at time.Time, st rawState, cnt inputCounts) model.Snapshot {
	snap := model.Snapshot{
		At:            at,
		Monitors:      append([]model.Rect(nil), st.Displays...),
		IdleFor:       idleDuration(st.IdleSeconds),
		Locked:        st.Locked || len(st.Displays) == 0,
		InputCountsOn: cnt.On,
	}
	t.applyCounts(&snap, cnt)

	if st.PointerOK {
		snap.PointerMoved = t.havePointer && st.Pointer != t.prevPointer
		t.prevPointer, t.havePointer = st.Pointer, true
	}

	wins := normalWindows(st.Windows)
	rects := make([]model.Rect, len(wins))
	for i, w := range wins {
		rects[i] = w.Bounds
	}
	visible := geometry.VisibleFractions(rects, st.Displays)

	pointerIdx := -1
	if st.PointerOK {
		pointerIdx = geometry.TopmostAt(rects, st.Pointer.X, st.Pointer.Y)
	}
	focusedIdx := -1
	if len(wins) > 0 {
		focusedIdx = 0 // wins is front-to-back: the topmost normal window has focus
	}

	for i, w := range wins {
		focused := i == focusedIdx
		if visible[i] == 0 && !focused {
			continue // fully covered or off-screen: cannot hold attention
		}
		mw := model.Window{
			ID:      model.WindowID(w.Number),
			AppID:   t.appID(w.PID, w.Owner),
			PID:     w.PID,
			Bounds:  w.Bounds,
			Monitor: geometry.MonitorIndex(w.Bounds, st.Displays),
			Visible: visible[i],
			Focused: focused,
		}
		if w.HasTitle {
			mw.Title = model.NewSensitive(cleanLabel(w.Title))
		}
		if i == pointerIdx {
			snap.PointerWindow = mw.ID
		}
		snap.Windows = append(snap.Windows, mw)
	}
	applyMedia(&snap, st.Media)
	return snap
}

// applyMedia marks calls and video. A call is active while a regular app
// captures audio, or a camera is on, even when the call's window is
// covered or on another Space. The call and the video are attributed to
// their app's frontmost window in the snapshot, if it has one there; the
// engine decides whether that window is visible enough to matter.
func applyMedia(snap *model.Snapshot, m mediaState) {
	if !m.OK {
		return
	}
	for _, pid := range m.MicPIDs {
		snap.CallActive = true
		if w := frontmostOf(snap.Windows, pid); w != nil {
			w.InCall = true
		}
	}
	if (!m.PerProcessMic && m.MicAny) || m.CameraOn {
		snap.CallActive = true
	}
	for _, pid := range m.DisplayAwakePIDs {
		if w := frontmostOf(snap.Windows, pid); w != nil {
			w.HasVideo = true
		}
	}
}

// frontmostOf returns pid's first window in front-to-back order, or nil.
func frontmostOf(ws []model.Window, pid int) *model.Window {
	for i := range ws {
		if ws[i].PID == pid {
			return &ws[i]
		}
	}
	return nil
}

// applyCounts converts cumulative tap counters into per-snapshot deltas.
func (t *translator) applyCounts(snap *model.Snapshot, cnt inputCounts) {
	if !cnt.On {
		t.haveCounts = false
		return
	}
	if t.haveCounts {
		snap.Keys = counterDelta(t.prevCounts.Keys, cnt.Keys)
		snap.Scrolls = counterDelta(t.prevCounts.Scrolls, cnt.Scrolls)
	}
	t.prevCounts, t.haveCounts = cnt, true
}

// appID returns the bundle identifier for pid, falling back to the owner
// name for processes without one (command-line tools, some helpers).
func (t *translator) appID(pid int, owner string) string {
	if id, ok := t.appIDs[pid]; ok {
		return id
	}
	if len(t.appIDs) >= maxAppIDCache {
		clear(t.appIDs)
	}
	id := cleanLabel(t.bundleID(pid))
	if id == "" {
		id = cleanLabel(owner)
	}
	t.appIDs[pid] = id
	return id
}

// normalWindows keeps on-screen application windows (layer 0) with a real
// window number and non-empty bounds, preserving z-order.
func normalWindows(in []rawWindow) []rawWindow {
	out := make([]rawWindow, 0, len(in))
	for _, w := range in {
		if w.Layer == 0 && w.Number != 0 && w.Bounds.Area() > 0 {
			out = append(out, w)
		}
	}
	return out
}

// idleDuration converts CoreGraphics' idle seconds, guarding against the
// NaN or negative values it can report around sleep and wake.
func idleDuration(sec float64) time.Duration {
	const maxIdle = 30 * 24 * time.Hour
	if math.IsNaN(sec) || sec <= 0 {
		return 0
	}
	if sec >= maxIdle.Seconds() {
		return maxIdle
	}
	return time.Duration(sec * float64(time.Second))
}

// counterDelta returns cur-prev, treating a decrease as a counter reset.
func counterDelta(prev, cur uint64) int {
	if cur < prev {
		return 0
	}
	return int(min(cur-prev, math.MaxInt32))
}

// cleanLabel repairs UTF-8 cut in half by fixed-size C buffers and caps length.
func cleanLabel(s string) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	if len(s) > maxLabelLen {
		s = strings.ToValidUTF8(s[:maxLabelLen], "")
	}
	return s
}

// mediaLimitations describes what media detection can't do on this Mac.
func mediaLimitations(m mediaState) []string {
	var out []string
	if !m.PerProcessMic {
		out = append(out, "calls are detected from the microphone being in use at all, not per app (needs macOS 14.2 or later)")
	}
	if !m.Responsible {
		out = append(out, "browser calls and video may not be matched to the browser's window")
	}
	return out
}
