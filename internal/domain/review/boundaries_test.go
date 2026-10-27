package review

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/annotation"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/boundary"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/features"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// The reviewed week, 19–25 Oct 2026, is Monday to Sunday.

func rule(kind, value, since, until string) boundary.Boundary {
	b, err := boundary.Parse(kind, value)
	if err != nil {
		panic(err)
	}
	b.Since, b.Until = since, until
	return b
}

// worked is a well-observed day with cat minutes from "HH:MM" to "HH:MM"
// (an end past 24:00 runs into the night).
func worked(date string, cat model.Category, spans ...[2]string) features.Day {
	d, _ := time.Parse(time.DateOnly, date)
	day := features.Day{Date: date, Weekend: weekend(date), Coverage: 1}
	clock := func(s string) time.Duration {
		var h, m int
		if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
			panic(err)
		}
		return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute
	}
	midnight := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local)
	for _, sp := range spans {
		for at := midnight.Add(clock(sp[0])); at.Before(midnight.Add(clock(sp[1]))); at = at.Add(time.Minute) {
			day.Minutes = append(day.Minutes, features.Minute{At: at, Label: "app", Category: cat, AttendedS: 60})
			day.Present += time.Minute
		}
	}
	return day
}

func adherence(t *testing.T, w Week, kind boundary.Kind) Adherence {
	t.Helper()
	for _, a := range w.Adherence {
		if a.Boundary.Kind == kind {
			return a
		}
	}
	t.Fatalf("no adherence for %s in %+v", kind, w.Adherence)
	return Adherence{}
}

func TestAdherenceWithoutData(t *testing.T) {
	w := Build(nil, nil, nil, today)
	bs := []boundary.Boundary{rule("shutdown", "21:30", "2026-10-01", ""), rule("no-weekend", "", "2026-10-01", "")}
	w.AddBoundaries(bs, nil, features.DefaultParams())
	if a := adherence(t, w, boundary.Shutdown); a.Judged != 0 || a.Kept != 0 || a.NoData != 5 {
		t.Errorf("shutdown: %+v", a)
	}
	if a := adherence(t, w, boundary.NoWeekend); a.Judged != 0 || a.NoData != 2 {
		t.Errorf("no weekend: %+v", a)
	}
	// No rules, nothing to judge.
	w.AddBoundaries(nil, nil, features.DefaultParams())
	if len(w.Adherence) != 0 {
		t.Errorf("%+v", w.Adherence)
	}
}

func TestShutdownAdherence(t *testing.T) {
	days := []features.Day{
		worked("2026-10-19", model.CategoryCode, [2]string{"09:00", "21:00"}),                              // kept
		worked("2026-10-20", model.CategoryCode, [2]string{"09:00", "21:40"}),                              // 10 min over: grace
		worked("2026-10-21", model.CategoryCode, [2]string{"09:00", "22:00"}),                              // 30 min over
		worked("2026-10-22", model.CategoryCode, [2]string{"09:00", "17:00"}, [2]string{"24:10", "24:40"}), // past midnight
		worked("2026-10-23", model.CategoryCode, [2]string{"09:00", "17:00"}),
		worked("2026-10-24", model.CategoryCode, [2]string{"22:00", "23:00"}), // weekend: not a workday
	}
	films := worked("2026-10-23", model.CategoryMedia, [2]string{"21:30", "23:30"})
	days[4].Minutes = append(days[4].Minutes, films.Minutes...) // media isn't work
	w := Build(nil, nil, nil, today)
	w.AddBoundaries([]boundary.Boundary{rule("shutdown", "21:30", "2026-10-01", "")}, days, features.DefaultParams())
	a := adherence(t, w, boundary.Shutdown)
	if a.Kept != 3 || a.Judged != 5 || !slices.Equal(a.Missed, []string{"2026-10-21", "2026-10-22"}) || a.NoData != 0 {
		t.Errorf("%+v", a)
	}
}

func TestAdherencePartialWeekAndChanges(t *testing.T) {
	late := func(date string) features.Day {
		return worked(date, model.CategoryCode, [2]string{"10:00", "21:00"})
	}
	days := []features.Day{late("2026-10-19"), late("2026-10-20"), late("2026-10-21"), late("2026-10-22"), late("2026-10-23")}

	// Set on Wednesday: it applies from Thursday.
	w := Build(nil, nil, nil, today)
	w.AddBoundaries([]boundary.Boundary{rule("shutdown", "22:00", "2026-10-22", "")}, days, features.DefaultParams())
	if a := adherence(t, w, boundary.Shutdown); a.Judged != 2 || a.Kept != 2 || a.NoData != 0 {
		t.Errorf("partial week: %+v", a)
	}

	// Changed on Wednesday from 22:00 to 20:00: Monday and Tuesday by the
	// old rule, Wednesday by neither, Thursday and Friday by the new.
	bs := []boundary.Boundary{rule("shutdown", "22:00", "2026-10-15", "2026-10-21"), rule("shutdown", "20:00", "2026-10-22", "")}
	w.AddBoundaries(bs, days, features.DefaultParams())
	a := adherence(t, w, boundary.Shutdown)
	if a.Boundary.Value != "20:00" || a.Judged != 4 || a.Kept != 2 || !slices.Equal(a.Missed, []string{"2026-10-22", "2026-10-23"}) {
		t.Errorf("changed: %+v", a)
	}

	// Removed: no adherence shown.
	w.AddBoundaries([]boundary.Boundary{rule("shutdown", "22:00", "2026-10-15", "2026-10-21")}, days, features.DefaultParams())
	if len(w.Adherence) != 0 {
		t.Errorf("removed: %+v", w.Adherence)
	}
}

func TestAdherenceLeavesOutDaysOff(t *testing.T) {
	notes := []annotation.Annotation{
		{From: "2026-10-20", To: "2026-10-21", Kind: annotation.Vacation},
		{From: "2026-10-22", To: "2026-10-22", Kind: annotation.OnCall}, // on call is judged
		{From: "2026-10-24", To: "2026-10-24", Kind: annotation.Sick},
	}
	days := []features.Day{
		worked("2026-10-20", model.CategoryCode, [2]string{"20:00", "23:00"}),
		worked("2026-10-22", model.CategoryCode, [2]string{"20:00", "23:00"}),
		worked("2026-10-24", model.CategoryCode, [2]string{"10:00", "14:00"}),
		worked("2026-10-25", model.CategoryCode, [2]string{"10:00", "10:10"}), // a glance
	}
	w := Build(nil, nil, notes, today)
	bs := []boundary.Boundary{rule("shutdown", "21:00", "2026-10-01", ""), rule("no-weekend", "", "2026-10-01", "")}
	w.AddBoundaries(bs, days, features.DefaultParams())
	if a := adherence(t, w, boundary.Shutdown); a.DaysOff != 2 || a.Judged != 1 || a.Kept != 0 || a.NoData != 2 {
		t.Errorf("shutdown: %+v", a)
	}
	if a := adherence(t, w, boundary.NoWeekend); a.DaysOff != 1 || a.Judged != 1 || a.Kept != 1 {
		t.Errorf("no weekend: %+v", a)
	}
}

func TestFocusAndMeetingAdherence(t *testing.T) {
	focused := worked("2026-10-19", model.CategoryCode, [2]string{"09:00", "11:00"})
	focused.Minutes = append(focused.Minutes, worked("2026-10-19", model.CategoryMeeting, [2]string{"13:00", "18:00"}).Minutes...)
	focused.Present = 7 * time.Hour
	short := worked("2026-10-20", model.CategoryCode, [2]string{"09:00", "09:45"}, [2]string{"10:00", "11:00"})
	short.Present = 2 * time.Hour
	barely := worked("2026-10-21", model.CategoryCode, [2]string{"09:00", "12:00"})
	barely.Coverage = 0.3 // too little observed to judge
	w := Build(nil, nil, nil, today)
	bs := []boundary.Boundary{rule("focus-block", "90", "2026-10-01", ""), rule("meetings-max", "4", "2026-10-01", "")}
	w.AddBoundaries(bs, []features.Day{focused, short, barely}, features.DefaultParams())
	if a := adherence(t, w, boundary.FocusBlock); a.Judged != 2 || a.Kept != 1 || !slices.Equal(a.Missed, []string{"2026-10-20"}) || a.NoData != 3 {
		t.Errorf("focus: %+v", a)
	}
	if a := adherence(t, w, boundary.MeetingsMax); a.Judged != 2 || a.Kept != 1 || !slices.Equal(a.Missed, []string{"2026-10-19"}) {
		t.Errorf("meetings: %+v", a)
	}
}

func TestOffers(t *testing.T) {
	w := Build(nil, nil, nil, today)
	w.Suggestions = []string{tips["after_hours_share"], tips["comms_share"]}
	w.suggested = []string{"after_hours_share", "comms_share"}
	w.AddBoundaries(nil, nil, features.DefaultParams())
	if len(w.Offers) != 1 || w.Offers[0].Boundary.Kind != boundary.Shutdown || w.Offers[0].Boundary.Value != "19:00" ||
		w.Offers[0].For != tips["after_hours_share"] {
		t.Errorf("offers: %+v", w.Offers)
	}
	// Not when the kind has a rule already.
	w.AddBoundaries([]boundary.Boundary{rule("shutdown", "22:00", "2026-10-30", "")}, nil, features.DefaultParams())
	if len(w.Offers) != 0 {
		t.Errorf("offered an existing kind: %+v", w.Offers)
	}
	for name, kind := range offerFor {
		if _, ok := tips[name]; !ok || starting(kind, features.DefaultParams()).Validate() != nil {
			t.Errorf("offer for %s", name)
		}
	}
}
