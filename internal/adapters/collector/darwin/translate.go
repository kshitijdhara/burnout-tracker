package darwin

import (
	"math"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/kshitijdhara/burntout/internal/adapters/collector/geometry"
	"github.com/kshitijdhara/burntout/internal/domain/model"
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
	Windows       []rawWindow
	Displays      []model.Rect
	Pointer       point
	PointerOK     bool
	IdleSeconds   float64
	Locked        bool
	DisplayAsleep bool
	Media         mediaState
}

// mediaState is which apps are in a call or keep the display awake, after
// resolveMedia has mapped processes to apps. Cameras are deliberately not
// read: CoreMediaIO loads third-party camera plugins into the calling
// process on older macOS, and a camera can't be tied to an app. Call apps
// typically keep the microphone open while muted, so the microphone
// alone finds the call.
type mediaState struct {
	OK               bool // media was read at all
	PerProcessMic    bool // MicPIDs is meaningful (macOS 14.2+)
	Responsible      bool // helper processes are mapped to their app
	MicPIDs          []int
	MicAny           bool  // fallback: the default input device is running
	DisplayAwakePIDs []int // apps keeping the display awake, e.g. playing video
}

// rawMedia is one media poll as the OS reports it: raw process ids.
type rawMedia struct {
	PerProcessMic, Responsible bool
	MicPIDs                    []int // processes capturing audio input
	MicAny                     bool
	AwakePIDs                  []int // processes holding a display-awake assertion
}

// processLookup answers questions about processes for resolveMedia.
type processLookup interface {
	responsible(pid int) int   // the app responsible for pid (itself if none)
	regularApp(pid int) bool   // an app with a Dock icon, not a background agent
	executable(pid int) string // the executable's path, "" if unknown
}

// resolveMedia maps raw processes to the apps that own windows:
//   - a call is a regular app capturing audio, through any of its helpers.
//     Background agents that keep the microphone open (voice triggers,
//     dictation utilities) are not calls.
//   - video is a display-awake assertion from an app or one of its bundled
//     helpers. Command-line tools are left out: `caffeinate -d` run in a
//     terminal would otherwise make the terminal look like it plays video.
func resolveMedia(raw rawMedia, p processLookup) mediaState {
	st := mediaState{
		OK: true, PerProcessMic: raw.PerProcessMic, Responsible: raw.Responsible,
		MicAny: raw.MicAny,
	}
	for _, pid := range raw.MicPIDs {
		if app := p.responsible(pid); p.regularApp(app) && !slices.Contains(st.MicPIDs, app) {
			st.MicPIDs = append(st.MicPIDs, app)
		}
	}
	for _, pid := range raw.AwakePIDs {
		if !inAppBundle(p.executable(pid)) {
			continue
		}
		if app := p.responsible(pid); !slices.Contains(st.DisplayAwakePIDs, app) {
			st.DisplayAwakePIDs = append(st.DisplayAwakePIDs, app)
		}
	}
	return st
}

// inAppBundle reports whether an executable lives inside an app or XPC
// service bundle (browsers, players and their helpers, WebKit's services),
// as opposed to a command-line tool such as /usr/bin/caffeinate.
func inAppBundle(path string) bool {
	return strings.Contains(path, ".app/") || strings.Contains(path, ".xpc/")
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
		DisplayAsleep: st.DisplayAsleep,
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
// captures audio, even when the call's window is
// covered or on another Space. macOS doesn't say which of an app's windows
// holds the call or the video, so they go to its most visible window in
// the snapshot, if it has one there; the engine decides whether that
// window is visible enough to matter.
func applyMedia(snap *model.Snapshot, m mediaState) {
	if !m.OK {
		return
	}
	for _, pid := range m.MicPIDs {
		snap.CallActive = true
		if w := mostVisibleOf(snap.Windows, pid); w != nil {
			w.InCall = true
		}
	}
	if !m.PerProcessMic && m.MicAny {
		snap.CallActive = true
	}
	for _, pid := range m.DisplayAwakePIDs {
		if w := mostVisibleOf(snap.Windows, pid); w != nil {
			w.HasVideo = true
		}
	}
}

// mostVisibleOf returns pid's most visible window, the frontmost on a tie,
// or nil.
func mostVisibleOf(ws []model.Window, pid int) *model.Window {
	var best *model.Window
	for i := range ws {
		if ws[i].PID == pid && (best == nil || ws[i].Visible > best.Visible) {
			best = &ws[i]
		}
	}
	return best
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

// cleanLabel repairs UTF-8 cut in half by fixed-size C buffers, drops
// control and invisible format characters (terminal escapes, bidi
// overrides) that another app could put in its name, and caps length.
func cleanLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.TrimSpace(s)
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
