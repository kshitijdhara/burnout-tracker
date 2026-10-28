package features

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/model"
)

var loc = time.FixedZone("", 2*3600)

// at returns 2026-10-21 (a Wednesday) at hh:mm local.
func at(hh, mm int) time.Time { return time.Date(2026, 10, 21, hh, mm, 0, 0, loc) }

// minutes returns n full minutes of label/cat starting at start.
func minutes(start time.Time, n int, label string, cat model.Category) []Minute {
	out := make([]Minute, n)
	for i := range out {
		out[i] = Minute{At: start.Add(time.Duration(i) * time.Minute), Label: label, Category: cat, AttendedS: 60}
	}
	return out
}

func concat(parts ...[]Minute) []Minute {
	var out []Minute
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func value(t *testing.T, name string, d Day) (float64, bool) {
	t.Helper()
	f, ok := ByName(name)
	if !ok {
		t.Fatalf("no feature %q", name)
	}
	return f.Compute(d, DefaultParams())
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestRegistryIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range Registry() {
		if seen[f.Name] {
			t.Errorf("duplicate feature %q", f.Name)
		}
		seen[f.Name] = true
		if f.Version < 1 || f.Unit == "" || f.Label == "" || f.Doc == "" || f.Compute == nil || f.Epsilon <= 0 {
			t.Errorf("feature %q is missing a field: %+v", f.Name, f)
		}
		switch f.Group {
		case GroupLoad, GroupFragmentation, GroupRecovery, GroupFriction, GroupAgent:
		default:
			t.Errorf("feature %q has unknown group %q", f.Name, f.Group)
		}
		if _, ok := f.Weights[RoleDeveloper]; !ok {
			t.Errorf("feature %q has no developer weight", f.Name)
		}
	}
}

func TestLoadFeatures(t *testing.T) {
	d := Day{Minutes: concat(
		minutes(at(8, 0), 60, "editor", model.CategoryCode),   // before core hours
		minutes(at(10, 0), 120, "editor", model.CategoryCode), // core
		minutes(at(13, 0), 30, "zoom", model.CategoryMeeting),
		minutes(at(20, 0), 30, "player", model.CategoryMedia), // not work
	)}
	// A call while the browser has attention counts as meeting time too.
	call := minutes(at(15, 0), 15, "browser", model.CategoryBrowser)
	for i := range call {
		call[i].CallS = 60
	}
	d.Minutes = append(d.Minutes, call...)

	if v, _ := value(t, "attended_hours", d); !near(v, (60+120+30+15)/60.0) {
		t.Errorf("attended_hours = %v", v)
	}
	if v, ok := value(t, "after_hours_share", d); !ok || !near(v, 60.0/225) {
		t.Errorf("after_hours_share = %v, %v", v, ok)
	}
	if v, _ := value(t, "meeting_hours", d); !near(v, 45.0/60) {
		t.Errorf("meeting_hours = %v", v)
	}
	d.Weekend = true
	if _, ok := value(t, "after_hours_share", d); ok {
		t.Error("after_hours_share computed on a weekend")
	}
}

func TestFragmentationFeatures(t *testing.T) {
	d := Day{Minutes: concat(
		minutes(at(9, 0), 50, "editor", model.CategoryCode),
		minutes(at(9, 50), 2, "unattributed", model.CategoryUncategorized), // ignored
		minutes(at(9, 52), 20, "terminal", model.CategoryTerminal),         // same focus block
		minutes(at(10, 12), 10, "slack", model.CategoryComms),
		minutes(at(10, 22), 18, "editor", model.CategoryCode),
	)}
	d.Minutes[50].Label, d.Minutes[51].Label = model.UnattributedWindowKey, model.UnattributedWindowKey
	// editor→terminal→slack→editor: 3 switches in 100 attended minutes.
	if v, ok := value(t, "switches_per_hour", d); !ok || !near(v, 3/(100.0/60)) {
		t.Errorf("switches_per_hour = %v, %v", v, ok)
	}
	if v, _ := value(t, "longest_focus_block", d); !near(v, 72) {
		t.Errorf("longest_focus_block = %v, want 72 (code + terminal across 2 unattributed minutes)", v)
	}
	if v, _ := value(t, "comms_share", d); !near(v, 10.0/100) {
		t.Errorf("comms_share = %v", v)
	}
	// Too little attention: rates aren't computed.
	short := Day{Minutes: minutes(at(9, 0), 10, "editor", model.CategoryCode)}
	for _, name := range []string{"switches_per_hour", "longest_focus_block", "comms_share"} {
		if _, ok := value(t, name, short); ok {
			t.Errorf("%s computed on a 10-minute day", name)
		}
	}
}

func TestFocusBlockBrokenByOtherWork(t *testing.T) {
	d := Day{Minutes: concat(
		minutes(at(9, 0), 30, "editor", model.CategoryCode),
		minutes(at(9, 30), 1, "slack", model.CategoryComms),
		minutes(at(9, 31), 20, "editor", model.CategoryCode),
		minutes(at(11, 0), 25, "editor", model.CategoryCode), // after a long gap
	)}
	if v, _ := value(t, "longest_focus_block", d); !near(v, 30) {
		t.Errorf("longest_focus_block = %v, want 30", v)
	}
}

func TestRecoveryFeatures(t *testing.T) {
	d := Day{
		Presence: []Span{
			{Start: at(9, 0), End: at(10, 0)},
			{Start: at(10, 3), End: at(12, 0)}, // 3 min: not a break
			{Start: at(13, 0), End: at(15, 0)}, // 60 min break
			{Start: at(15, 10), End: at(18, 0)},
		},
		PrevLastPresence: at(9, 0).Add(-14 * time.Hour),
	}
	if v, _ := value(t, "breaks", d); v != 2 {
		t.Errorf("breaks = %v, want 2", v)
	}
	if v, _ := value(t, "longest_break", d); !near(v, 60) {
		t.Errorf("longest_break = %v", v)
	}
	if v, ok := value(t, "overnight_gap", d); !ok || !near(v, 14) {
		t.Errorf("overnight_gap = %v, %v", v, ok)
	}
	d.PrevLastPresence = time.Time{}
	if _, ok := value(t, "overnight_gap", d); ok {
		t.Error("overnight_gap without a previous day")
	}
}

func TestFrictionFeatures(t *testing.T) {
	cmd := func(hh, mm int, argv0, group string, exit int) Span {
		return Span{Start: at(hh, mm), End: at(hh, mm).Add(10 * time.Second), Argv0: argv0, Group: group, Exit: exit}
	}
	d := Day{Shell: []Span{
		cmd(9, 0, "go", "build", 1), cmd(9, 1, "go", "build", 1), cmd(9, 2, "go", "build", 1), // a loop
		cmd(9, 3, "go", "build", 1), // still the same loop
		cmd(9, 4, "go", "build", 0),
		cmd(9, 30, "make", "build", 2), cmd(9, 31, "make", "build", 2),
		cmd(10, 0, "ls", "other", 0), cmd(10, 1, "ls", "other", 0),
		cmd(23, 30, "kubectl", "infra", 0), cmd(23, 40, "ssh", "remote", 0), cmd(21, 0, "ssh", "remote", 0),
	}}
	if v, ok := value(t, "shell_failure_rate", d); !ok || !near(v, 6.0/12) {
		t.Errorf("shell_failure_rate = %v, %v", v, ok)
	}
	if v, _ := value(t, "failure_loops", d); v != 1 {
		t.Errorf("failure_loops = %v, want 1", v)
	}
	if v, _ := value(t, "late_night_infra", d); v != 2 {
		t.Errorf("late_night_infra = %v, want 2", v)
	}
	if _, ok := value(t, "shell_failure_rate", Day{Shell: d.Shell[:5]}); ok {
		t.Error("failure rate computed from 5 commands")
	}
}

func TestAgentFeatures(t *testing.T) {
	d := Day{
		Agent: []Span{
			{Start: at(9, 0), End: at(10, 0), State: "working", Session: "a"},
			{Start: at(9, 30), End: at(10, 30), State: "working", Session: "b"},
			{Start: at(10, 30), End: at(10, 45), State: "blocked_on_human", Session: "b"},
		},
		Human: []Span{
			{Start: at(9, 0), End: at(9, 30), State: "reading"},
			{Start: at(9, 30), End: at(9, 40), State: "composing"},
		},
	}
	// 0:30 with one, 0:30 with two, 0:30 with one: mean 4/3.
	if v, ok := value(t, "agent_parallelism", d); !ok || !near(v, 4.0/3) {
		t.Errorf("agent_parallelism = %v, %v", v, ok)
	}
	if v, _ := value(t, "agent_blocked_minutes", d); !near(v, 15) {
		t.Errorf("agent_blocked_minutes = %v", v)
	}
	if v, _ := value(t, "reading_share", d); !near(v, 0.75) {
		t.Errorf("reading_share = %v", v)
	}
	if _, ok := value(t, "agent_parallelism", Day{}); ok {
		t.Error("agent_parallelism without agents")
	}
}

func TestValuesSkipsWhatDoesntApply(t *testing.T) {
	v := Values(Day{Weekend: true}, DefaultParams())
	if _, ok := v["after_hours_share"]; ok {
		t.Error("weekday feature on a weekend")
	}
	if got, ok := v["attended_hours"]; !ok || got != 0 {
		t.Errorf("attended_hours = %v, %v", got, ok)
	}
}

// Core hours come from the profile: the same day is after-hours work for
// a 09:00 starter and core work for a 07:00 starter.
func TestCoreHoursParam(t *testing.T) {
	d := Day{Minutes: concat(
		minutes(at(7, 0), 120, "editor", model.CategoryCode),  // 07:00–09:00
		minutes(at(10, 0), 120, "editor", model.CategoryCode), // core either way
	)}
	f, _ := ByName("after_hours_share")
	def, _ := f.Compute(d, DefaultParams())
	early := DefaultParams()
	early.CoreStart, early.CoreEnd = 7*time.Hour, 16*time.Hour
	got, _ := f.Compute(d, early)
	if !near(def, 0.5) || !near(got, 0) {
		t.Errorf("after-hours share: %v with 09:00-18:00, %v with 07:00-16:00", def, got)
	}
	if early.Key() == DefaultParams().Key() || DefaultParams().Key() != DefaultKey || !strings.Contains(early.Key(), "core=07:00-16:00") {
		t.Errorf("keys: default %q, early %q", DefaultParams().Key(), early.Key())
	}
	for _, r := range Roles() {
		if !r.Valid() || r.Describe() == "" {
			t.Errorf("role %q", r)
		}
		for _, f := range Registry() {
			if w := f.Weight(r); w < 0 || w > 1 {
				t.Errorf("%s weight for %s: %v", f.Name, r, w)
			}
		}
	}
}

// Calendar meetings count once with on-screen meeting time, and in-person
// ones (no screen time) count too.
func TestCalendarFeatures(t *testing.T) {
	busy := func(h1, m1, h2, m2 int) Span { return Span{Start: at(h1, m1), End: at(h2, m2)} }
	d := Day{
		Minutes: minutes(at(10, 0), 30, "zoom", model.CategoryMeeting), // 10:00–10:30 on screen
		Busy: []Span{
			busy(10, 0, 10, 30), // the same call, on the calendar
			busy(10, 30, 11, 0), // back to back, in person
			busy(11, 3, 11, 30), // 3 minutes later: back to back
			busy(11, 3, 11, 30), // the same meeting on a second calendar
			busy(14, 0, 15, 0),  // not back to back
			busy(17, 30, 19, 0), // 1 h after 18:00
		},
		Calendar: true,
	}
	if v, _ := value(t, "meeting_hours", d); !near(v, (30+30+27+60+90)/60.0) {
		t.Errorf("meeting_hours = %v", v)
	}
	if v, ok := value(t, "back_to_back_meetings", d); !ok || v != 2 {
		t.Errorf("back_to_back_meetings = %v %v", v, ok)
	}
	if v, ok := value(t, "after_hours_meetings", d); !ok || !near(v, 1) {
		t.Errorf("after_hours_meetings = %v %v", v, ok)
	}
	// Without a calendar the calendar-only features don't apply, and a
	// day with a calendar but no meetings is 0, not missing.
	d.Calendar, d.Busy = false, nil
	for _, name := range []string{"back_to_back_meetings", "after_hours_meetings"} {
		if _, ok := value(t, name, d); ok {
			t.Errorf("%s computed without a calendar", name)
		}
	}
	d.Calendar = true
	if v, ok := value(t, "back_to_back_meetings", d); !ok || v != 0 {
		t.Errorf("no meetings: %v %v", v, ok)
	}
	if v, _ := value(t, "meeting_hours", d); !near(v, 0.5) {
		t.Errorf("screen-only meeting_hours = %v", v)
	}
}

func TestCommsInterruptRate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		minutes []Minute
		want    float64
		ok      bool
	}{
		{name: "empty day"},
		{name: "too little attention", minutes: minutes(at(9, 0), 10, "slack", model.CategoryComms)},
		{
			name: "into comms twice", ok: true, want: 2,
			minutes: concat(
				minutes(at(9, 0), 20, "editor", model.CategoryCode),
				minutes(at(9, 20), 5, "slack", model.CategoryComms),
				minutes(at(9, 25), 5, "mail", model.CategoryComms), // within comms: not another interrupt
				minutes(at(9, 30), 20, "editor", model.CategoryCode),
				minutes(at(9, 50), 10, "slack", model.CategoryComms),
			),
		},
		{
			name: "after a gap is not a switch", ok: true, want: 0,
			minutes: concat(
				minutes(at(9, 0), 40, "editor", model.CategoryCode),
				minutes(at(11, 0), 20, "slack", model.CategoryComms),
			),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := value(t, "comms_interrupt_rate", Day{Minutes: tc.minutes})
			if ok != tc.ok || !near(v, tc.want) {
				t.Errorf("= %v, %v; want %v, %v", v, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestAgentSwitchesPerHour(t *testing.T) {
	work := func(h1, m1, h2, m2 int, session string) Span {
		return Span{Start: at(h1, m1), End: at(h2, m2), State: "working", Session: session}
	}
	// editor→browser→editor→slack, one switch every 15 minutes, 09:00–10:00.
	day := concat(
		minutes(at(9, 0), 15, "editor", model.CategoryCode),
		minutes(at(9, 15), 15, "browser", model.CategoryBrowser),
		minutes(at(9, 30), 15, "editor", model.CategoryCode),
		minutes(at(9, 45), 15, "slack", model.CategoryComms),
	)
	for _, tc := range []struct {
		name  string
		agent []Span
		want  float64
		ok    bool
	}{
		{name: "no agents"},
		{name: "agents only idle", agent: []Span{{Start: at(9, 0), End: at(10, 0), State: "idle"}}},
		{name: "too little working time", agent: []Span{work(9, 0, 9, 20, "a")}},
		{name: "working all hour", agent: []Span{work(9, 0, 10, 0, "a")}, ok: true, want: 3},
		{
			// Overlapping sessions count once; the switch at 09:45 is
			// outside the union.
			name: "parallel sessions", ok: true, want: 2 / (40.0 / 60),
			agent: []Span{work(9, 5, 9, 40, "a"), work(9, 10, 9, 45, "b")},
		},
		{
			// A switch across a gap in agent work doesn't count.
			// Touching pieces join; an inverted span is ignored.
			name: "two stretches", ok: true, want: 2 / (40.0 / 60),
			agent: []Span{work(9, 10, 9, 20, "a"), work(9, 20, 9, 40, "a"), work(9, 50, 10, 0, "b"), work(9, 45, 9, 42, "c")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := value(t, "agent_switches_per_hour", Day{Minutes: day, Agent: tc.agent})
			if ok != tc.ok || !near(v, tc.want) {
				t.Errorf("= %v, %v; want %v, %v", v, ok, tc.want, tc.ok)
			}
		})
	}
	if _, ok := value(t, "agent_switches_per_hour", Day{Agent: []Span{work(9, 0, 10, 0, "a")}}); ok {
		t.Error("computed on an empty day")
	}
}

func TestDaysSinceLightDay(t *testing.T) {
	// hours returns earlier days ending the day before 2026-10-21, oldest
	// first.
	hours := func(h ...float64) []PriorDay {
		out := make([]PriorDay, len(h))
		end := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
		for i, v := range h {
			out[i] = PriorDay{Date: end.AddDate(0, 0, i-len(h)).Format(time.DateOnly), AttendedH: v}
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		date  string
		prior []PriorDay
		want  float64
		ok    bool
	}{
		{name: "empty day", date: "2026-10-21"},
		{name: "no date", prior: hours(8)},
		{name: "nothing within 28 days", date: "2026-10-21", prior: []PriorDay{{Date: "2026-09-01", AttendedH: 1}}},
		{name: "later days aren't seen", date: "2026-10-21", prior: []PriorDay{{Date: "2026-10-22", AttendedH: 1}, {Date: "2026-10-21", AttendedH: 1}}},
		{name: "fallback: yesterday light", date: "2026-10-21", prior: hours(8, 3), ok: true, want: 1},
		{name: "fallback: no light day", date: "2026-10-21", prior: hours(8, 9, 7), ok: true, want: 4},
		{
			// p25 of these 8 days is 9 h: only the 6 h day is light.
			name: "own percentile", date: "2026-10-21", ok: true, want: 6,
			prior: hours(9, 10, 6, 9, 10, 9, 9, 9),
		},
		{
			// A heavy worker's 7 h day is light for them, though not under
			// the fallback.
			name: "percentile above fallback", date: "2026-10-21", ok: true, want: 2,
			prior: hours(11, 12, 11, 12, 11, 12, 7, 12),
		},
		{
			name: "unordered and with a gap", date: "2026-10-21", ok: true, want: 3,
			prior: []PriorDay{{Date: "2026-10-20", AttendedH: 9}, {Date: "2026-10-12", AttendedH: 2}, {Date: "2026-10-15", AttendedH: 9}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := value(t, "days_since_light_day", Day{Date: tc.date, Prior: tc.prior})
			if ok != tc.ok || !near(v, tc.want) {
				t.Errorf("= %v, %v; want %v, %v", v, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestApprovalLatency(t *testing.T) {
	blocked := func(h1, m1, h2, m2 int, session string) Span {
		return Span{Start: at(h1, m1), End: at(h2, m2), State: "blocked_on_human", Session: session}
	}
	for _, tc := range []struct {
		name  string
		agent []Span
		want  float64
		ok    bool
	}{
		{name: "empty day"},
		{name: "never waited", agent: []Span{{Start: at(9, 0), End: at(10, 0), State: "working", Session: "a"}}},
		{name: "one wait", agent: []Span{blocked(9, 0, 9, 4, "a")}, ok: true, want: 4},
		{
			name: "median of three", ok: true, want: 2,
			agent: []Span{blocked(9, 0, 9, 1, "a"), blocked(9, 0, 9, 2, "b"), blocked(10, 0, 10, 30, "a")},
		},
		{
			// A wait split at a restart is one wait; an empty one is none.
			name: "split wait", ok: true, want: 10,
			agent: []Span{blocked(9, 0, 9, 6, "a"), blocked(9, 6, 9, 10, "a"), blocked(11, 0, 11, 0, "a")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := value(t, "approval_latency", Day{Agent: tc.agent})
			if ok != tc.ok || !near(v, tc.want) {
				t.Errorf("= %v, %v; want %v, %v", v, ok, tc.want, tc.ok)
			}
		})
	}
}
