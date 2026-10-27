//go:build !linux

package sdnotify

// Send does nothing: only systemd, on Linux, listens.
func Send(string) error { return nil }
