package review

import (
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/checkin"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/features"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/strain"
)

const today = "2026-10-26" // reviews 19–25 Oct, against 12–18 Oct

func ok(date string, v float64, drivers ...string) strain.Score {
	sc := strain.Score{Date: date, Status: strain.StatusOK, Strain: v, UsableDays: 30}
	for _, d := range drivers {
		sc.Drivers = append(sc.Drivers, strain.Driver{Feature: d, Label: d + " label", Contribution: 1})
	}
	return sc
}

func TestBuild(t *testing.T) {
	scores := []strain.Score{
		ok("2026-10-12", 30), ok("2026-10-15", 40), // previous week: 35
		ok("2026-10-19", 50, "after_hours_share"),
		ok("2026-10-20", 60, "after_hours_share", "breaks"),
		{Date: "2026-10-21", Status: strain.StatusMissing, Reason: "device barely used",
			Guardrails: []strain.Guardrail{{Name: "overnight_gap", Text: "short night"}}},
		ok("2026-10-22", 70, "after_hours_share", "meeting_hours"),
		ok("2026-10-25", 40),
		ok("2026-10-26", 99), // today: not in the review
	}
	scores[3].Guardrails = []strain.Guardrail{{Name: "weekly_hours", Text: "57 h"}}
	scores[5].Guardrails = []strain.Guardrail{{Name: "weekly_hours", Text: "58 h"}}
	w := Build(scores, nil, nil, today)
	if w.From != "2026-10-19" || w.To != "2026-10-25" {
		t.Fatalf("range %s–%s", w.From, w.To)
	}
	if w.Scored != 4 || w.Average != 55 || w.PrevScored != 2 || w.PrevAverage != 35 || w.Peak.Date != "2026-10-22" {
		t.Errorf("week: %+v", w)
	}
	if len(w.Drivers) != 3 || w.Drivers[0].Name != "after_hours_share" || w.Drivers[0].Days != 3 {
		t.Errorf("drivers: %+v", w.Drivers)
	}
	if len(w.Guardrails) != 2 || w.Guardrails[0].Name != "weekly_hours" || w.Guardrails[0].Days != 2 {
		t.Errorf("guardrails: %+v", w.Guardrails)
	}
	// Guardrails come first (most days first), then drivers.
	if len(w.Suggestions) != 2 || w.Suggestions[0] != tips["weekly_hours"] || w.Suggestions[1] != tips["overnight_gap"] {
		t.Errorf("suggestions: %q", w.Suggestions)
	}
	// Without guardrails, the most frequent driver leads.
	for i := range scores {
		scores[i].Guardrails = nil
	}
	if got := Build(scores, nil, nil, today).Suggestions; len(got) != 2 || got[0] != tips["after_hours_share"] {
		t.Errorf("driver suggestions: %q", got)
	}
}

func TestBuildWhileCalibrating(t *testing.T) {
	w := Build([]strain.Score{{Date: "2026-10-24", Status: strain.StatusCalibrating, UsableDays: 6}}, nil, nil, today)
	if !w.Calibrating || w.CalibratedOf != 6 || len(w.Suggestions) != 0 {
		t.Errorf("week: %+v", w)
	}
}

func TestLatestWeeklyCheckins(t *testing.T) {
	at := func(d int) time.Time { return time.Date(2026, 10, d, 20, 0, 0, 0, time.UTC) }
	rec := func(d int, id string, score float64) checkin.Record {
		return checkin.Record{At: at(d), Instrument: id, Score: score}
	}
	records := []checkin.Record{
		rec(11, checkin.CBIPersonalID, 40), rec(11, checkin.CBIWorkID, 45),
		rec(18, checkin.CBIPersonalID, 50), rec(18, checkin.CBIWorkID, 55),
		rec(19, checkin.DailyID, 75), // not weekly
		rec(25, checkin.CBIPersonalID, 60), rec(25, checkin.CBIWorkID, 70),
	}
	w := Build(nil, records, nil, today)
	if w.Checkin[checkin.CBIWorkID].Score != 70 || w.Checkin[checkin.CBIPersonalID].Score != 60 ||
		w.PrevCheckin[checkin.CBIWorkID].Score != 55 || len(w.PrevCheckin) != 2 {
		t.Errorf("check-ins: latest %+v, previous %+v", w.Checkin, w.PrevCheckin)
	}

	due, last := WeeklyDue(records, at(31))
	if !due || !last.Equal(at(25)) {
		t.Errorf("due after 6 days? %v (last %v)", due, last)
	}
	if due, _ := WeeklyDue(records, at(30)); due {
		t.Error("due after 5 days")
	}
	if due, _ := WeeklyDue(nil, at(30)); !due {
		t.Error("not due with no check-ins")
	}
}

func TestEverySuggestionTargetsAKnownName(t *testing.T) {
	known := map[string]bool{"weekly_hours": true, "overnight_gap": true, "weekend_work": true} // guardrails
	for _, f := range features.Registry() {
		known[f.Name] = true
	}
	for name, tip := range tips {
		if !known[name] || strings.TrimSpace(tip) == "" {
			t.Errorf("tip for unknown feature or guardrail %q", name)
		}
	}
}
