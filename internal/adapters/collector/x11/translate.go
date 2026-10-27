// Package x11 is the Linux X11 desktop collector. It reads the EWMH window
// list in stacking order, works out how much of each window is visible on
// which RandR monitor, and reports focus, pointer, idle and lock state as a
// model.Snapshot for the attention engine.
//
// This file holds the pure translation logic so it is tested on every
// platform; the X and D-Bus calls live in *_linux.go.
//
// Limits: video and call detection are not implemented, so Window.HasVideo
// and Window.InCall are always false, and input counts are not collected.
package x11

import (
	"bytes"
	"encoding/binary"
	"slices"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/collector/geometry"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

// Options configures the collector. Titles default to off, matching the
// data inventory (docs/privacy-and-security.md §3.1).
type Options struct {
	// Titles enables reading window titles for in-memory classification.
	// Titles are never logged or persisted.
	Titles bool
	// Now supplies Snapshot.At. Defaults to time.Now.
	Now func() time.Time
}

// allDesktops is the _NET_WM_DESKTOP value of a window shown on every
// workspace (a "sticky" window).
const allDesktops = 0xFFFFFFFF

// noDesktop marks a missing _NET_WM_DESKTOP or _NET_CURRENT_DESKTOP.
const noDesktop = -1

// Window types (_NET_WM_WINDOW_TYPE) that are part of the desktop shell,
// not something the user attends to: the wallpaper and panels.
const (
	typeDesktop = "_NET_WM_WINDOW_TYPE_DESKTOP"
	typeDock    = "_NET_WM_WINDOW_TYPE_DOCK"
)

// rawWindow is one client from _NET_CLIENT_LIST_STACKING as read from X.
type rawWindow struct {
	id       uint32
	pid      int
	appID    string
	bounds   model.Rect // root coordinates, including the WM frame
	viewable bool       // map state is Viewable
	hidden   bool       // _NET_WM_STATE_HIDDEN (minimised or shaded)
	types    []string   // _NET_WM_WINDOW_TYPE atom names
	desktop  int64      // _NET_WM_DESKTOP, or noDesktop
	title    model.Sensitive[string]
}

// rawState is everything read from X for one snapshot.
type rawState struct {
	at             time.Time
	stacking       []rawWindow // _NET_CLIENT_LIST_STACKING order: bottom first
	currentDesktop int64       // _NET_CURRENT_DESKTOP, or noDesktop
	monitors       []model.Rect
	active         uint32 // _NET_ACTIVE_WINDOW, 0 if none
	pointerX       int
	pointerY       int
	havePointer    bool
	pointerMoved   bool
	idleFor        time.Duration
	locked         bool
}

// buildSnapshot turns raw X readings into a model.Snapshot. Windows that
// are entirely covered or off-screen are dropped unless they hold focus.
func buildSnapshot(s rawState) model.Snapshot {
	// Everything drawn on this workspace, topmost first. Docks and the
	// desktop window still cover what is beneath them and still catch the
	// pointer, so they take part in the geometry; they just aren't reported.
	var shown []rawWindow
	for _, w := range slices.Backward(s.stacking) {
		if w.viewable && !w.hidden && onDesktop(w.desktop, s.currentDesktop) {
			shown = append(shown, w)
		}
	}
	rects := make([]model.Rect, len(shown))
	for i, w := range shown {
		rects[i] = w.bounds
	}
	visible := geometry.VisibleFractions(rects, s.monitors)
	pointer := -1
	if s.havePointer {
		pointer = geometry.TopmostAt(rects, s.pointerX, s.pointerY)
	}

	snap := model.Snapshot{
		At:           s.at,
		Windows:      make([]model.Window, 0, len(shown)),
		Monitors:     s.monitors,
		PointerMoved: s.pointerMoved,
		IdleFor:      s.idleFor,
		Locked:       s.locked,
	}
	for i, w := range shown {
		if isShell(w.types) || w.bounds.Area() == 0 {
			continue
		}
		focused := w.id != 0 && w.id == s.active
		if visible[i] == 0 && !focused {
			continue
		}
		snap.Windows = append(snap.Windows, model.Window{
			ID:      model.WindowID(w.id),
			AppID:   w.appID,
			PID:     w.pid,
			Bounds:  w.bounds,
			Monitor: geometry.MonitorIndex(w.bounds, s.monitors),
			Visible: visible[i],
			Focused: focused,
			Title:   w.title,
		})
		if i == pointer {
			snap.PointerWindow = model.WindowID(w.id)
		}
	}
	return snap
}

// onDesktop reports whether a window on workspace win is shown while
// workspace current is active. Sticky windows are on every workspace, and
// when either value is missing (a WM without workspaces) the window counts
// as shown.
func onDesktop(win, current int64) bool {
	return win == noDesktop || current == noDesktop || win == allDesktops || win == current
}

// isShell reports whether a window's types mark it as the desktop or a
// panel.
func isShell(types []string) bool {
	return slices.Contains(types, typeDesktop) || slices.Contains(types, typeDock)
}

// frameBounds returns a client's outer bounds in root coordinates. x and y
// are the client's origin translated to the root window; extents is
// _NET_FRAME_EXTENTS (left, right, top, bottom), which reparenting WMs set
// to the decoration they draw around the client. A missing or short
// extents value means no frame.
func frameBounds(x, y, w, h int, extents []uint32) model.Rect {
	if len(extents) < 4 {
		return model.Rect{X: x, Y: y, W: w, H: h}
	}
	l, r, t, b := int(extents[0]), int(extents[1]), int(extents[2]), int(extents[3])
	return model.Rect{X: x - l, Y: y - t, W: w + l + r, H: h + t + b}
}

// cardinals decodes a 32-bit-format property value (CARDINAL, WINDOW or
// ATOM lists). X sends these in the connection's byte order, which xgb
// always negotiates as little-endian.
func cardinals(b []byte) []uint32 {
	out := make([]uint32, len(b)/4)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(b[4*i:])
	}
	return out
}

// appIDFromClass picks an app id from WM_CLASS, which holds two
// NUL-terminated strings: the instance name, then the class name. The
// class is the stable one ("Code", "firefox"); the instance is used when
// the class is empty. It returns "" when neither is set.
func appIDFromClass(b []byte) string {
	parts := bytes.Split(bytes.TrimRight(b, "\x00"), []byte{0})
	if len(parts) >= 2 && len(parts[1]) > 0 {
		return string(parts[1])
	}
	return string(parts[0])
}

// capabilities reports what this collector observes, for doctor.
func capabilities(opts Options, idle bool) ports.Capabilities {
	return ports.Capabilities{
		Name:    "x11",
		Windows: true,
		Pointer: true,
		Idle:    idle,
		Titles:  opts.Titles,
		Limitations: []string{
			"video and call detection not implemented (presence relies on idle time)",
			"Do Not Disturb and screen sharing aren't detected, so nudges can show during a call, a share or a presentation",
			"input counts not implemented on Linux (idle-time deltas stand in)",
		},
	}
}
