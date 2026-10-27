package main

import (
	"testing"
	"time"
)

func TestReminderDue(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	ago := func(d int) time.Time { return now.AddDate(0, 0, -d) }
	var never time.Time
	for _, c := range []struct {
		name            string
		consented, seen time.Time
		want            bool
	}{
		{"consented recently, never seen", ago(10), never, false},
		{"consented over a month ago, never seen", ago(31), never, true},
		{"seen recently", ago(90), ago(5), false},
		{"seen over a month ago", ago(90), ago(31), true},
		{"consent unknown, seen recently", never, ago(5), false},
		{"nothing known", never, never, true},
		{"seen in the future (clock change)", ago(90), ago(-60), true},
		{"consented in the future (clock change)", ago(-10), never, true},
	} {
		if got := reminderDue(c.consented, c.seen, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
