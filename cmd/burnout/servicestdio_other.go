//go:build !windows

package main

// serviceStdio needs nothing here: launchd sends the daemon's crash output
// to the crash log and systemd to the journal.
func serviceStdio(string) {}
