package review

import (
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/annotation"
	"github.com/kshitijdhara/burntout/internal/domain/boundary"
	"github.com/kshitijdhara/burntout/internal/domain/features"
	"github.com/kshitijdhara/burntout/internal/domain/model"
	"github.com/kshitijdhara/burntout/internal/domain/strain"
)

// Adherence is how one active boundary was kept over the week. Each day is
// judged by the rule of its kind that applied that day, so a rule changed
// mid-week still counts the days before the change.
type Adherence struct {
	Boundary boundary.Boundary // the active rule
	Kept     int               // days it was kept
	Judged   int               // days it applied and there was enough data
	Missed   []string          // days it wasn't kept, oldest first
	DaysOff  int               // days it applied but were marked vacation or sick
	NoData   int               // days it applied without enough data to tell
}

// Offer is a boundary the review suggests, tied to one of its suggestions.
type Offer struct {
	Boundary boundary.Boundary // kind and a starting value
	For      string            // the suggestion it goes with
}

// AddBoundaries judges the week's days against bs (every rule, active and
// past) and offers a boundary for suggestions whose kind has none. days
// are the recorded inputs of the week's days, any order; a day without
// any is missing, never kept. p are the feature definitions the score
// used.
func (w *Week) AddBoundaries(bs []boundary.Boundary, days []features.Day, p features.Params) {
	byDate := make(map[string]features.Day, len(days))
	for _, d := range days {
		byDate[d.Date] = d
	}
	from, _ := time.Parse(time.DateOnly, w.From)
	var dates []string
	for d := from; d.Format(time.DateOnly) <= w.To; d = d.AddDate(0, 0, 1) {
		dates = append(dates, d.Format(time.DateOnly))
	}
	off := annotation.Excluded(w.Annotations, dates)

	current := boundary.Current(bs)
	have := map[boundary.Kind]bool{}
	w.Adherence = nil
	for _, cur := range current {
		have[cur.Kind] = true
		a := Adherence{Boundary: cur}
		for _, date := range dates {
			rule, ok := ruleOn(bs, cur.Kind, date)
			if !ok || cur.Kind.Workdays() == weekend(date) {
				continue
			}
			if off[date] {
				a.DaysOff++
				continue
			}
			d, recorded := byDate[date]
			kept, judged := keeps(rule, d, recorded, p)
			switch {
			case !judged:
				a.NoData++
			case kept:
				a.Judged++
				a.Kept++
			default:
				a.Judged++
				a.Missed = append(a.Missed, date)
			}
		}
		w.Adherence = append(w.Adherence, a)
	}

	w.Offers = nil
	for i, name := range w.suggested {
		kind, ok := offerFor[name]
		if !ok || have[kind] {
			continue
		}
		have[kind] = true
		w.Offers = append(w.Offers, Offer{Boundary: starting(kind, p), For: w.Suggestions[i]})
	}
}

// ruleOn is the rule of kind that applied on date.
func ruleOn(bs []boundary.Boundary, kind boundary.Kind, date string) (boundary.Boundary, bool) {
	for _, b := range bs {
		if b.Kind == kind && b.ActiveOn(date) {
			return b, true
		}
	}
	return boundary.Boundary{}, false
}

func weekend(date string) bool {
	t, _ := time.Parse(time.DateOnly, date)
	return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday
}

// keeps judges one day. Shutdown and no-weekend are about work that didn't
// happen, so any recorded day can be judged: some use of the device and
// no work past the line is the rule kept. The meetings cap and the focus
// block need a day observed well enough to score (system design §7.1),
// or a barely used day would keep the cap and break the block.
func keeps(b boundary.Boundary, d features.Day, recorded bool, p features.Params) (kept, judged bool) {
	if !recorded || (len(d.Minutes) == 0 && d.Present == 0) {
		return false, false
	}
	grace := boundary.Grace.Seconds()
	switch b.Kind {
	case boundary.Shutdown:
		return workAfter(d, b.ShutdownAt()) <= grace, true
	case boundary.NoWeekend:
		return workAfter(d, 0) <= grace, true
	}
	sp := strain.DefaultParams()
	if d.Coverage < sp.MinCoverage || d.Present < sp.MinPresent {
		return false, false
	}
	name := "meeting_hours"
	if b.Kind == boundary.FocusBlock {
		name = "longest_focus_block"
	}
	f, _ := features.ByName(name)
	v, ok := f.Compute(d, p)
	if !ok {
		return false, false
	}
	if b.Kind == boundary.FocusBlock {
		return v >= b.Number(), true
	}
	return v <= b.Number(), true
}

// workAfter is the seconds of work (everything but media, as features
// count it) from at into the logical day's calendar date. Minutes after
// midnight belong to the day before, and are later than any shutdown.
func workAfter(d features.Day, at time.Duration) float64 {
	date, _ := time.Parse(time.DateOnly, d.Date)
	total := 0.0
	for _, m := range d.Minutes {
		start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, m.At.Location())
		if m.Category != model.CategoryMedia && m.At.Sub(start) >= at {
			total += m.AttendedS
		}
	}
	return total
}

// offerFor is the boundary that goes with a suggestion's driver or
// guardrail.
var offerFor = map[string]boundary.Kind{
	"after_hours_share":     boundary.Shutdown,
	"overnight_gap":         boundary.Shutdown,
	"after_hours_meetings":  boundary.Shutdown,
	"weekend_work":          boundary.NoWeekend,
	"meeting_hours":         boundary.MeetingsMax,
	"back_to_back_meetings": boundary.MeetingsMax,
	"switches_per_hour":     boundary.FocusBlock,
	"longest_focus_block":   boundary.FocusBlock,
}

// starting is an offered boundary's first value: a shutdown an hour after
// core hours end (not before boundary.EarliestShutdown), four hours of
// meetings, an hour of focus.
func starting(kind boundary.Kind, p features.Params) boundary.Boundary {
	value := ""
	switch kind {
	case boundary.Shutdown:
		at := min(max(p.CoreEnd+time.Hour, boundary.EarliestShutdown), 23*time.Hour+30*time.Minute)
		value = time.Time{}.Add(at).Format("15:04")
	case boundary.MeetingsMax:
		value = "4"
	case boundary.FocusBlock:
		value = "60"
	}
	b, _ := boundary.Parse(string(kind), value)
	return b
}
