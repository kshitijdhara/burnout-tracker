// Package review builds the weekly review (system design §8.2): the last
// seven completed days against the seven before, the drivers that kept
// coming up, the limits crossed, the check-in trend, and one or two
// suggestions tied to the top drivers. Pure: no I/O, no clock.
package review

import (
	"sort"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/annotation"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/checkin"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/strain"
)

// Week is the review of one week.
type Week struct {
	From, To string // logical days, inclusive

	Scored       int     // days with a strain score
	Average      float64 // mean strain over scored days
	PrevScored   int
	PrevAverage  float64
	Peak         strain.Score // the highest-strain day
	Calibrating  bool         // no day is scored yet
	CalibratedOf int          // usable days so far, while calibrating

	Drivers    []Recurring // drivers by how often they came up
	Guardrails []Recurring // limits crossed, by day count

	Checkin, PrevCheckin map[string]checkin.Record // weekly CBI results by instrument

	Annotations []annotation.Annotation // those overlapping the week

	Suggestions []string
}

// Recurring is a driver or guardrail and the days it appeared on.
type Recurring struct {
	Name  string // feature or guardrail name
	Label string
	Days  int
	// weight orders drivers that appear on the same number of days.
	weight float64
}

// Build reviews the 7 logical days before today. scores may cover any
// range; records are all check-ins (any order).
func Build(scores []strain.Score, records []checkin.Record, notes []annotation.Annotation, today string) Week {
	t, _ := time.Parse(time.DateOnly, today)
	day := func(back int) string { return t.AddDate(0, 0, -back).Format(time.DateOnly) }
	w := Week{From: day(7), To: day(1)}
	prevFrom := day(14)

	drivers := map[string]*Recurring{}
	guards := map[string]*Recurring{}
	var sum, prevSum float64
	for _, sc := range scores {
		switch {
		case sc.Date >= w.From && sc.Date <= w.To:
			w.CalibratedOf = max(w.CalibratedOf, sc.UsableDays)
			for _, g := range sc.Guardrails {
				count(guards, g.Name, g.Text, 0)
			}
			if sc.Status != strain.StatusOK {
				continue
			}
			w.Scored++
			sum += sc.Strain
			if sc.Strain > w.Peak.Strain {
				w.Peak = sc
			}
			for _, d := range sc.Drivers {
				count(drivers, d.Feature, d.Label, d.Contribution)
			}
		case sc.Date >= prevFrom && sc.Date < w.From && sc.Status == strain.StatusOK:
			w.PrevScored++
			prevSum += sc.Strain
		}
	}
	if w.Scored > 0 {
		w.Average = sum / float64(w.Scored)
	}
	if w.PrevScored > 0 {
		w.PrevAverage = prevSum / float64(w.PrevScored)
	}
	w.Calibrating = w.Scored == 0
	w.Drivers = sorted(drivers)
	w.Guardrails = sorted(guards)
	w.Checkin, w.PrevCheckin = latestWeekly(records)
	for _, a := range notes {
		if a.To >= w.From && a.From <= w.To {
			w.Annotations = append(w.Annotations, a)
		}
	}
	w.Suggestions = suggest(w.Drivers, w.Guardrails)
	return w
}

func count(m map[string]*Recurring, name, label string, weight float64) {
	r, ok := m[name]
	if !ok {
		r = &Recurring{Name: name, Label: label}
		m[name] = r
	}
	r.Days++
	r.weight += weight
}

func sorted(m map[string]*Recurring) []Recurring {
	out := make([]Recurring, 0, len(m))
	for _, r := range m {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Days != out[j].Days {
			return out[i].Days > out[j].Days
		}
		if out[i].weight != out[j].weight {
			return out[i].weight > out[j].weight
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// latestWeekly returns the most recent weekly check-in (both subscales,
// taken together) and the one before it.
func latestWeekly(records []checkin.Record) (latest, prev map[string]checkin.Record) {
	rs := make([]checkin.Record, 0, len(records))
	for _, r := range records {
		if r.Instrument == checkin.CBIPersonalID || r.Instrument == checkin.CBIWorkID {
			rs = append(rs, r)
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].At.After(rs[j].At) })
	// Both subscales of one check-in are saved with the same timestamp.
	var sessions []map[string]checkin.Record
	var at time.Time
	for _, r := range rs {
		if n := len(sessions); n > 0 && r.At.Equal(at) {
			if _, dup := sessions[n-1][r.Instrument]; !dup {
				sessions[n-1][r.Instrument] = r
				continue
			}
		}
		sessions = append(sessions, map[string]checkin.Record{r.Instrument: r})
		at = r.At
	}
	if len(sessions) > 0 {
		latest = sessions[0]
	}
	if len(sessions) > 1 {
		prev = sessions[1]
	}
	return latest, prev
}

// WeeklyDue reports whether a weekly check-in is due: none yet, or the last
// was at least 6 days before now, so it is due all day on the same weekday
// a week later. It also returns when the last one was.
func WeeklyDue(records []checkin.Record, now time.Time) (bool, time.Time) {
	var last time.Time
	for _, r := range records {
		if (r.Instrument == checkin.CBIPersonalID || r.Instrument == checkin.CBIWorkID) && r.At.After(last) {
			last = r.At
		}
	}
	return last.IsZero() || now.Sub(last) >= 6*24*time.Hour, last
}

// tips are suggestions tied to drivers and guardrails: concrete, small,
// and never shaming (system design §8.3). A mental-health professional
// reviews this copy before the alpha.
var tips = map[string]string{
	"after_hours_share":     "Work ran outside core hours more than usual. Try picking a shutdown time and stopping there for a week.",
	"attended_hours":        "Your days were longer than usual. Try protecting one lighter day this week.",
	"weekly_hours":          "This week went past 55 hours of work. Is there one thing you can move, delegate or drop?",
	"overnight_gap":         "The gap between your last and first work of the day was shorter than usual. Try ending work a little earlier on a few evenings.",
	"breaks":                "You took fewer breaks than usual. Try stepping away from the screen for a few minutes every hour or two.",
	"longest_break":         "Your breaks were shorter than usual. Try taking one proper break away from the screen each day.",
	"switches_per_hour":     "Your attention switched between apps more than usual. Try blocking an hour or two for focused work with chat silenced.",
	"longest_focus_block":   "Your focused stretches were shorter than usual. Try blocking an hour or two for focused work with chat silenced.",
	"comms_share":           "Chat and email took a bigger share of your time. Try checking messages at set times instead of as they arrive.",
	"meeting_hours":         "Meetings took more of your time than usual. Is there one recurring meeting you could shorten or skip?",
	"shell_failure_rate":    "More of your commands failed than usual. When you're stuck, a few minutes away can help more than another try.",
	"failure_loops":         "You hit the same failing command repeatedly. When you're stuck, a few minutes away can help more than another try.",
	"late_night_infra":      "You ran infrastructure or ssh commands late at night. If that's on-call work, it's worth noticing how often it happens.",
	"agent_parallelism":     "You ran more AI agents at once than usual. Fewer parallel sessions can mean less switching for you.",
	"agent_blocked_minutes": "Agents waited on you more than usual. Batching approvals, or fewer parallel sessions, can cut the interruptions.",
	"weekend_work":          "You worked on both weekend days. Try keeping at least one day fully off this week.",
}

// suggest picks up to two suggestions: guardrails first, then the most
// frequent drivers. Two items with the same advice yield it once.
func suggest(drivers, guards []Recurring) []string {
	var out []string
	add := func(name string) {
		tip, ok := tips[name]
		if !ok || len(out) == 2 {
			return
		}
		for _, have := range out {
			if have == tip {
				return
			}
		}
		out = append(out, tip)
	}
	for _, g := range guards {
		add(g.Name)
	}
	for _, d := range drivers {
		add(d.Name)
	}
	return out
}
