package features

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
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
