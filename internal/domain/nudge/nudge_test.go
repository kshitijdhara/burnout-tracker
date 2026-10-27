package nudge

import (
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/schedule"
)

const (
	friday   = "2026-10-23"
	saturday = "2026-10-24"
)

func settings() Settings {
	w, err := schedule.Parse("07:00-23:00")
	if err != nil {
		panic(err)
	}
	return Settings{Enabled: true, Waking: w}
}

// at is a moment on day, at hh:mm, with the user working and nothing
// suppressing.
func at(day string, hh, mm int) Now {
	d, _ := time.ParseInLocation(time.DateOnly, day, time.UTC)
	return Now{At: d.Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute), Day: day, Known: true, Active: true}
}

var shutdown = Boundaries{Shutdown: 21*time.Hour + 30*time.Minute, HasShutdown: true, WeekendsOff: true}

func TestDecide(t *testing.T) {
	off := func(k Kind) Settings { s := settings(); s.Off = map[Kind]bool{k: true}; return s }
	disabled := settings()
	disabled.Enabled = false
	nightOwl := settings()
	nightOwl.Waking, _ = schedule.Parse("09:00-03:00")
	idle := at(friday, 22, 0)
	idle.Active = false

	cases := []struct {
		name       string
		s          Settings
		b          Boundaries
		guardrails []string
		today      Today
		now        Now
		want       Kind
		show       bool
	}{
		{"nothing applies", settings(), Boundaries{}, nil, Today{}, at(friday, 22, 0), "", false},
		{"before the shutdown time", settings(), shutdown, nil, Today{}, at(friday, 21, 29), "", false},
		{"at the shutdown time", settings(), shutdown, nil, Today{}, at(friday, 21, 30), Shutdown, true},
		{"shutdown, but not working", settings(), shutdown, nil, Today{}, idle, "", false},
		{"after a shutdown past midnight", nightOwl, Boundaries{Shutdown: 25 * time.Hour, HasShutdown: true}, nil, Today{},
			Now{At: at(saturday, 1, 30).At, Day: friday, Known: true, Active: true}, Shutdown, true},
		{"before a shutdown past midnight", settings(), Boundaries{Shutdown: 25 * time.Hour, HasShutdown: true}, nil, Today{}, at(friday, 22, 59), "", false},
		{"no shutdown on a weekend", settings(), Boundaries{Shutdown: 21 * time.Hour, HasShutdown: true}, nil, Today{}, at(saturday, 22, 0), "", false},
		{"working on a day off", settings(), shutdown, nil, Today{}, at(saturday, 11, 0), DayOff, true},
		{"a guardrail", settings(), Boundaries{}, []string{"weekly_hours"}, Today{}, at(friday, 10, 0), WeeklyHours, true},
		{"boundaries before guardrails", settings(), shutdown, []string{"overnight_gap"}, Today{}, at(friday, 22, 0), Shutdown, true},
		{"guardrails in order", settings(), Boundaries{}, []string{"weekend_work", "overnight_gap"}, Today{}, at(friday, 10, 0), OvernightGap, true},
		{"unknown guardrail", settings(), Boundaries{}, []string{"something_new"}, Today{}, at(friday, 10, 0), "", false},
		{"a kind turned off", off(Shutdown), shutdown, []string{"weekly_hours"}, Today{}, at(friday, 22, 0), WeeklyHours, true},
		{"nudges off", disabled, shutdown, nil, Today{}, at(friday, 22, 0), "", false},

		// One a day: today's kind sticks, and a snooze ends the day.
		{"today's kind sticks", settings(), shutdown, nil, Today{Day: friday, Kind: WeeklyHours}, at(friday, 22, 0), WeeklyHours, true},
		{"snoozed", settings(), shutdown, nil, Today{Day: friday, Kind: Shutdown, Snoozed: true}, at(friday, 22, 0), "", false},
		{"snooze ends at the next day", settings(), shutdown, nil, Today{Day: friday, Kind: Shutdown, Snoozed: true}, at(saturday, 11, 0), DayOff, true},
		{"today's kind turned off", off(WeeklyHours), shutdown, nil, Today{Day: friday, Kind: WeeklyHours}, at(friday, 22, 0), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.s, tc.b, tc.guardrails, tc.today, tc.now)
			if d.Kind != tc.want || d.Show != tc.show {
				t.Errorf("got %+v, want kind %q show %t", d, tc.want, tc.show)
			}
		})
	}
}

func TestSuppression(t *testing.T) {
	late := settings()
	late.Waking, _ = schedule.Parse("07:00-22:00")
	cases := []struct {
		name string
		s    Settings
		edit func(*Now)
		why  string
	}{
		{"nothing", settings(), func(*Now) {}, ""},
		{"paused", settings(), func(n *Now) { n.Paused = true }, "paused"},
		{"private hours", settings(), func(n *Now) { n.Private = true }, "private hours"},
		{"after waking hours", late, func(*Now) {}, "outside waking hours"},
		{"before waking hours", settings(), func(n *Now) { n.At = n.At.Add(-17 * time.Hour) }, "outside waking hours"},
		{"no collector", settings(), func(n *Now) { n.Known = false }, "the desktop collector hasn't reported"},
		{"in a call", settings(), func(n *Now) { n.InCall = true }, "in a call"},
		{"video", settings(), func(n *Now) { n.Video = true }, "video is playing"},
		{"screen share", settings(), func(n *Now) { n.ScreenShare = true }, "the screen is shared"},
		{"focus", settings(), func(n *Now) { n.Focus = true }, "Focus or Do Not Disturb is on"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := at(friday, 22, 0)
			tc.edit(&now)
			d := Decide(tc.s, Boundaries{}, []string{"weekly_hours"}, Today{}, now)
			if d.Why != tc.why || d.Show != (tc.why == "") {
				t.Errorf("got %+v, want why %q", d, tc.why)
			}
			// Suppression hides the nudge; it doesn't use up the day.
			if d.Kind != WeeklyHours {
				t.Errorf("kind %q, want weekly_hours even while suppressed", d.Kind)
			}
		})
	}
}

// TestCopy keeps the copy supportive and free of numbers: nudges are read
// at a glance, maybe by someone looking over a shoulder.
func TestCopy(t *testing.T) {
	blame := []string{"should", "too much", "too many", "again", "failed", "must"}
	for _, k := range Kinds() {
		c := CopyOf(k)
		if c.Title == "" || c.Text == "" {
			t.Errorf("%s: no copy", k)
		}
		if len(c.Title) > 16 {
			t.Errorf("%s: title %q is too long for the menu bar", k, c.Title)
		}
		for _, s := range []string{c.Title, c.Text} {
			if strings.IndexFunc(s, unicode.IsDigit) >= 0 {
				t.Errorf("%s: %q names a number", k, s)
			}
			for _, w := range blame {
				if strings.Contains(strings.ToLower(s), w) {
					t.Errorf("%s: %q says %q", k, s, w)
				}
			}
		}
	}
}
