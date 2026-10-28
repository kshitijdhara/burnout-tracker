package wayland

import "strings"

// Extension is what GNOME Shell reports about the bridge extension.
type Extension struct {
	Installed bool
	State     int // GNOME Shell's ExtensionState
	Error     string
}

// GNOME Shell's ExtensionState values (js/misc/extensionUtils.js). 45
// calls 1 and 2 ENABLED and DISABLED, 46 and later ACTIVE and INACTIVE.
const (
	stateActive    = 1
	stateError     = 3
	stateOutOfDate = 4
)

// EnableCommand turns the extension on. Packages never run it (privacy &
// security §5.2, AC-2); the user does.
const EnableCommand = "gnome-extensions enable " + UUID

// Describe turns the extension's state and whether the bridge answered
// into a doctor line.
func (e Extension) Describe(answering bool) (ok bool, detail string) {
	switch {
	case answering:
		return true, "enabled and answering"
	case !e.Installed:
		return false, "the extension is not installed; it ships in the BurntOut .deb and .rpm (log out and back in after installing, then run `" + EnableCommand + "`)"
	case e.State == stateError:
		return false, "the extension failed to load: " + strings.TrimSpace(e.Error)
	case e.State == stateOutOfDate:
		return false, "the extension does not support this GNOME Shell version; update burntout"
	case e.State == stateActive:
		return false, "the extension is enabled but not answering; check `journalctl --user -b -g burntout-bridge`"
	default:
		return false, "the extension is installed but not enabled; run `" + EnableCommand + "`"
	}
}
