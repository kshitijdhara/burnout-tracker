//go:build linux

package wayland

import (
	"context"
	"time"

	"github.com/godbus/dbus/v5"
)

// ExtensionInfo asks GNOME Shell's own extension service what it knows
// about the bridge extension, for doctor. ok is false when GNOME Shell
// doesn't answer (not GNOME, or no session bus).
func ExtensionInfo(ctx context.Context) (info Extension, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return Extension{}, false
	}
	defer func() { _ = conn.Close() }()
	var raw map[string]dbus.Variant
	err = conn.Object("org.gnome.Shell.Extensions", "/org/gnome/Shell/Extensions").
		CallWithContext(ctx, "org.gnome.Shell.Extensions.GetExtensionInfo", 0, UUID).Store(&raw)
	if err != nil {
		return Extension{}, false
	}
	return extensionFromInfo(raw), true
}

// stateUninstalled is reported for an extension removed this session.
const stateUninstalled = 99

func extensionFromInfo(raw map[string]dbus.Variant) Extension {
	var e Extension
	if v, ok := raw["state"].Value().(float64); ok {
		e.State = int(v)
		e.Installed = e.State != stateUninstalled
	}
	if v, ok := raw["error"].Value().(string); ok {
		e.Error = v
	}
	return e
}
