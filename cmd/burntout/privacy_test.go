package main

import (
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/control"
	"github.com/kshitijdhara/burntout/internal/domain/annotation"
)

var now = time.Date(2026, 10, 25, 14, 30, 0, 0, time.Local)

func TestPauseUntil(t *testing.T) {
	if got, err := pauseUntil("90m", now, 4*time.Hour); err != nil || !got.Equal(now.Add(90*time.Minute)) {
		t.Errorf("90m: %v %v", got, err)
	}
	if got, _ := pauseUntil("tomorrow", now, 4*time.Hour); !got.Equal(time.Date(2026, 10, 26, 4, 0, 0, 0, time.Local)) {
		t.Errorf("tomorrow: %v", got)
	}
	// At 02:00 the logical day is still yesterday's, so tomorrow is 04:00 today.
	late := time.Date(2026, 10, 26, 2, 0, 0, 0, time.Local)
	if got, _ := pauseUntil("tomorrow", late, 4*time.Hour); !got.Equal(time.Date(2026, 10, 26, 4, 0, 0, 0, time.Local)) {
		t.Errorf("tomorrow after midnight: %v", got)
	}
	for _, bad := range []string{"soon", "-1h", "0s", "800h"} {
		if _, err := pauseUntil(bad, now, 4*time.Hour); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if got := describePause(control.Pause{}, now); got != "until you resume" {
		t.Errorf("describe: %q", got)
	}
	if got := describePause(control.Pause{Until: now.Add(time.Hour)}, now); got != "until 15:30" {
		t.Errorf("describe: %q", got)
	}
}

func TestForgetFrom(t *testing.T) {
	r := 4 * time.Hour
	for _, tc := range []struct {
		last  time.Duration
		today bool
		since string
		all   bool
		want  time.Time
		what  string
	}{
		{last: time.Hour, want: now.Add(-time.Hour), what: "the last 1h0m0s"},
		{today: true, want: time.Date(2026, 10, 25, 4, 0, 0, 0, time.Local), what: "today"},
		{since: "2026-10-20", want: time.Date(2026, 10, 20, 4, 0, 0, 0, time.Local), what: "since 2026-10-20"},
		{all: true, want: time.UnixMilli(0), what: "everything recorded"},
	} {
		got, what, err := forgetFrom(tc.last, tc.today, tc.since, tc.all, now, r)
		if err != nil || !got.Equal(tc.want) || !strings.Contains(what, tc.what) {
			t.Errorf("%+v: %v %q %v", tc, got, what, err)
		}
	}
	for name, f := range map[string]func() error{
		"none":     func() error { _, _, err := forgetFrom(0, false, "", false, now, r); return err },
		"two":      func() error { _, _, err := forgetFrom(time.Hour, true, "", false, now, r); return err },
		"bad date": func() error { _, _, err := forgetFrom(0, false, "last week", false, now, r); return err },
		"negative": func() error { _, _, err := forgetFrom(-time.Hour, false, "", false, now, r); return err },
	} {
		if f() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseAnnotation(t *testing.T) {
	a, err := parseAnnotation([]string{"vacation", "2026-10-20", "2026-10-24", "--note", "Lisbon"}, "2026-10-25")
	if err != nil || a.Kind != annotation.Vacation || a.From != "2026-10-20" || a.To != "2026-10-24" || a.Note != "Lisbon" {
		t.Fatalf("%+v %v", a, err)
	}
	if got := describeAnnotation(a); got != "vacation, 2026-10-20 to 2026-10-24 (5 days): Lisbon" {
		t.Errorf("describe: %q", got)
	}
	a, err = parseAnnotation([]string{"on-call", "yesterday"}, "2026-10-25")
	if err != nil || a.Kind != annotation.OnCall || a.From != "2026-10-24" || a.To != "2026-10-24" {
		t.Fatalf("%+v %v", a, err)
	}
	if got := describeAnnotation(a); got != "on-call, 2026-10-24" {
		t.Errorf("describe: %q", got)
	}
	for _, bad := range [][]string{
		{"vacation"}, {"party", "today"}, {"sick", "today", "yesterday"}, {"sick", "today", "--note"},
		{"other", "today", "--note", "a\x1b]0;x\x07"},
	} {
		if _, err := parseAnnotation(bad, "2026-10-25"); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestPauseEndAndRecentStart(t *testing.T) {
	now := time.Date(2026, 10, 27, 15, 30, 0, 0, time.Local)
	if got := pauseEnd(true, false, now); !got.Equal(now.Add(time.Hour)) {
		t.Errorf("an hour: %v", got)
	}
	if got := pauseEnd(false, false, now); !got.IsZero() {
		t.Errorf("until resumed: %v", got)
	}
	want, _ := pauseUntil("tomorrow", now, rollover)
	if got := pauseEnd(false, true, now); !got.Equal(want) || !got.After(now) {
		t.Errorf("tomorrow: %v, want %v", got, want)
	}
	if got, err := recentStart(true, now); err != nil || !got.Equal(now.Add(-time.Hour)) {
		t.Errorf("last hour: %v %v", got, err)
	}
	wantToday, _, _ := forgetFrom(0, true, "", false, now, rollover)
	if got, err := recentStart(false, now); err != nil || !got.Equal(wantToday) {
		t.Errorf("today: %v %v, want %v", got, err, wantToday)
	}
}
