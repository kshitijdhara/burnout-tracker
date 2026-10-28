package tray

import (
	"context"
	"time"

	"github.com/kshitijdhara/burntout/assets"
)

// noHost: every Windows desktop has a notification area. Whether the icon
// made it there shows when the tray becomes ready.
func noHost(context.Context) string { return "" }

// platformIcons are .ico files: Windows loads tray icons from them.
func platformIcons() icons {
	return icons{
		recording: ico(assets.TrayColor16, assets.TrayColor32),
		paused:    ico(assets.TrayPausedColor16, assets.TrayPausedColor32),
	}
}

// watchRegistered: the Windows icon is ready when systray says so.
func watchRegistered() func(context.Context, time.Duration) bool {
	return func(context.Context, time.Duration) bool { return true }
}
