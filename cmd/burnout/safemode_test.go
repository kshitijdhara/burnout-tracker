package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/control"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/crashloop"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/service"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
)

// TestCleanStopIsNotACrash: the work forgets its start as soon as collect
// returns, before the tray's macOS logout handler lets the process exit,
// so logouts and stops never add up to safe mode.
func TestCleanStopIsNotACrash(t *testing.T) {
	dir := t.TempDir()
	ts := &trayState{dataDir: dir, settings: config.Default}
	now := time.Now()
	for i := range crashloop.Limit + 1 {
		h, started, err := crashloop.Record(dir, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if h.Safe {
			t.Fatalf("safe mode after %d clean stops", i)
		}
		work := trayWork(ts, cleanStop(dir, started), func(context.Context) error { return nil })
		if err := work(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		if n := len(crashloop.Load(dir).Starts); n != 0 {
			t.Fatalf("%d starts left once the work returned", n)
		}
	}

	// Leaving safe mode clears the history itself; the work doesn't touch it.
	_, started, err := crashloop.Record(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	work := trayWork(ts, cleanStop(dir, started), func(context.Context) error { return errSafeModeOver })
	if err := work(context.Background(), true); !errors.Is(err, errSafeModeOver) {
		t.Fatal(err)
	}
	if n := len(crashloop.Load(dir).Starts); n != 1 {
		t.Errorf("%d starts, want the one safe mode's end leaves to Clear", n)
	}

	// Without the icon nothing is collected, and the work calls nothing.
	called := false
	work = trayWork(ts, func(error) { called = true }, func(context.Context) error {
		t.Error("collected without the icon")
		return nil
	})
	if err := work(context.Background(), false); !errors.Is(err, errNoTray) || called {
		t.Errorf("no icon: %v, stopped called %v", err, called)
	}
}

// stopRunner checks, when the service is stopped, that the crash-loop
// history is already gone: Task Scheduler's stop kills the daemon.
type stopRunner struct {
	t       *testing.T
	dataDir string
	stopped bool
}

func (r *stopRunner) Run(_ string, args ...string) (string, error) {
	if slices.Contains(args, "disable") {
		r.stopped = true
		if _, err := os.Stat(filepath.Join(r.dataDir, crashloop.FileName)); !errors.Is(err, os.ErrNotExist) {
			r.t.Errorf("the service stopped before the start history was cleared: %v", err)
		}
	}
	return "", nil
}

func TestServiceUninstallIsNotACrash(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(paths.EnvDataDir, dir)
	t.Setenv(paths.EnvRuntimeDir, t.TempDir())
	if _, _, err := crashloop.Record(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	r := &stopRunner{t: t, dataDir: dir}
	m := service.Manager{
		GOOS: "linux", Bin: "/usr/local/bin/burnout", Args: daemonServiceArgs, Home: home,
		Config: filepath.Join(home, ".config"), LogDir: filepath.Join(dir, "logs"), Run: r, Sleep: func(time.Duration) {},
	}
	if err := uninstallService(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if !r.stopped {
		t.Error("the service wasn't stopped")
	}
}

func TestSafeModeDropsTheCollectorAndShowsInTheTray(t *testing.T) {
	safeMode.Store(true)
	t.Cleanup(func() { safeMode.Store(false) })
	if c := screenCollector(slog.New(slog.NewTextHandler(io.Discard, nil)), true, true); c != nil {
		_ = c.Close()
		t.Fatal("a desktop collector in safe mode")
	}
	dir := t.TempDir()
	now := time.Date(2026, 10, 27, 15, 0, 0, 0, time.Local)
	if s := statusNow(dir, config.Default(), now); s.Paused || !strings.HasPrefix(s.Text, "Safe mode") {
		t.Errorf("safe mode: %+v", s)
	}
	// A pause still says so first: it is what the user did last.
	if err := control.SetPause(dir, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s := statusNow(dir, config.Default(), now); !s.Paused {
		t.Errorf("paused in safe mode: %+v", s)
	}
}
