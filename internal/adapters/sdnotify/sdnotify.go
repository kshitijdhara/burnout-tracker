// Package sdnotify tells systemd how the daemon is doing (sd_notify(3)):
// READY=1 once it is collecting, and WATCHDOG=1 while its loop is alive,
// so a hung daemon is restarted (system design §11.2 "Hang"). It is a
// no-op when NOTIFY_SOCKET is unset, and on platforms without systemd.
package sdnotify

// EnvSocket is the variable systemd sets to its notification socket.
const EnvSocket = "NOTIFY_SOCKET"

// Ready says the service has started.
func Ready() error { return Send("READY=1") }

// Watchdog says the service is alive.
func Watchdog() error { return Send("WATCHDOG=1") }
