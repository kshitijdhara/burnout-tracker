//go:build !darwin && !windows

package tray

import (
	"context"
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
