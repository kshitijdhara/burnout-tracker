//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/collector/wayland"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/collector/x11"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

// desktopSession is what the environment says about the graphical session.
type desktopSession struct {
	display, waylandDisplay, sessionType, desktop string
}

func currentSession() desktopSession {
	return desktopSession{
		display:        os.Getenv("DISPLAY"),
		waylandDisplay: os.Getenv("WAYLAND_DISPLAY"),
		sessionType:    os.Getenv("XDG_SESSION_TYPE"),
		desktop:        os.Getenv("XDG_CURRENT_DESKTOP"),
	}
}

// wayland reports a Wayland session. A systemd user service may not have
// XDG_SESSION_TYPE imported, so WAYLAND_DISPLAY counts too. Xwayland sets
// DISPLAY as well, but it only sees X clients.
func (s desktopSession) wayland() bool {
	return s.sessionType == "wayland" || s.waylandDisplay != ""
}

// gnome reports GNOME, or a desktop built on it ("ubuntu:GNOME").
func (s desktopSession) gnome() bool {
	for d := range strings.SplitSeq(s.desktop, ":") {
		if strings.EqualFold(d, "GNOME") {
			return true
		}
	}
	return false
}

// newCollector picks the X11 collector on an X session and the GNOME
// Shell bridge on GNOME Wayland.
func newCollector(o collectorOptions) (ports.Collector, error) {
	s := currentSession()
	if s.wayland() && s.gnome() {
		// The first dial is bounded by the hello timeout; the daemon's
		// redials run under its own context, from Snapshot.
		open := wayland.New
		if o.Wait {
			open = wayland.NewWaiting
		}
		c, err := open(context.Background(), wayland.Options{Titles: o.Titles, Now: o.Now})
		if err != nil {
			return nil, gnomeBridgeError(err)
		}
		return c, nil
	}
	if err := x11Session(s); err != nil {
		return nil, err
	}
	return x11.New(x11.Options{Titles: o.Titles, Now: o.Now})
}

// x11Session says why the desktop can't be read through X, or nil if it can.
func x11Session(s desktopSession) error {
	switch {
	case s.wayland():
		return errors.New("no desktop collector for this Wayland desktop (only GNOME is supported); log in with an X11 session for desktop collection")
	case s.display == "":
		return errors.New("no desktop collector: DISPLAY is not set (desktop collection needs an X11 session)")
	}
	return nil
}

// gnomeBridgeError explains a bridge that didn't answer, with the fix.
func gnomeBridgeError(err error) error {
	if errors.Is(err, wayland.ErrNotRunning) {
		return fmt.Errorf("no desktop collector: on GNOME Wayland, burnout needs its GNOME Shell extension. "+
			"Install the burnout .deb or .rpm, log out and back in, then run `%s`", wayland.EnableCommand)
	}
	return fmt.Errorf("no desktop collector: the GNOME Shell bridge failed: %w", err)
}
