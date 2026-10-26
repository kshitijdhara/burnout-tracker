package dashboard

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestPreview serves the dashboard with made-up data for working on the
// page: BURNOUT_DASHBOARD_PREVIEW=60s go test -run TestPreview -v
// ./internal/adapters/dashboard/ prints a one-time link and serves for that
// long. It is skipped otherwise.
func TestPreview(t *testing.T) {
	d, err := time.ParseDuration(os.Getenv("BURNOUT_DASHBOARD_PREVIEW"))
	if err != nil {
		t.Skip("set BURNOUT_DASHBOARD_PREVIEW to a duration to serve a preview")
	}
	s, err := Listen(previewSource{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(s.LaunchURL())
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if err := s.Serve(ctx); err != nil {
		t.Fatal(err)
	}
}

type previewSource struct{}

const previewToday = "2026-10-26"

func (previewSource) Overview(context.Context) (Overview, error) {
	end, _ := time.Parse(time.DateOnly, previewToday)
	var days []DayScore
	for back := 90; back >= 1; back-- {
		date := end.AddDate(0, 0, -back).Format(time.DateOnly)
		switch {
		case back > 78:
			days = append(days, DayScore{Date: date, Status: "calibrating"})
		case back == 40 || back == 41:
			days = append(days, DayScore{Date: date, Status: "missing", Reason: "vacation", Marked: "vacation, 2026-09-15 to 2026-09-16 (2 days)"})
		default:
			v := 30 + 25*math.Sin(float64(back)/9) + float64(30-min(back, 30))*1.2
			v = math.Max(5, math.Min(95, v))
			ds := DayScore{Date: date, Status: "ok", Strain: &v, Band: band(v)}
			if back == 1 {
				ds.Drivers = []string{"hours of attended work higher than usual lately", "app switches per hour higher than usual lately"}
				ds.Guardrails = []string{"57 h of attended work in the last 7 days (limit 55 h)"}
			}
			days = append(days, ds)
		}
	}
	latest := days[len(days)-1]
	return Overview{
		Today: previewToday, Profile: "developer, core hours 09:00-18:00", Days: days, Latest: &latest,
		Notices: []string{"Disk encryption is off: your data is unprotected if this device is lost or stolen."},
	}, nil
}

func band(v float64) string {
	switch {
	case v >= 70:
		return "high"
	case v >= 40:
		return "elevated"
	}
	return "low"
}

func (previewSource) Day(context.Context, string) (Day, error) {
	cats := []string{"code", "terminal", "browser", "comms", "docs", "meeting"}
	var timeline []SlotEntry
	for m := 9 * 60; m < 19*60; m += 10 {
		if m >= 12*60+30 && m < 13*60+20 {
			continue // lunch
		}
		timeline = append(timeline, SlotEntry{
			Start: fmt.Sprintf("%02d:%02d", m/60, m%60), Category: cats[(m/37)%len(cats)], AttendedS: 420 + float64(m%180),
		})
	}
	return Day{
		Date: previewToday, AttendedS: 6.6 * 3600,
		Categories: []Named{{"code", 9800}, {"terminal", 5200}, {"browser", 4100}, {"comms", 2600}, {"meeting", 1500}, {"docs", 600}},
		TopApps:    []Named{{"com.microsoft.VSCode", 9800}, {"com.mitchellh.ghostty", 5200}, {"com.google.Chrome", 4100}, {"com.tinyspeck.slackmacgap", 2600}, {"us.zoom.xos", 1500}},
		Commands:   184, Failures: 23,
		TopCommands: []Named{{"git", 61}, {"go", 48}, {"make", 20}, {"kubectl", 12}, {"ssh", 4}},
		Agents:      2, AgentStates: []Named{{"working", 7200}, {"blocked_on_human", 1100}, {"idle", 900}},
		HumanStates: []Named{{"reading", 2400}, {"composing", 900}, {"approving", 300}, {"elsewhere", 3000}},
		Meetings:    []Meeting{{"10:00", "10:15", false}, {"14:00", "15:00", false}, {"15:00", "15:30", true}},
		Timeline:    timeline,
	}, nil
}

func (previewSource) Week(context.Context) (Week, error) {
	avg, prev, p := 52.0, 41.0, 68.0
	return Week{
		From: "2026-10-19", To: "2026-10-25", Scored: 6, Average: &avg, PrevAverage: &prev, Peak: &Peak{"2026-10-23", 68, "elevated"},
		Drivers:     []Count{{"hours of attended work", 4}, {"app switches per hour", 3}, {"back-to-back meetings", 2}},
		Limits:      []Count{{"57 h of attended work in the last 7 days", 1}},
		Checkins:    []Check{{"Work-related burnout", 54, "moderate", &p}, {"Personal burnout", 46, "low", nil}},
		Marked:      []string{"on-call, 2026-10-20 to 2026-10-22 (3 days)"},
		Suggestions: []string{"This week went past 55 hours of work. Is there one thing you can move, delegate or drop?"},
	}, nil
}

func (previewSource) Privacy(context.Context) (Privacy, error) {
	return Privacy{
		Collected: []string{"Which apps and windows hold your attention, per minute. Window titles are not collected."},
		Settings: []Setting{
			{"Consent", "given"}, {"Profile", "developer, core hours 09:00-18:00"}, {"Collection", "running"},
			{"Calendar", "from calendar.google.com, last synced Oct 26 09:12 (" + strconv.Itoa(14) + " meetings)"},
			{"Data folder", "/Users/someone/Library/Application Support/burnout (owner-only; disk encryption enabled)"},
		},
	}, nil
}
