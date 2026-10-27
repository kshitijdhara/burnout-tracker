package main

import (
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/control"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/strain"
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

// TestTrayNudge: the day's nudge shows only while the collector has just
// seen no call or video, and never in safe mode or while paused; one kind
// a day, recorded in the data folder, and the menu's snooze ends the day.
func TestTrayNudge(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 26, 15, 0, 0, 0, time.Local) // a Monday
	c := config.Default()
	t.Cleanup(func() {
		lastPresence.Store(nil)
		latestGuardrails.Store(nil)
		safeMode.Store(false)
	})
	publishGuardrails([]strain.Score{
		{Date: "2026-10-25", Status: strain.StatusOK, Guardrails: []strain.Guardrail{{Name: "weekly_hours", Text: "50 h"}}},
		{Date: "2026-10-26", Status: strain.StatusCalibrating},
	})
	seen := func(p presence) { p.at = now.Add(-time.Second); lastPresence.Store(&p) }

	if s := trayStatus(dir, c, now); s.Nudge.Kind != "" {
		t.Errorf("before the collector reported: %+v", s.Nudge)
	}
	seen(presence{active: true, call: true})
	if s := trayStatus(dir, c, now); s.Nudge.Kind != "" {
		t.Errorf("in a call: %+v", s.Nudge)
	}
	seen(presence{active: true, video: true})
	if s := trayStatus(dir, c, now); s.Nudge.Kind != "" {
		t.Errorf("with video: %+v", s.Nudge)
	}
	seen(presence{active: true})
	if s := trayStatus(dir, c, now.Add(time.Minute)); s.Nudge.Kind != "" {
		t.Errorf("with a stale snapshot: %+v", s.Nudge)
	}
	s := trayStatus(dir, c, now)
	if s.Nudge.Kind != "weekly_hours" || s.Nudge.Text == "" || s.Text != "Recording" {
		t.Fatalf("nudge: %+v", s)
	}
	if n, err := control.ReadNudges(dir); err != nil || n.Day != "2026-10-26" || n.Kind != "weekly_hours" {
		t.Errorf("not recorded: %+v %v", n, err)
	}

	safeMode.Store(true)
	if s := trayStatus(dir, c, now); s.Nudge.Kind != "" || s.Text != "Safe mode: recording hooks only, after repeated crashes" {
		t.Errorf("safe mode: %+v", s)
	}
	safeMode.Store(false)
	if err := control.SetPause(dir, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s := trayStatus(dir, c, now); s.Nudge.Kind != "" {
		t.Errorf("paused: %+v", s.Nudge)
	}
	if err := control.Resume(dir); err != nil {
		t.Fatal(err)
	}
	c.Nudges.Enabled = false
	if s := trayStatus(dir, c, now); s.Nudge.Kind != "" {
		t.Errorf("nudges off: %+v", s.Nudge)
	}
	c.Nudges.Enabled = true

	if err := snoozeNudge(dir, "weekly_hours", now); err != nil {
		t.Fatal(err)
	}
	if s := trayStatus(dir, c, now); s.Nudge.Kind != "" {
		t.Errorf("snoozed: %+v", s.Nudge)
	}
	tomorrow := now.AddDate(0, 0, 1)
	seen(presence{active: true})
	lastPresence.Load().at = tomorrow
	if s := trayStatus(dir, c, tomorrow); s.Nudge.Kind != "weekly_hours" {
		t.Errorf("the next day: %+v", s.Nudge)
	}
	if err := turnOffNudge(dir, "weekly_hours", tomorrow); err != nil {
		t.Fatal(err)
	}
	later := tomorrow.AddDate(0, 0, 1)
	publishGuardrails([]strain.Score{{Date: "2026-10-27", Status: strain.StatusOK, Guardrails: []strain.Guardrail{{Name: "weekly_hours"}}}})
	lastPresence.Load().at = later
	if s := trayStatus(dir, c, later); s.Nudge.Kind != "" {
		t.Errorf("turned off: %+v", s.Nudge)
	}
	c.Nudges.WeeklyHours = true // the file's switch can't turn the tray's off back on
	if s := trayStatus(dir, c, later); s.Nudge.Kind != "" {
		t.Errorf("turned off, config on: %+v", s.Nudge)
	}
}
