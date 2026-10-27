package main

import (
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/control"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
)

func TestTrayStatus(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 26, 15, 0, 0, 0, time.Local)
	c := config.Default()
	if s := trayStatus(dir, c, now); s.Paused || s.Text != "Recording" {
		t.Errorf("recording: %+v", s)
	}
	if err := control.SetPause(dir, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s := trayStatus(dir, c, now); !s.Paused || s.Text != "Paused until 16:00" {
		t.Errorf("paused: %+v", s)
	}
	if s := trayStatus(dir, c, now.Add(2*time.Hour)); s.Paused {
		t.Errorf("an expired pause: %+v", s)
	}
	if err := control.Resume(dir); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("privacy.private_hours", []string{"14:00-16:00"}); err != nil {
		t.Fatal(err)
	}
	if s := trayStatus(dir, c, now); !s.Paused || s.Text != "Private hours: nothing is recorded" {
		t.Errorf("private hours: %+v", s)
	}
}
