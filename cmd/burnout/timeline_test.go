package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/service"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/classify"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

func TestPrintTimeline(t *testing.T) {
	zone := time.FixedZone("", 330*60)
	at := func(h, m int) time.Time { return time.Date(2026, 10, 21, h, m, 0, 0, zone) }
	minute := func(t time.Time, label string, cat model.Category, call float64) sqlite.MinuteActivity {
		return sqlite.MinuteActivity{Minute: t, Label: label, Category: cat, AttendedS: 60, CallS: call}
	}
	var minutes []sqlite.MinuteActivity
	for m := range 3 {
		minutes = append(minutes, minute(at(9, m), "com.google.Chrome", model.CategoryBrowser, 60))
	}
	minutes = append(minutes,
		minute(at(9, 3), "com.google.Chrome", model.CategoryBrowser, 0), // call over: a new run
		minute(at(9, 20), "private", model.CategoryPrivate, 0),          // after a gap
		minute(at(9, 21), sqlite.UnattributedLabel, model.CategoryUncategorized, 0),
	)
	var b bytes.Buffer
	printTimeline(&b, "2026-10-21", minutes)
	want := []string{
		"09:00–09:03      3m  com.google.Chrome (browser) · call",
		"09:03–09:04      1m  com.google.Chrome (browser)\n",
		"09:04–09:20     16m  away, locked or idle",
		"09:20–09:21      1m  private\n",
		"09:21–09:22      1m  present, no clear window",
		"Attended in total: 0h06m",
	}
	out := b.String()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}

func TestSpan(t *testing.T) {
	for d, want := range map[time.Duration]string{7 * time.Minute: "7m", 65 * time.Minute: "1h05m", 0: "0m"} {
		if got := span(d); got != want {
			t.Errorf("span(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestMediaNow(t *testing.T) {
	w := func(app string, call, video bool) model.Window {
		return model.Window{AppID: app, InCall: call, HasVideo: video}
	}
	for _, tc := range []struct {
		snap model.Snapshot
		want string
	}{
		{model.Snapshot{}, "no call or video detected"},
		{model.Snapshot{CallActive: true, Windows: []model.Window{w("com.google.Chrome", true, false), w("com.google.Chrome", true, true)}},
			"in a call in com.google.Chrome; video playing in com.google.Chrome"},
		{model.Snapshot{CallActive: true}, "in a call (its window isn't visible)"},
		{model.Snapshot{Windows: []model.Window{w("com.1password.1password", false, true)}}, "video playing in a private app"},
	} {
		if got := mediaNow(tc.snap, classify.New(nil, nil)); got != tc.want {
			t.Errorf("mediaNow = %q, want %q", got, tc.want)
		}
	}
}

func TestPrintableEscapesTerminalControls(t *testing.T) {
	for in, want := range map[string]string{
		"com.google.Chrome":    "com.google.Chrome",
		"Evil\x1b]0;pwned\x07": `Evil\u001b]0;pwned\u0007`,
		"abc\u202eexe.txt":     "abc\\u202eexe.txt",
		"Café — Notes":         "Café — Notes",
	} {
		if got := printable(in); got != want {
			t.Errorf("printable(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnstableBinary(t *testing.T) {
	sep := string(filepath.Separator)
	for bin, want := range map[string]bool{
		filepath.Join(os.TempDir(), "go-build123", "b001", "exe", "burnout"): true,
		filepath.Join(os.TempDir(), "burnout"):                               true,
		sep + filepath.Join("home", "me", ".local", "bin", "burnout"):        false,
	} {
		if got := unstableBinary(bin); got != want {
			t.Errorf("unstableBinary(%q) = %v, want %v", bin, got, want)
		}
	}
}

func TestDescribeService(t *testing.T) {
	for _, tc := range []struct {
		st   service.Status
		want string
	}{
		{service.Status{Installed: true, Running: true, Detail: "pid 812"}, "running (pid 812)"},
		{service.Status{Installed: true}, "installed but not running"},
		{service.Status{}, "not installed"},
	} {
		if got := describeService(tc.st, "journalctl --user -u burnout.service"); !strings.Contains(got, tc.want) {
			t.Errorf("describeService(%+v) = %q, want it to mention %q", tc.st, got, tc.want)
		}
	}
}
