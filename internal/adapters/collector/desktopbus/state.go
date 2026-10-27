// Package desktopbus reads Linux desktop session state over D-Bus for the
// desktop collectors: screen lock from logind (else GNOME's screensaver)
// and, on GNOME, idle time from Mutter's idle monitor. The X11 and GNOME
// Wayland collectors share it.
//
// This file holds the pure logic so it is tested on every platform; the
// D-Bus calls live in bus_linux.go.
package desktopbus

import (
	"math"
	"time"
)

// IsLocked combines logind's view of the user's graphical session: the
// screen counts as locked while the session's LockedHint is set or while
// another session is in the foreground (a switched user or VT).
func IsLocked(lockedHint, active bool) bool {
	return lockedHint || !active
}

// IdleDuration converts Mutter's idle time in milliseconds, saturating
// rather than overflowing on a nonsense value.
func IdleDuration(ms uint64) time.Duration {
	if ms > math.MaxInt64/uint64(time.Millisecond) {
		return math.MaxInt64
	}
	return time.Duration(ms) * time.Millisecond //nolint:gosec // G115: bounded above
}
