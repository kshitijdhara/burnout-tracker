package wayland

import (
	"slices"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/collector/geometry"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

// Options configures the collector. Titles default to off, matching the
// data inventory (docs/privacy-and-security.md §3.1); the hello tells the
// extension, which then sends no titles at all.
type Options struct {
	// Titles enables window titles for in-memory classification. Titles
	// are never logged or persisted.
	Titles bool
	// Now supplies Snapshot.At. Defaults to time.Now.
	Now func() time.Time
	// Dir holds the socket and token. Defaults to paths.RuntimeDir().
	Dir string
}

// extra is what the collector adds to a bridge reply: its own readings and
// what it remembers between snapshots.
type extra struct {
	at           time.Time
	titles       bool
	pointerMoved bool
	idleFor      time.Duration
	locked       bool
}

// buildSnapshot turns a snapshot reply into a model.Snapshot. Windows that
// are entirely covered or off-screen are dropped unless they hold focus.
func buildSnapshot(r reply, e extra) model.Snapshot {
	monitors := make([]model.Rect, len(r.Monitors))
	for i, m := range r.Monitors {
		monitors[i] = modelRect(m)
	}
	// Everything drawn on this workspace, topmost first. Docks and the
	// desktop still cover what is beneath them and catch the pointer, so
	// they take part in the geometry; they just aren't reported.
	var shown []window
	for _, w := range slices.Backward(r.Windows) {
		if !w.Minimised && w.OnWorkspace && w.Type != typeOther {
			shown = append(shown, w)
		}
	}
	rects := make([]model.Rect, len(shown))
	for i, w := range shown {
		rects[i] = modelRect(w.Frame)
	}
	visible := geometry.VisibleFractions(rects, monitors)
	pointer := geometry.TopmostAt(rects, r.Pointer.X, r.Pointer.Y)

	snap := model.Snapshot{
		At:           e.at,
		Windows:      make([]model.Window, 0, len(shown)),
		Monitors:     monitors,
		PointerMoved: e.pointerMoved,
		IdleFor:      e.idleFor,
		Locked:       e.locked,
	}
	for i, w := range shown {
		if w.Type == typeDesktop || w.Type == typeDock || rects[i].Area() == 0 {
			continue
		}
		focused := w.ID != 0 && w.ID == r.Focus
		if visible[i] == 0 && !focused {
			continue
		}
		mw := model.Window{
			ID:      model.WindowID(w.ID),
			AppID:   appID(w),
			PID:     w.PID,
			Bounds:  rects[i],
			Monitor: geometry.MonitorIndex(rects[i], monitors),
			Visible: visible[i],
			Focused: focused,
		}
		// A title the hello didn't ask for is dropped, whatever the
		// extension sent.
		if e.titles && w.Title != nil {
			mw.Title = model.NewSensitive(*w.Title)
		}
		snap.Windows = append(snap.Windows, mw)
		if i == pointer {
			snap.PointerWindow = mw.ID
		}
	}
	return snap
}

// lockedSnapshot is reported while the screen is locked. GNOME disables
// extensions on the lock screen, so the bridge is not asked.
func lockedSnapshot(e extra) model.Snapshot {
	return model.Snapshot{At: e.at, IdleFor: e.idleFor, Locked: true}
}

// appID picks the id the classifier matches. On Wayland, Mutter's WM class
// is the client's app_id ("firefox", "org.gnome.Ptyxis"), or the X class
// for Xwayland clients, which is what the X11 rules already know. GNOME's
// own app id ("org.gnome.Ptyxis.desktop") is the fallback.
func appID(w window) string {
	if w.WMClass != "" {
		return w.WMClass
	}
	return strings.TrimSuffix(w.App, ".desktop")
}

func modelRect(r rect) model.Rect { return model.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H} }

// capabilities reports what this collector observes, for doctor.
func capabilities(opts Options, idle bool) ports.Capabilities {
	return ports.Capabilities{
		Name:    "gnome-wayland",
		Windows: true,
		Pointer: true,
		Idle:    idle,
		Titles:  opts.Titles,
		Limitations: []string{
			"windows are read by the burnout GNOME Shell extension, which GNOME pauses while the screen is locked",
			"video and call detection not implemented (presence relies on idle time)",
			"input counts not implemented on Linux (idle-time deltas stand in)",
		},
	}
}
