//go:build linux

package portal

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	dest   = "org.freedesktop.portal.Desktop"
	object = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	method = "org.freedesktop.portal.OpenURI.OpenFile"
	// callTimeout bounds the call: the portal answers at once and opens the
	// file in the background.
	callTimeout = 3 * time.Second
)

// OpenFile asks the desktop portal to open path with its default app. It
// returns ErrUnavailable when the session bus, the portal or descriptor
// passing is missing.
func OpenFile(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = conn.Close() }()
	if !conn.SupportsUnixFDs() {
		return fmt.Errorf("%w: the session bus can't pass files", ErrUnavailable)
	}
	f, err := os.Open(path) //nolint:gosec // our own launch file, from the data folder
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // the portal holds its own copy once it answers
	fd := f.Fd()
	if fd > math.MaxInt32 { // descriptors are small; D-Bus carries them as int32
		return fmt.Errorf("%w: descriptor %d out of range", ErrUnavailable, fd)
	}
	call := conn.Object(dest, object).CallWithContext(ctx, method, 0,
		"", dbus.UnixFD(int32(fd)), map[string]dbus.Variant{})
	if call.Err != nil {
		var e dbus.Error
		if errors.As(call.Err, &e) && (e.Name == "org.freedesktop.DBus.Error.ServiceUnknown" ||
			e.Name == "org.freedesktop.DBus.Error.UnknownMethod" || e.Name == "org.freedesktop.DBus.Error.UnknownObject") {
			return fmt.Errorf("%w: %s", ErrUnavailable, e.Name)
		}
		return fmt.Errorf("portal: open %s: %w", path, call.Err)
	}
	return nil
}
