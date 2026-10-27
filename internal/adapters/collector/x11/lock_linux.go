//go:build linux

package x11

import (
	"context"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	// lockTimeout bounds each D-Bus call so a stuck bus can't stall a tick.
	lockTimeout = 500 * time.Millisecond
	// lockRetry spaces out reconnects to a bus that wasn't reachable.
	lockRetry = 30 * time.Second

	logind      = "org.freedesktop.login1"
	logindIface = "org.freedesktop.login1.Session"
	// The caller's session, or the user's graphical session when the caller
	// (a systemd user service) belongs to none.
	logindAuto  = dbus.ObjectPath("/org/freedesktop/login1/session/auto")
	gnomeSaver  = "org.gnome.ScreenSaver"
	gnomeObject = dbus.ObjectPath("/org/gnome/ScreenSaver")
)

// lockReader reads the screen lock state, first from logind on the system
// bus, then from GNOME's screensaver on the session bus. Each connection is
// kept across snapshots. When neither answers, the screen counts as
// unlocked: idle time still ends presence.
type lockReader struct {
	system, session       *dbus.Conn
	systemTry, sessionTry time.Time
}

func (l *lockReader) locked(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	if hint, active, ok := l.logind(ctx); ok {
		return isLocked(hint, active)
	}
	active, _ := l.gnome(ctx)
	return active
}

func (l *lockReader) logind(ctx context.Context) (hint, active bool, ok bool) {
	conn := reconnect(&l.system, &l.systemTry, dbus.ConnectSystemBus)
	if conn == nil {
		return false, false, false
	}
	obj := conn.Object(logind, logindAuto)
	if err := property(ctx, obj, logindIface, "LockedHint", &hint); err != nil {
		dropIfClosed(&l.system)
		return false, false, false
	}
	if err := property(ctx, obj, logindIface, "Active", &active); err != nil {
		dropIfClosed(&l.system)
		return false, false, false
	}
	return hint, active, true
}

func (l *lockReader) gnome(ctx context.Context) (active, ok bool) {
	conn := reconnect(&l.session, &l.sessionTry, dbus.ConnectSessionBus)
	if conn == nil {
		return false, false
	}
	if err := conn.Object(gnomeSaver, gnomeObject).CallWithContext(ctx, gnomeSaver+".GetActive", 0).Store(&active); err != nil {
		dropIfClosed(&l.session)
		return false, false
	}
	return active, true
}

func (l *lockReader) close() {
	for _, c := range []**dbus.Conn{&l.system, &l.session} {
		if *c != nil {
			_ = (*c).Close()
			*c = nil
		}
	}
}

// reconnect returns *conn, connecting first if needed, but tries at most
// once every lockRetry.
func reconnect(conn **dbus.Conn, last *time.Time, dial func(...dbus.ConnOption) (*dbus.Conn, error)) *dbus.Conn {
	if *conn != nil {
		return *conn
	}
	if !last.IsZero() && time.Since(*last) < lockRetry {
		return nil
	}
	*last = time.Now()
	c, err := dial()
	if err != nil {
		return nil
	}
	*conn = c
	return c
}

// dropIfClosed forgets a connection the bus closed, so it is redialled.
func dropIfClosed(conn **dbus.Conn) {
	if *conn != nil && !(*conn).Connected() {
		_ = (*conn).Close()
		*conn = nil
	}
}

func property(ctx context.Context, obj dbus.BusObject, iface, name string, out *bool) error {
	return obj.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, iface, name).Store(out)
}
