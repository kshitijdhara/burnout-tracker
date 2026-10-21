// Package windows is the Windows desktop collector. It enumerates top-level
// windows in z-order, works out how much of each is visible on which
// monitor, and reports focus, pointer, idle and lock state as a
// model.Snapshot for the attention engine.
//
// This file and the other untagged files hold the pure translation logic so
// it is tested on every platform; the Win32 calls live in *_windows.go.
//
// Phase 0 limits: video and call detection are not implemented, so
// Window.HasVideo and Window.InCall are always false.
package windows

import (
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/collector/geometry"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

// Options configures the collector. Both opt-ins default to off, matching
// the data inventory (docs/privacy-and-security.md §3.1).
type Options struct {
	// Titles enables reading window titles for in-memory classification.
	// Titles are never logged or persisted.
	Titles bool
	// InputCounts enables counting key and scroll/click events through Raw
	// Input. Key identities are never read (§8.6).
	InputCounts bool
	// Now supplies Snapshot.At. Defaults to time.Now.
	Now func() time.Time
}

// rawWindow is one candidate top-level window as read from Win32, in the
// order EnumWindows returned it (topmost first).
type rawWindow struct {
	handle uint64
	pid    int
	appID  string
	bounds model.Rect
	title  model.Sensitive[string]
}

// rawState is everything read from Win32 for one snapshot.
type rawState struct {
	at            time.Time
	windows       []rawWindow // z-order, topmost first
	monitors      []model.Rect
	foreground    uint64 // root window of the foreground window, 0 if none
	pointerRoot   uint64 // root window under the pointer, 0 if none
	pointerMoved  bool
	idleFor       time.Duration
	locked        bool
	keys, scrolls int
	inputCountsOn bool
}

// buildSnapshot turns raw Win32 readings into a model.Snapshot. Windows that
// are entirely covered or off-screen are dropped unless they hold focus.
func buildSnapshot(s rawState) model.Snapshot {
	rects := make([]model.Rect, len(s.windows))
	for i, w := range s.windows {
		rects[i] = w.bounds
	}
	visible := geometry.VisibleFractions(rects, s.monitors)

	snap := model.Snapshot{
		At:            s.at,
		Windows:       make([]model.Window, 0, len(s.windows)),
		PointerMoved:  s.pointerMoved,
		IdleFor:       s.idleFor,
		Keys:          s.keys,
		Scrolls:       s.scrolls,
		Locked:        s.locked,
		InputCountsOn: s.inputCountsOn,
	}
	for i, w := range s.windows {
		focused := w.handle != 0 && w.handle == s.foreground
		if visible[i] == 0 && !focused {
			continue
		}
		snap.Windows = append(snap.Windows, model.Window{
			ID:      model.WindowID(w.handle),
			AppID:   w.appID,
			PID:     w.pid,
			Bounds:  w.bounds,
			Monitor: geometry.MonitorIndex(w.bounds, s.monitors),
			Visible: visible[i],
			Focused: focused,
			Title:   w.title,
		})
		if w.handle == s.pointerRoot {
			snap.PointerWindow = model.WindowID(w.handle)
		}
	}
	return snap
}

// rectFromLTRB converts a Win32 RECT (left, top, right, bottom) to a
// model.Rect.
func rectFromLTRB(left, top, right, bottom int32) model.Rect {
	return model.Rect{X: int(left), Y: int(top), W: int(right - left), H: int(bottom - top)}
}

// idleFor converts GetTickCount64 and LASTINPUTINFO.dwTime into an idle
// duration. dwTime is a 32-bit tick count that wraps every ~49.7 days, so
// the subtraction is done in uint32 arithmetic, which wraps correctly.
func idleFor(nowTick uint64, lastInput uint32) time.Duration {
	return time.Duration(uint32(nowTick)-lastInput) * time.Millisecond //nolint:gosec // G115: truncation to 32 bits is the point; see above
}

// lockAppExe is the process that draws the Windows lock screen.
const lockAppExe = "LockApp.exe"

// isLocked applies the Phase 0 lock heuristic. The session counts as locked
// when the input desktop can't be opened (the secure desktop is active:
// lock screen, UAC prompt or Ctrl+Alt+Del screen) or when the lock screen
// app is in the foreground. A WTS session-notification listener would be
// more precise and is a later improvement.
func isLocked(inputDesktopOK bool, foregroundApp string) bool {
	return !inputDesktopOK || strings.EqualFold(foregroundApp, lockAppExe)
}

// Raw Input mouse button flags (RAWMOUSE.usButtonFlags) that count as a
// scroll or click. Button releases and plain movement are not counted.
const (
	riMouseLeftDown   = 0x0001
	riMouseRightDown  = 0x0004
	riMouseMiddleDown = 0x0010
	riMouseButton4    = 0x0040
	riMouseButton5    = 0x0100
	riMouseWheel      = 0x0400
	riMouseHWheel     = 0x0800
)

// countMouseFlags returns the number of scroll or click events encoded in
// one RAWMOUSE.usButtonFlags value.
func countMouseFlags(flags uint16) int {
	n := 0
	for _, bit := range [...]uint16{
		riMouseLeftDown, riMouseRightDown, riMouseMiddleDown,
		riMouseButton4, riMouseButton5, riMouseWheel, riMouseHWheel,
	} {
		if flags&bit != 0 {
			n++
		}
	}
	return n
}

// pressesFromEvents converts keyboard Raw Input events into key presses.
// We only read the Raw Input header, never the keyboard payload, so we
// can't tell a press from a release; each press produces two events. The
// odd event left over is carried into the next snapshot.
func pressesFromEvents(events int64) (presses, carry int64) {
	return events / 2, events % 2
}

// capabilities reports what this collector observes, for doctor.
func capabilities(opts Options, inputCountsOn bool) ports.Capabilities {
	return ports.Capabilities{
		Name:        "windows",
		Windows:     true,
		Pointer:     true,
		Idle:        true,
		InputCounts: opts.InputCounts && inputCountsOn,
		Titles:      opts.Titles,
	}
}
