package main

import (
	"strings"
	"testing"

	"github.com/kshitijdhara/burntout/internal/adapters/control"
	"github.com/kshitijdhara/burntout/internal/config"
)

// TestTitlesLineAfterTurningOff: the running daemon decided at its start
// whether it reads titles, so turning the opt-in off says so until it
// restarts, instead of "not collected".
func TestTitlesLineAfterTurningOff(t *testing.T) {
	dir := t.TempDir()
	off := config.Default()
	if err := control.SetDaemonTitles(dir, true); err != nil {
		t.Fatal(err)
	}
	if got := titlesLine(off, daemonReadsTitles(true, dir)); !strings.HasPrefix(got, "off from the next daemon start") {
		t.Errorf("a daemon that reads titles: %q", got)
	}
	if got := titlesLine(off, daemonReadsTitles(false, dir)); !strings.HasPrefix(got, "off (not collected)") {
		t.Errorf("no daemon running: %q", got)
	}
	if err := control.SetDaemonTitles(dir, false); err != nil {
		t.Fatal(err)
	}
	if got := titlesLine(off, daemonReadsTitles(true, dir)); !strings.HasPrefix(got, "off (not collected)") {
		t.Errorf("a daemon that doesn't read titles: %q", got)
	}
	on := config.Default()
	on.Privacy.TitleClassification = true
	if got := titlesLine(on, true); !strings.HasPrefix(got, "on:") {
		t.Errorf("on: %q", got)
	}
}
