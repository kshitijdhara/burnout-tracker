//go:build linux

package desktopbus

import (
	"context"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	// callTimeout bounds each D-Bus call so a stuck bus can't stall a tick.
	callTimeout = 500 * time.Millisecond
	// retry spaces out reconnects to a bus that wasn't reachable.
	retry = 30 * time.Second

	logind      = "org.freedesktop.login1"
	logindIface = "org.freedesktop.login1.Session"
	// The caller's session, or the user's graphical session when the caller
	// (a systemd user service) belongs to none.
	logindAuto  = dbus.ObjectPath("/org/freedesktop/login1/session/auto")
	gnomeSaver  = "org.gnome.ScreenSaver"
	gnomeObject = dbus.ObjectPath("/org/gnome/ScreenSaver")

	mutterIdle       = "org.gnome.Mutter.IdleMonitor"
	mutterIdleObject = dbus.ObjectPath("/org/gnome/Mutter/IdleMonitor/Core")
)

// Reader reads lock and idle state from the system and session buses. Each
// connection is kept across snapshots. The zero value is ready to use; it
// is not safe for concurrent use (collectors serialise their snapshots).
type Reader struct {
	system, session       *dbus.Conn
	systemTry, sessionTry time.Time
}

// Locked reads the screen lock state, first from logind on the system bus,
// then from GNOME's screensaver on the session bus. When neither answers,
// the screen counts as unlocked: idle time still ends presence.
func (r *Reader) Locked(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if hint, active, ok := r.logind(ctx); ok {
		return IsLocked(hint, active)
	}
	active, _ := r.gnome(ctx)
	return active
}

// MutterIdle returns the time since the last user input from Mutter's
// core idle monitor, and false when Mutter doesn't answer (not GNOME, or
// the session bus is unreachable).
func (r *Reader) MutterIdle(ctx context.Context) (time.Duration, bool) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	conn := reconnect(&r.session, &r.sessionTry, dbus.ConnectSessionBus)
	if conn == nil {
		return 0, false
	}
	var ms uint64
	if err := conn.Object(mutterIdle, mutterIdleObject).CallWithContext(ctx, mutterIdle+".GetIdletime", 0).Store(&ms); err != nil {
		dropIfClosed(&r.session)
		return 0, false
	}
	return IdleDuration(ms), true
}

func (r *Reader) logind(ctx context.Context) (hint, active bool, ok bool) {
	conn := reconnect(&r.system, &r.systemTry, dbus.ConnectSystemBus)
	if conn == nil {
		return false, false, false
	}
	obj := conn.Object(logind, logindAuto)
	if err := property(ctx, obj, logindIface, "LockedHint", &hint); err != nil {
		dropIfClosed(&r.system)
		return false, false, false
	}
	if err := property(ctx, obj, logindIface, "Active", &active); err != nil {
		dropIfClosed(&r.system)
		return false, false, false
	}
	return hint, active, true
}

func (r *Reader) gnome(ctx context.Context) (active, ok bool) {
	conn := reconnect(&r.session, &r.sessionTry, dbus.ConnectSessionBus)
	if conn == nil {
		return false, false
	}
	if err := conn.Object(gnomeSaver, gnomeObject).CallWithContext(ctx, gnomeSaver+".GetActive", 0).Store(&active); err != nil {
		dropIfClosed(&r.session)
		return false, false
	}
	return active, true
}

// Close disconnects from both buses.
func (r *Reader) Close() error {
	for _, c := range []**dbus.Conn{&r.system, &r.session} {
		if *c != nil {
			_ = (*c).Close()
			*c = nil
		}
	}
	return nil
}

// reconnect returns *conn, connecting first if needed, but tries at most
// once every retry.
func reconnect(conn **dbus.Conn, last *time.Time, dial func(...dbus.ConnOption) (*dbus.Conn, error)) *dbus.Conn {
	if *conn != nil {
		return *conn
	}
	if !last.IsZero() && time.Since(*last) < retry {
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
