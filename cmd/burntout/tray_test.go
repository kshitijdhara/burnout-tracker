package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/control"
	"github.com/kshitijdhara/burntout/internal/adapters/paths"
	"github.com/kshitijdhara/burntout/internal/adapters/tray"
	"github.com/kshitijdhara/burntout/internal/config"
	"github.com/kshitijdhara/burntout/internal/domain/boundary"
	"github.com/kshitijdhara/burntout/internal/domain/nudge"
	"github.com/kshitijdhara/burntout/internal/domain/strain"
)

func TestTrayStatus(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 26, 15, 0, 0, 0, time.Local)
	c := config.Default()
	if s := statusNow(dir, c, now); s.Paused || s.Text != "Recording" {
		t.Errorf("recording: %+v", s)
	}
	if err := control.SetPause(dir, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s := statusNow(dir, c, now); !s.Paused || s.Text != "Paused until 16:00" {
		t.Errorf("paused: %+v", s)
	}
	if s := statusNow(dir, c, now.Add(2*time.Hour)); s.Paused {
		t.Errorf("an expired pause: %+v", s)
	}
	if err := control.Resume(dir); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("privacy.private_hours", []string{"14:00-16:00"}); err != nil {
		t.Fatal(err)
	}
	if s := statusNow(dir, c, now); !s.Paused || s.Text != "Private hours: nothing is recorded" {
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

	if s := statusNow(dir, c, now); s.Nudge.Kind != "" {
		t.Errorf("before the collector reported: %+v", s.Nudge)
	}
	seen(presence{active: true, call: true})
	if s := statusNow(dir, c, now); s.Nudge.Kind != "" {
		t.Errorf("in a call: %+v", s.Nudge)
	}
	seen(presence{active: true, video: true})
	if s := statusNow(dir, c, now); s.Nudge.Kind != "" {
		t.Errorf("with video: %+v", s.Nudge)
	}
	seen(presence{active: true})
	if s := statusNow(dir, c, now.Add(time.Minute)); s.Nudge.Kind != "" {
		t.Errorf("with a stale snapshot: %+v", s.Nudge)
	}
	s := statusNow(dir, c, now)
	if s.Nudge.Kind != "weekly_hours" || s.Nudge.Text == "" || s.Text != "Recording" {
		t.Fatalf("nudge: %+v", s)
	}
	if n, err := control.ReadNudges(dir); err != nil || n.Day != "2026-10-26" || n.Kind != "weekly_hours" {
		t.Errorf("not recorded: %+v %v", n, err)
	}

	safeMode.Store(true)
	if s := statusNow(dir, c, now); s.Nudge.Kind != "" || s.Text != "Safe mode: recording hooks only, after repeated crashes" {
		t.Errorf("safe mode: %+v", s)
	}
	safeMode.Store(false)
	if err := control.SetPause(dir, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s := statusNow(dir, c, now); s.Nudge.Kind != "" {
		t.Errorf("paused: %+v", s.Nudge)
	}
	if err := control.Resume(dir); err != nil {
		t.Fatal(err)
	}
	c.Nudges.Enabled = false
	if s := statusNow(dir, c, now); s.Nudge.Kind != "" {
		t.Errorf("nudges off: %+v", s.Nudge)
	}
	c.Nudges.Enabled = true

	if err := snoozeNudge(dir, "weekly_hours", now); err != nil {
		t.Fatal(err)
	}
	if s := statusNow(dir, c, now); s.Nudge.Kind != "" {
		t.Errorf("snoozed: %+v", s.Nudge)
	}
	tomorrow := now.AddDate(0, 0, 1)
	seen(presence{active: true})
	lastPresence.Load().at = tomorrow
	if s := statusNow(dir, c, tomorrow); s.Nudge.Kind != "weekly_hours" {
		t.Errorf("the next day: %+v", s.Nudge)
	}

	// "Don't show this kind again" turns it off in the config file, where
	// `burntout config set` turns it back on.
	cfgPath := isolateConfig(t, dir)
	if err := turnOffNudge(dir, "weekly_hours", tomorrow); err != nil {
		t.Fatal(err)
	}
	if s := statusNow(dir, c, tomorrow); s.Nudge.Kind != "" {
		t.Errorf("turned off, before the daemon rereads the file: %+v", s.Nudge)
	}
	c = loadOrFatal(t, cfgPath)
	if c.Nudges.WeeklyHours {
		t.Fatal("turning a kind off didn't reach the config file")
	}
	later := tomorrow.AddDate(0, 0, 1)
	publishGuardrails([]strain.Score{{Date: "2026-10-27", Status: strain.StatusOK, Guardrails: []strain.Guardrail{{Name: "weekly_hours"}}}})
	lastPresence.Load().at = later
	if s := statusNow(dir, c, later); s.Nudge.Kind != "" {
		t.Errorf("turned off: %+v", s.Nudge)
	}
	if err := changeConfig(cfgPath, "set", "nudges.weekly_hours", []string{"true"}); err != nil {
		t.Fatal(err)
	}
	c = loadOrFatal(t, cfgPath)
	if s := statusNow(dir, c, later); s.Nudge.Kind != "weekly_hours" {
		t.Errorf("turned back on with `burntout config set`: %+v", s.Nudge)
	}
}

// TestNudgeTurnedOffByAnOlderTray: a kind v0.1.0-alpha.1's tray turned
// off in the data folder stays off until `burntout config set` turns it on.
func TestNudgeTurnedOffByAnOlderTray(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 26, 15, 0, 0, 0, time.Local)
	t.Cleanup(func() {
		lastPresence.Store(nil)
		latestGuardrails.Store(nil)
	})
	publishGuardrails([]strain.Score{{Date: "2026-10-25", Status: strain.StatusOK, Guardrails: []strain.Guardrail{{Name: "weekly_hours"}}}})
	lastPresence.Store(&presence{at: now.Add(-time.Second), active: true})
	if err := os.WriteFile(filepath.Join(dir, "nudges"), []byte(`{"off":["weekly_hours"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := isolateConfig(t, dir)
	if s := statusNow(dir, config.Default(), now); s.Nudge.Kind != "" {
		t.Errorf("turned off by the older tray: %+v", s.Nudge)
	}
	if err := changeConfig(cfgPath, "set", "nudges.weekly_hours", []string{"true"}); err != nil {
		t.Fatal(err)
	}
	if s := statusNow(dir, loadOrFatal(t, cfgPath), now); s.Nudge.Kind != "weekly_hours" {
		t.Errorf("turned back on: %+v", s.Nudge)
	}
}

// TestTrayStatusWritesNothing: the menu asks every 2 s, so only the
// minute's refresh records the day's nudge, and the status still hides it
// at once when a call starts.
func TestTrayStatusWritesNothing(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 26, 15, 0, 0, 0, time.Local)
	t.Cleanup(func() {
		lastPresence.Store(nil)
		latestGuardrails.Store(nil)
	})
	publishGuardrails([]strain.Score{{Date: "2026-10-25", Status: strain.StatusOK, Guardrails: []strain.Guardrail{{Name: "weekly_hours"}}}})
	lastPresence.Store(&presence{at: now.Add(-time.Second), active: true})
	c := config.Default()
	ts := &trayState{dataDir: dir, settings: func() config.Config { return c }}
	if s := ts.status(now); s.Nudge.Kind != "" || s.Text != "Recording" {
		t.Errorf("before a refresh: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(dir, "nudges")); !os.IsNotExist(err) {
		t.Fatalf("the status wrote the nudge state: %v", err)
	}
	ts.refresh(now)
	if s := ts.status(now); s.Nudge.Kind != "weekly_hours" {
		t.Errorf("after a refresh: %+v", s.Nudge)
	}
	lastPresence.Store(&presence{at: now.Add(-time.Second), active: true, call: true})
	if s := ts.status(now); s.Nudge.Kind != "" {
		t.Errorf("in a call since the refresh: %+v", s.Nudge)
	}
}

// TestSafeModeShowsWhilePaused: a pause or private hours say so first,
// and safe mode still shows after them, as the update notice does.
func TestSafeModeShowsWhilePaused(t *testing.T) {
	safeMode.Store(true)
	t.Cleanup(func() { safeMode.Store(false) })
	dir := t.TempDir()
	now := time.Date(2026, 10, 26, 15, 0, 0, 0, time.Local)
	c := config.Default()
	if err := control.SetPause(dir, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s := trayStatus(dir, c, now, trayExtras{update: "1.2.0"}); s.Text != "Paused until 16:00 · safe mode · update 1.2.0 available" {
		t.Errorf("paused: %q", s.Text)
	}
	if err := control.Resume(dir); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("privacy.private_hours", []string{"14:00-16:00"}); err != nil {
		t.Fatal(err)
	}
	if s := trayStatus(dir, c, now, trayExtras{}); s.Text != "Private hours: nothing is recorded · safe mode" {
		t.Errorf("private hours: %q", s.Text)
	}
	safeMode.Store(false)
	if s := trayStatus(dir, c, now, trayExtras{}); strings.Contains(s.Text, "safe mode") {
		t.Errorf("not in safe mode: %q", s.Text)
	}
}

// statusNow is the tray's status at now with its slower part just read.
func statusNow(dataDir string, c config.Config, now time.Time) tray.Status {
	return trayStatus(dataDir, c, now, readTrayExtras(dataDir, c, now))
}

// isolateConfig points the config file, data folder and daemon socket at
// test folders, and returns the config file's path.
func isolateConfig(t *testing.T, dataDir string) string {
	t.Helper()
	t.Setenv(paths.EnvConfigDir, t.TempDir())
	t.Setenv(paths.EnvDataDir, dataDir)
	t.Setenv(paths.EnvRuntimeDir, t.TempDir())
	p, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func loadOrFatal(t *testing.T, path string) config.Config {
	t.Helper()
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestNudgeBoundaries: only the rules that apply on the day count, so a
// shutdown time set tonight starts tomorrow.
func TestNudgeBoundaries(t *testing.T) {
	t.Cleanup(func() { latestBoundaries.Store(nil) })
	if b := nudgeBoundaries("2026-10-26"); b != (nudge.Boundaries{}) {
		t.Errorf("none read yet: %+v", b)
	}
	bs := []boundary.Boundary{
		{Kind: boundary.Shutdown, Value: "20:00", Since: "2026-10-01", Until: "2026-10-26"},
		{Kind: boundary.Shutdown, Value: "21:30", Since: "2026-10-26"},
		{Kind: boundary.NoWeekend, Since: "2026-10-27"},
		{Kind: boundary.MeetingsMax, Value: "4", Since: "2026-10-01"},
	}
	latestBoundaries.Store(&bs)
	if b := nudgeBoundaries("2026-10-25"); b.Shutdown != 20*time.Hour || !b.HasShutdown || b.WeekendsOff {
		t.Errorf("the old rule: %+v", b)
	}
	if b := nudgeBoundaries("2026-10-26"); b.Shutdown != 21*time.Hour+30*time.Minute || b.WeekendsOff {
		t.Errorf("the new rule: %+v", b)
	}
	if b := nudgeBoundaries("2026-10-27"); !b.WeekendsOff {
		t.Errorf("no weekend from its first day: %+v", b)
	}
}
