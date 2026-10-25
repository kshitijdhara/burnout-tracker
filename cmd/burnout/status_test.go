package main

import (
	"strings"
	"testing"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/strain"
)

func TestFormatStrain(t *testing.T) {
	calibrating := []strain.Score{
		{Date: "2026-10-23", Status: strain.StatusCalibrating, UsableDays: 8},
		{Date: "2026-10-24", Status: strain.StatusCalibrating, UsableDays: 9},
	}
	scored := []strain.Score{
		{Date: "2026-10-22", Status: strain.StatusOK, Strain: 31},
		{Date: "2026-10-23", Status: strain.StatusMissing, Reason: "device barely used"},
		{Date: "2026-10-24", Status: strain.StatusOK, Strain: 74.4,
			Drivers:    []strain.Driver{{Label: "hours of attended work", HigherWorse: true, Deviation: 2.1}},
			Guardrails: []strain.Guardrail{{Name: "weekly_hours", Text: "57 h of attended work in the last 7 days (limit 55 h)"}}},
	}
	missingLast := scored[:2]
	notUsed := []strain.Score{{Date: "2026-10-24", Status: strain.StatusMissing, Reason: "device barely used"}}
	for _, tc := range []struct {
		name   string
		scores []strain.Score
		want   []string
		not    []string
	}{
		{"none", nil, []string{"no completed days"}, nil},
		{"calibrating", calibrating, []string{"calibrating, 9 of 14 days"}, []string{"/100"}},
		{"scored", scored, []string{
			"Strain on 2026-10-24: 74/100, high",
			"↑ hours of attended work: 2.1σ higher than usual lately",
			"! 57 h of attended work",
			"Last 7 days: Sun – Mon – Tue – Wed – Thu 31 Fri – Sat 74", // 18–24 Oct
			"not a diagnosis",
		}, nil},
		{"never usable", notUsed, []string{"calibrating, 0 of 14 days", "2026-10-24 didn't count: device barely used."}, nil},
		{"missing last", missingLast, []string{
			"no score for 2026-10-23 (device barely used). Last score: 31/100 on 2026-10-22, low",
		}, []string{"↑"}},
	} {
		got := formatStrain(tc.scores, "2026-10-25")
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q in:\n%s", tc.name, w, got)
			}
		}
		for _, n := range tc.not {
			if strings.Contains(got, n) {
				t.Errorf("%s: unexpected %q in:\n%s", tc.name, n, got)
			}
		}
	}
}
