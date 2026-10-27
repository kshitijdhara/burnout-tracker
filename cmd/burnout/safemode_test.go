package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/control"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
)

func TestSafeModeDropsTheCollectorAndShowsInTheTray(t *testing.T) {
	safeMode.Store(true)
	t.Cleanup(func() { safeMode.Store(false) })
	if c := screenCollector(slog.New(slog.NewTextHandler(io.Discard, nil)), true); c != nil {
		_ = c.Close()
		t.Fatal("a desktop collector in safe mode")
	}
	dir := t.TempDir()
	now := time.Date(2026, 10, 27, 15, 0, 0, 0, time.Local)
	if s := trayStatus(dir, config.Default(), now); s.Paused || !strings.HasPrefix(s.Text, "Safe mode") {
		t.Errorf("safe mode: %+v", s)
	}
	// A pause still says so first: it is what the user did last.
	if err := control.SetPause(dir, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s := trayStatus(dir, config.Default(), now); !s.Paused {
		t.Errorf("paused in safe mode: %+v", s)
	}
}
