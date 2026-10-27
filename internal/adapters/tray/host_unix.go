//go:build !darwin && !windows

package tray

import (
	"context"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/kshitijdhara/burnout-tracker/assets"
)

const (
	watcher   = "org.kde.StatusNotifierWatcher"
	hostCheck = 3 * time.Second
)

// noHost says why no panel can show a StatusNotifier icon, or "" if one
// can. Without a host the icon would be registered but never seen.
func noHost(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, hostCheck)
	defer cancel()
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return "no desktop session bus"
	}
	defer func() { _ = conn.Close() }()
	var has bool
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, watcher).Store(&has); err != nil || !has {
		return "the desktop has no tray (on GNOME, the AppIndicator extension adds one)"
	}
	v, err := conn.Object(watcher, "/StatusNotifierWatcher").GetProperty(watcher + ".IsStatusNotifierHostRegistered")
	if registered, ok := v.Value().(bool); err != nil || !ok || !registered {
		return "the desktop's tray isn't running"
	}
	return ""
}

// platformIcons are PNGs, at the size panels scale down from.
func platformIcons() icons {
	return icons{recording: assets.TrayColor64, paused: assets.TrayPausedColor64}
}

// watchRegistered subscribes, before systray starts, to the tray host's
// announcement of our icon, and returns a wait for it. systray (v1.11.0)
// calls its ready callback before it finishes setting up its bus
// connection, so menu updates made at once race with that setup. The host
// announces the icon only after the setup is done; the menu's first call
// (SetTooltip) then takes systray's lock after the setup released it, which
// orders every later update after the setup. wait reports whether the
// announcement came; without one it gives up after the timeout, as before.
func watchRegistered() (wait func(ctx context.Context, timeout time.Duration) bool) {
	none := func(context.Context, time.Duration) bool { return false }
	// systray uses the process's shared session-bus connection, so ours has
	// the same unique name as its icon. It is systray's: never close it.
	conn, err := dbus.SessionBus()
	if err != nil || len(conn.Names()) == 0 {
		return none
	}
	match := []dbus.MatchOption{dbus.WithMatchInterface(watcher), dbus.WithMatchMember("StatusNotifierItemRegistered")}
	if conn.AddMatchSignal(match...) != nil {
		return none
	}
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	self := conn.Names()[0]
	return func(ctx context.Context, timeout time.Duration) bool {
		defer func() {
			conn.RemoveSignal(signals)
			_ = conn.RemoveMatchSignal(match...)
		}()
		deadline := time.After(timeout)
		for {
			select {
			case s, ok := <-signals:
				if !ok || s == nil {
					return false // the connection closed
				}
				if s.Name != watcher+".StatusNotifierItemRegistered" || len(s.Body) == 0 {
					continue
				}
				// Hosts name the icon by its bus name and path, e.g. ":1.45/StatusNotifierItem".
				if id, _ := s.Body[0].(string); strings.HasPrefix(id, self) {
					return true
				}
			case <-deadline:
				return false
			case <-ctx.Done():
				return false
			}
		}
	}
}
