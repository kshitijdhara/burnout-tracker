//go:build linux

package main

import (
	"errors"
	"os"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/collector/x11"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

// newCollector picks the X11 collector on an X session. A Wayland session
// may also set DISPLAY for Xwayland, but Xwayland only sees X clients, so
// Wayland waits for its own bridge.
func newCollector(o collectorOptions) (ports.Collector, error) {
	if err := x11Session(os.Getenv("DISPLAY"), os.Getenv("XDG_SESSION_TYPE")); err != nil {
		return nil, err
	}
	return x11.New(x11.Options{Titles: o.Titles, Now: o.Now})
}

// x11Session says why the desktop can't be read through X, or nil if it can.
func x11Session(display, sessionType string) error {
	switch {
	case sessionType == "wayland":
		return errors.New("no desktop collector for Wayland yet (a GNOME bridge is coming); log in with an X11 session for desktop collection")
	case display == "":
		return errors.New("no desktop collector: DISPLAY is not set (desktop collection needs an X11 session)")
	}
	return nil
}
