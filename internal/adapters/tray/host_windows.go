package tray

import (
	"context"

	"github.com/kshitijdhara/burnout-tracker/assets"
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
