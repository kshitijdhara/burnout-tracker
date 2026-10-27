package strain

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/features"
)

var start = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) // a Monday

// routine returns a normal day: 7.5 h on weekdays with some variation,
// light weekends, regular breaks and nights.
func routine(i int) map[string]float64 {
	date := start.AddDate(0, 0, i)
	wiggle := math.Sin(float64(i) * 1.7) // deterministic day-to-day noise
	if isWeekend(date) {
		return map[string]float64{
			"attended_hours": 0.8 + 0.3*wiggle, "breaks": 4, "longest_break": 120,
			"overnight_gap": 10 + wiggle,
		}
	}
	return map[string]float64{
		"attended_hours": 7.5 + 0.5*wiggle, "after_hours_share": 0.08 + 0.03*wiggle,
		"meeting_hours": 1.5 + 0.3*wiggle, "switches_per_hour": 20 + 3*wiggle,
		"longest_focus_block": 75 + 15*wiggle, "comms_share": 0.15 + 0.03*wiggle,
		"breaks": 5 + math.Round(wiggle), "longest_break": 50 + 10*wiggle,
		"overnight_gap": 11 + 0.5*wiggle, "failure_loops": 0, "late_night_infra": 0,
	}
}

// overwork is a crunch day: long hours, late nights, few breaks.
func overwork(i int) map[string]float64 {
	v := routine(i)
	if isWeekend(start.AddDate(0, 0, i)) {
		v["attended_hours"], v["overnight_gap"] = 5, 6
		return v
	}
	v["attended_hours"], v["after_hours_share"] = 11.5, 0.4
	v["breaks"], v["longest_break"], v["overnight_gap"] = 1, 10, 6.5
	v["longest_focus_block"], v["switches_per_hour"] = 30, 35
	return v
}

func history(n int, values func(int) map[string]float64) []Day {
	days := make([]Day, n)
	for i := range days {
		days[i] = Day{
			Date: start.AddDate(0, 0, i).Format(time.DateOnly), Coverage: 0.95, Present: 6 * time.Hour,
			Values: values(i),
		}
	}
	return days
}

func last(scores []Score) Score { return scores[len(scores)-1] }

func TestCalibration(t *testing.T) {
	scores := Compute(history(14, routine), DefaultParams())
	for _, sc := range scores[:13] {
		if sc.Status != StatusCalibrating || sc.Strain != 0 {
			t.Fatalf("%s: %+v, want calibrating", sc.Date, sc)
		}
	}
	if sc := last(scores); sc.Status != StatusOK || sc.UsableDays != 14 {
		t.Fatalf("day 14: %+v, want ok", sc)
	}
}

func TestMissingDaysAreNotRest(t *testing.T) {
	days := history(20, routine)
	days[3].Coverage = 0.3
	days[4].Present = 10 * time.Minute
	days[5].Excluded = true
	scores := Compute(days, DefaultParams())
	for i, want := range map[int]string{3: "30%", 4: "barely used", 5: "vacation"} {
		if sc := scores[i]; sc.Status != StatusMissing || !strings.Contains(sc.Reason, want) {
			t.Errorf("day %d: %+v, want missing (%s)", i, sc, want)
		}
	}
	// Three missing days push the first score back by three days.
	if scores[15].Status != StatusCalibrating || scores[16].Status != StatusOK {
		t.Errorf("first score on day %d, want day 16", firstOK(scores))
	}
}

func firstOK(scores []Score) int {
	for i, sc := range scores {
		if sc.Status == StatusOK {
			return i
		}
	}
	return -1
}

func TestSteadyRoutineScoresLow(t *testing.T) {
	scores := Compute(history(60, routine), DefaultParams())
	for _, sc := range scores[20:] {
		if sc.Strain > 35 {
			t.Fatalf("%s: strain %.0f on an ordinary day; drivers %v", sc.Date, sc.Strain, texts(sc))
		}
		if len(sc.Guardrails) > 0 {
			t.Fatalf("%s: guardrails %v on an ordinary day", sc.Date, sc.Guardrails)
		}
	}
}

func TestOverworkRaisesStrainAndNamesDrivers(t *testing.T) {
	values := func(i int) map[string]float64 {
		if i >= 42 {
			return overwork(i)
		}
		return routine(i)
	}
	scores := Compute(history(49, values), DefaultParams())
	before, after := scores[41], last(scores)
	if before.Strain > 35 || after.Strain < 75 {
		t.Fatalf("strain %.0f before the crunch, %.0f after a week of it", before.Strain, after.Strain)
	}
	got := strings.Join(texts(after), "; ")
	if len(after.Drivers) != 3 || !strings.Contains(got, "higher than usual lately") {
		t.Fatalf("drivers: %s", got)
	}
	if !hasGuardrail(after, "weekly_hours") || !hasGuardrail(after, "overnight_gap") {
		t.Fatalf("guardrails: %+v", after.Guardrails)
	}
}

// A moderate, sustained rise lands mid-scale, not all-or-nothing.
func TestModerateOverworkIsGraded(t *testing.T) {
	values := func(i int) map[string]float64 {
		v := routine(i)
		if i >= 60 && !isWeekend(start.AddDate(0, 0, i)) {
			v["attended_hours"], v["after_hours_share"], v["breaks"], v["overnight_gap"] = 8.8, 0.18, 3, 9.5
		}
		return v
	}
	scores := Compute(history(75, values), DefaultParams())
	if s := scores[60].Strain; s > 30 {
		t.Errorf("one moderately long day: strain %.0f", s)
	}
	if s := scores[74].Strain; s < 40 || s > 80 {
		t.Errorf("two moderately long weeks: strain %.0f, want mid-scale", s)
	}
}

func TestLowerIsWorseFeaturesExplainAsLower(t *testing.T) {
	values := func(i int) map[string]float64 {
		v := routine(i)
		if i >= 42 && !isWeekend(start.AddDate(0, 0, i)) {
			v["breaks"], v["longest_break"], v["longest_focus_block"] = 0, 0, 10
		}
		return v
	}
	sc := last(Compute(history(47, values), DefaultParams()))
	got := strings.Join(texts(sc), "; ")
	if !strings.Contains(got, "breaks of 5 minutes or more") || !strings.Contains(got, "lower than usual lately") {
		t.Fatalf("drivers: %s", got)
	}
}

func TestWeekendsHaveTheirOwnBaseline(t *testing.T) {
	scores := Compute(history(60, routine), DefaultParams())
	for _, sc := range scores[20:] {
		for _, d := range sc.Drivers {
			if d.Feature == "attended_hours" {
				t.Fatalf("%s: ordinary hours flagged: %s", sc.Date, d.Text())
			}
		}
	}
}

func TestGuardrails(t *testing.T) {
	values := func(i int) map[string]float64 {
		v := routine(i)
		switch start.AddDate(0, 0, i).Weekday() {
		case time.Saturday, time.Sunday:
			v["attended_hours"] = 3
		}
		if i == 20 {
			v["overnight_gap"] = 5
		}
		return v
	}
	scores := Compute(history(28, values), DefaultParams())
	if !hasGuardrail(scores[20], "overnight_gap") {
		t.Errorf("short night not flagged: %+v", scores[20].Guardrails)
	}
	sunday := 27 // 2026-06-28
	if !hasGuardrail(scores[sunday], "weekend_work") || hasGuardrail(scores[sunday-1], "weekend_work") {
		t.Errorf("weekend work: sat %+v, sun %+v", scores[sunday-1].Guardrails, scores[sunday].Guardrails)
	}
	long := history(7, func(int) map[string]float64 { return map[string]float64{"attended_hours": 8} })
	if sc := last(Compute(long, DefaultParams())); !hasGuardrail(sc, "weekly_hours") {
		t.Errorf("56 h week not flagged: %+v", sc.Guardrails)
	}
}

func TestPastScoresNeverChange(t *testing.T) {
	days := history(40, routine)
	a := Compute(days[:30], DefaultParams())
	b := Compute(days, DefaultParams())
	for i := range a {
		if a[i].Strain != b[i].Strain || a[i].Status != b[i].Status {
			t.Fatalf("%s changed: %+v → %+v", a[i].Date, a[i], b[i])
		}
	}
}

// After months of creeping overwork the rolling baseline has drifted up,
// but the anchor (the calmest earlier month) keeps the score high.
func TestAnchorResistsDrift(t *testing.T) {
	values := func(i int) map[string]float64 {
		if i >= 60 {
			return overwork(i)
		}
		return routine(i)
	}
	days := history(150, values)
	with := last(Compute(days, DefaultParams()))
	p := DefaultParams()
	p.AnchorMinDays = 1 << 20 // no anchor
	without := last(Compute(days, p))
	if with.Strain < without.Strain+15 {
		t.Fatalf("strain after 3 months of overwork: %.0f with the anchor, %.0f without", with.Strain, without.Strain)
	}
}

func TestDriverText(t *testing.T) {
	d := Driver{Label: "share of work outside core hours", HigherWorse: true, Deviation: 2.13}
	if got := d.Text(); got != "share of work outside core hours: 2.1σ higher than usual lately" {
		t.Errorf("Text() = %q", got)
	}
	d = Driver{Label: "breaks", HigherWorse: false, Deviation: 1.5}
	if got := d.Text(); got != "breaks: 1.5σ lower than usual lately" {
		t.Errorf("Text() = %q", got)
	}
}

func hasGuardrail(sc Score, name string) bool {
	for _, g := range sc.Guardrails {
		if g.Name == name {
			return true
		}
	}
	return false
}

func texts(sc Score) []string {
	var out []string
	for _, d := range sc.Drivers {
		out = append(out, d.Text())
	}
	return out
}

// The same meeting-heavy fortnight weighs less for a PM, whose job is
// meetings and switching, than for a developer.
func TestRoleWeights(t *testing.T) {
	values := func(i int) map[string]float64 {
		v := routine(i)
		if i >= 60 && !isWeekend(start.AddDate(0, 0, i)) {
			v["meeting_hours"], v["switches_per_hour"], v["comms_share"], v["longest_focus_block"] = 4.5, 32, 0.3, 35
		}
		return v
	}
	days := history(75, values)
	dev := last(Compute(days, DefaultParams()))
	p := DefaultParams()
	p.Role = features.RolePM
	pm := last(Compute(days, p))
	if dev.Strain-pm.Strain < 10 {
		t.Errorf("developer %.0f, PM %.0f: the PM's meeting-heavy weeks should weigh clearly less", dev.Strain, pm.Strain)
	}
	if !features.Role("student").Valid() || features.Role("manager").Valid() {
		t.Error("role validation")
	}
}
