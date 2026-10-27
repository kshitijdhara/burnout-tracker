package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/egress/update"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
)

func withVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

func TestUpdateNotice(t *testing.T) {
	withVersion(t, "1.1.0")
	dir := t.TempDir()
	now := time.Date(2026, 10, 26, 15, 0, 0, 0, time.Local)
	if err := update.SaveState(dir, update.State{Checked: now, Tried: now, Latest: "1.2.0"}); err != nil {
		t.Fatal(err)
	}
	c := config.Default()
	if s := statusNow(dir, c, now); s.Text != "Recording" {
		t.Errorf("check off: %q", s.Text)
	}
	c.Update.Check = config.UpdateWeekly
	if s := statusNow(dir, c, now); s.Text != "Recording · update 1.2.0 available" {
		t.Errorf("update found: %q", s.Text)
	}
	withVersion(t, "1.2.0")
	if s := statusNow(dir, c, now); s.Text != "Recording" {
		t.Errorf("after updating: %q", s.Text)
	}
}

func TestUpdateLoop(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	write := func(c config.Config) {
		t.Helper()
		if err := os.WriteFile(cfgPath, config.Render(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(config.Default())
	live, err := newLiveConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := make(chan struct{}, 10)
	check := func(context.Context) (update.Version, error) {
		calls <- struct{}{}
		return update.ParseVersion("2.0.0")
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	// run runs the loop until done reports true (or for a short while when
	// done is nil), then a few more wakes, so a second check would show.
	run := func(done func() bool) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			updateLoopWith(ctx, log, dir, live, check, time.Millisecond, 5*time.Millisecond)
		}()
		for deadline := time.Now().Add(10 * time.Second); done != nil && !done(); {
			if time.Now().After(deadline) {
				t.Error("the loop didn't check in time")
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		cancel()
		<-stopped
	}

	run(nil)
	if len(calls) != 0 {
		t.Fatalf("checked %d times while off", len(calls))
	}
	c := config.Default()
	c.Update.Check = config.UpdateWeekly
	write(c)
	if _, _, err := live.check(); err != nil {
		t.Fatal(err)
	}
	run(func() bool { return update.LoadState(dir).Latest != "" })
	if len(calls) != 1 {
		t.Fatalf("checked %d times in one week, want 1", len(calls))
	}
	if st := update.LoadState(dir); st.Latest != "2.0.0" || st.Checked.IsZero() {
		t.Errorf("state: %+v", st)
	}

	// A failure is retried a day later, not every wake.
	if err := update.SaveState(dir, update.State{}); err != nil {
		t.Fatal(err)
	}
	<-calls
	check = func(context.Context) (update.Version, error) {
		calls <- struct{}{}
		return update.Version{}, errors.New("api.github.com answered 503")
	}
	run(func() bool { return !update.LoadState(dir).Tried.IsZero() })
	if len(calls) != 1 || !strings.Contains(logs.String(), "update check failed") {
		t.Errorf("failed check: %d calls; log %s", len(calls), logs.String())
	}
	if st := update.LoadState(dir); st.Tried.IsZero() || !st.Checked.IsZero() {
		t.Errorf("state after a failure: %+v", st)
	}
}

func TestReportUpdate(t *testing.T) {
	withVersion(t, "1.1.0")
	v, _ := update.ParseVersion("1.2.0")
	var b strings.Builder
	reportUpdate(&b, update.Install{Kind: update.Package, Path: "/usr/bin/burnout"},
		update.Result{Version: v, Download: "/cache/burnout_1.2.0_linux_amd64.deb"}, false)
	if !strings.Contains(b.String(), "sudo apt install /cache/burnout_1.2.0_linux_amd64.deb") {
		t.Errorf("package: %s", b.String())
	}
	b.Reset()
	reportUpdate(&b, update.Install{Kind: update.UserPrefix}, update.Result{Version: v, Swapped: true, Previous: "/b/burnout.old"}, false)
	if !strings.Contains(b.String(), "Updated 1.1.0 to 1.2.0") || !strings.Contains(b.String(), "/b/burnout.old") {
		t.Errorf("swap: %s", b.String())
	}
}
