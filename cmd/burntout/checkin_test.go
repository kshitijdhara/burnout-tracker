package main

import (
	"bufio"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/boundary"
	"github.com/kshitijdhara/burntout/internal/domain/checkin"
	"github.com/kshitijdhara/burntout/internal/domain/review"
	"github.com/kshitijdhara/burntout/internal/domain/strain"
)

func TestAskCheckin(t *testing.T) {
	now := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	// 13 answers; the first has a typo and is asked again.
	input := "7\n" + strings.Repeat("2\n", 6) + strings.Repeat("3\n", 6) + "5\n"
	var out strings.Builder
	recs, err := askCheckin(bufio.NewReader(strings.NewReader(input)), &out, checkin.Weekly(), now)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(recs) != 2 || recs[0].Instrument != checkin.CBIPersonalID || recs[1].Instrument != checkin.CBIWorkID {
		t.Fatalf("records: %+v", recs)
	}
	// Personal: all "Often" (75). Work: six "Somewhat"/"Sometimes" (50) and
	// "Never" enough energy (0, reversed to 100).
	if recs[0].Score != 75 || recs[1].Score != (6*50+100)/7.0 || !recs[0].At.Equal(now) {
		t.Errorf("scores %v, %v", recs[0].Score, recs[1].Score)
	}
	got := out.String()
	for _, want := range []string{"13 questions", "1/13  How often do you feel tired?", "Please type a number from 1 to 5", "13/13"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	for name, in := range map[string]string{"q": "2\nq\n", "end of input": "2\n3\n"} {
		_, err := askCheckin(bufio.NewReader(strings.NewReader(in)), &strings.Builder{}, checkin.Weekly(), now)
		if !errors.Is(err, errQuit) {
			t.Errorf("%s: %v, want errQuit", name, err)
		}
	}
}

func TestCheckinResult(t *testing.T) {
	prev := []checkin.Record{{Instrument: checkin.CBIWorkID, Score: 55}}
	low := []checkin.Record{
		{Instrument: checkin.CBIPersonalID, Score: 40},
		{Instrument: checkin.CBIWorkID, Score: 60},
	}
	got := checkinResult(low, prev, "IN")
	for _, want := range []string{"Personal burnout: 40/100 (low)", "Work-related burnout: 60/100 (moderate), last time 55", "burntout review"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Tele-MANAS") {
		t.Errorf("support shown for a moderate result:\n%s", got)
	}
	high := []checkin.Record{{Instrument: checkin.CBIPersonalID, Score: 40}, {Instrument: checkin.CBIWorkID, Score: 79}}
	got = checkinResult(high, nil, "IN")
	for _, want := range []string{"(high)", "you don't have to sort it out alone", "Tele-MANAS: call 14416", "findahelpline.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if got := checkinResult([]checkin.Record{{Instrument: checkin.DailyID, Score: 100}}, nil, ""); strings.Contains(got, "alone") {
		t.Errorf("support shown for a daily check-in:\n%s", got)
	}
}

func TestFormatReview(t *testing.T) {
	w := review.Week{
		From: "2026-10-19", To: "2026-10-25", Scored: 5, Average: 52, PrevScored: 6, PrevAverage: 40,
		Peak:        strain.Score{Date: "2026-10-22", Strain: 71},
		Drivers:     []review.Recurring{{Name: "after_hours_share", Label: "share of work outside core hours", Days: 3}},
		Guardrails:  []review.Recurring{{Name: "overnight_gap", Label: "only 6.0 h between …", Days: 1}},
		Checkin:     map[string]checkin.Record{checkin.CBIWorkID: {Instrument: checkin.CBIWorkID, Score: 61}},
		Suggestions: []string{"Try picking a shutdown time."},
		Adherence: []review.Adherence{
			{Boundary: boundary.Boundary{Kind: boundary.Shutdown, Value: "21:30", Since: "2026-10-01"},
				Kept: 3, Judged: 4, Missed: []string{"2026-10-21"}, DaysOff: 1},
			{Boundary: boundary.Boundary{Kind: boundary.NoWeekend, Since: "2026-10-01"}, NoData: 2},
			{Boundary: boundary.Boundary{Kind: boundary.FocusBlock, Value: "90", Since: "2026-10-27"}},
		},
		Offers: []review.Offer{{Boundary: boundary.Boundary{Kind: boundary.MeetingsMax, Value: "4"}, For: "Try picking a shutdown time."}},
	}
	got := formatReview(w, true)
	for _, want := range []string{
		"Week of 2026-10-19 to 2026-10-25",
		"Strain: 52 on average over 5 scored days (the week before: 40, +12)",
		"Highest: 2026-10-22, 71 (high)",
		"↑ share of work outside core hours (3 days)",
		"! only 6.0 h between … (1 day)",
		"work-related burnout 61 (moderate).",
		"This week's is due",
		"• Try picking a shutdown time.\n    A boundary for it: `burntout boundary add meetings-max 4`",
		"Your boundaries:\n  • Stop work by 21:30 on workdays: kept on 3 of 4 days (not on Wed 21 Oct); 1 day off left out",
		"• No work at the weekend: no days to go by yet (2 days without enough data)",
		"• A focus block of at least 90 minutes on workdays: new, it applies from 2026-10-27",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	got = formatReview(review.Week{From: "a", To: "b", Calibrating: true, CalibratedOf: 4}, false)
	if !strings.Contains(got, "still calibrating (4 of 14") || !strings.Contains(got, "Check-in: none yet.") {
		t.Errorf("calibrating review:\n%s", got)
	}
}
