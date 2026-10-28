// Package strain computes the cold-start strain score from daily features
// (system design §7.2): each feature is compared with your own recent
// weekdays or weekends, deviations are smoothed and summed by group with
// caps, absolute guardrails add to it, and the sum maps to 0–100. Pure: no
// I/O, no clock.
package strain

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/features"
)

// ModelVersion names this scoring method in stored scores.
const ModelVersion = "strain-v1"

// Status says whether a day has a score.
type Status string

// Statuses (system design §6).
const (
	StatusOK          Status = "ok"
	StatusMissing     Status = "missing"     // too little data: never treated as rest
	StatusCalibrating Status = "calibrating" // fewer than CalibrationDays usable days so far
)

// Day is one logical day's features and how much of it was observed.
type Day struct {
	Date     string // YYYY-MM-DD
	Coverage float64
	Present  time.Duration
	Values   map[string]float64
	// Excluded marks an annotated vacation or sick day: kept out of
	// baselines and not scored.
	Excluded bool
}

// Params configure the score. DefaultParams has the design values.
type Params struct {
	Role            features.Role
	CalibrationDays int           // usable days before the first score
	BaselineDays    int           // trailing window for baselines, calendar days
	MinBaseline     [2]int        // fewest same-type days for a baseline: weekday, weekend
	MinCoverage     float64       // below this a day is missing
	MinPresent      time.Duration // below this a day is missing (device not used)
	ZClip           float64
	EWMAHalfLife    float64 // smoothing half-life, days
	GroupCap        float64 // per group, both directions
	C0, S           float64 // strain = 100 / (1 + exp(-(c - C0)/S))

	AnchorDays     int // length of the anchor window
	AnchorLookback int // how far back the anchor window may start, days
	AnchorMinDays  int // scored days an anchor window needs

	WeeklyHours      float64       // guardrail: attended work in 7 days
	MinOvernight     time.Duration // guardrail: overnight gap
	WeekendHours     float64       // guardrail: both weekend days above this
	WeeklyPenalty    float64
	OvernightPenalty float64
	WeekendPenalty   float64
}

// DefaultParams returns the values from the design.
func DefaultParams() Params {
	return Params{
		Role:             features.RoleDeveloper,
		CalibrationDays:  14,
		BaselineDays:     28,
		MinBaseline:      [2]int{5, 2},
		MinCoverage:      0.6,
		MinPresent:       30 * time.Minute,
		ZClip:            4,
		EWMAHalfLife:     7,
		GroupCap:         2,
		C0:               3,
		S:                1.25,
		AnchorDays:       28,
		AnchorLookback:   180,
		AnchorMinDays:    14,
		WeeklyHours:      55,
		MinOvernight:     7 * time.Hour,
		WeekendHours:     2,
		WeeklyPenalty:    1.5,
		OvernightPenalty: 1,
		WeekendPenalty:   1,
	}
}

// minDriver is the smallest contribution worth naming: half a robust SD
// at full weight. Smaller deviations are everyday noise.
const minDriver = 0.5

// Driver is a feature pushing the score up or down.
type Driver struct {
	Feature      string
	Label        string
	HigherWorse  bool
	Deviation    float64 // smoothed deviation in robust SDs, positive = worse
	Contribution float64 // weighted deviation
}

// Text explains the driver in plain language, for example "hours of
// attended work: 2.1σ higher than usual lately". The deviation is smoothed
// over recent days, each compared with your usual weekdays or weekends.
func (d Driver) Text() string {
	dir := "higher"
	if (d.Deviation > 0) != d.HigherWorse {
		dir = "lower"
	}
	return fmt.Sprintf("%s: %.1fσ %s than usual lately", d.Label, math.Abs(d.Deviation), dir)
}

// Guardrail is an absolute limit the day crossed.
type Guardrail struct {
	Name    string
	Text    string
	Penalty float64
}

// Score is one day's result.
type Score struct {
	Date       string
	Status     Status
	Reason     string // why a day is missing
	Strain     float64
	Composite  float64
	Groups     map[features.Group]float64
	Drivers    []Driver // top contributors, worst first
	Guardrails []Guardrail
	UsableDays int // usable days up to and including this one
}

// Compute scores every day in days (any order; dates may be missing, and
// a missing date is never treated as rest). Each day only sees the days
// before it, so a past score never changes when new days arrive.
func Compute(days []Day, p Params) []Score {
	days = slices.Clone(days)
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	reg := features.Registry()
	s := scorer{p: p, reg: reg, ewma: map[string]float64{}, byDate: map[string]int{}}
	out := make([]Score, 0, len(days))
	for i, d := range days {
		s.byDate[d.Date] = i
		sc := s.score(days, i)
		s.scores = append(s.scores, sc)
		out = append(out, sc)
	}
	return out
}

type scorer struct {
	p      Params
	reg    []features.Feature
	ewma   map[string]float64 // feature → smoothed deviation
	usable int
	byDate map[string]int
	scores []Score
}

func (s *scorer) usableDay(d Day) (bool, string) {
	switch {
	case d.Excluded:
		return false, "vacation or sick day"
	case d.Present < s.p.MinPresent:
		return false, "device barely used"
	case d.Coverage < s.p.MinCoverage:
		return false, fmt.Sprintf("only %.0f%% of the day observed", d.Coverage*100)
	}
	return true, ""
}

func (s *scorer) score(days []Day, i int) Score {
	d := days[i]
	sc := Score{Date: d.Date, Groups: map[features.Group]float64{}}
	sc.Guardrails = s.guardrails(days, i)
	ok, reason := s.usableDay(d)
	if !ok {
		sc.Status, sc.Reason, sc.UsableDays = StatusMissing, reason, s.usable
		return sc
	}
	s.usable++
	sc.UsableDays = s.usable

	date := mustDate(d.Date)
	weekend := isWeekend(date)
	anchor := s.anchorWindow(date)
	alpha := 1 - math.Pow(0.5, 1/s.p.EWMAHalfLife) // today's weight
	var drivers []Driver
	for _, f := range s.reg {
		x, has := d.Values[f.Name]
		if !has {
			continue
		}
		med, mad, ok := s.baseline(days, i, f.Name, weekend, date.AddDate(0, 0, -s.p.BaselineDays), date)
		if !ok {
			continue
		}
		if anchor != nil {
			if amed, amad, ok := s.baseline(days, i, f.Name, weekend, anchor[0], anchor[1]); ok {
				med, mad = (med+amed)/2, (mad+amad)/2
			}
		}
		z := (x - med) / math.Max(1.4826*mad, f.Epsilon)
		if !f.HigherWorse {
			z = -z
		}
		z = math.Max(-s.p.ZClip, math.Min(s.p.ZClip, z))
		e, seen := s.ewma[f.Name]
		if !seen {
			e = z
		} else {
			e = alpha*z + (1-alpha)*e
		}
		s.ewma[f.Name] = e
		w := f.Weight(s.p.Role)
		if w == 0 {
			continue
		}
		sc.Groups[f.Group] += w * e
		drivers = append(drivers, Driver{
			Feature: f.Name, Label: f.Label, HigherWorse: f.HigherWorse,
			Deviation: e, Contribution: w * e,
		})
	}
	// Summed in a fixed order, so the same input always gives exactly the
	// same score.
	for _, g := range slices.Sorted(maps.Keys(sc.Groups)) {
		sc.Groups[g] = math.Max(-s.p.GroupCap, math.Min(s.p.GroupCap, sc.Groups[g]))
		sc.Composite += sc.Groups[g]
	}
	for _, g := range sc.Guardrails {
		sc.Composite += g.Penalty
	}
	sort.Slice(drivers, func(a, b int) bool { return drivers[a].Contribution > drivers[b].Contribution })
	for _, dr := range drivers {
		if len(sc.Drivers) == 3 || dr.Contribution < minDriver {
			break
		}
		sc.Drivers = append(sc.Drivers, dr)
	}
	if s.usable < s.p.CalibrationDays {
		sc.Status = StatusCalibrating
		return sc
	}
	sc.Status = StatusOK
	sc.Strain = 100 / (1 + math.Exp(-(sc.Composite-s.p.C0)/s.p.S))
	return sc
}

// baseline returns the median and MAD of feature over usable days of the
// same type (weekday or weekend) in [from, to), before day i.
func (s *scorer) baseline(days []Day, i int, feature string, weekend bool, from, to time.Time) (med, mad float64, ok bool) {
	var xs []float64
	lo, hi := from.Format(time.DateOnly), to.Format(time.DateOnly)
	for j := i - 1; j >= 0 && days[j].Date >= lo; j-- {
		d := days[j]
		if d.Date >= hi || isWeekend(mustDate(d.Date)) != weekend {
			continue
		}
		if ok, _ := s.usableDay(d); !ok {
			continue
		}
		if v, has := d.Values[feature]; has {
			xs = append(xs, v)
		}
	}
	need := s.p.MinBaseline[0]
	if weekend {
		need = s.p.MinBaseline[1]
	}
	if len(xs) < need {
		return 0, 0, false
	}
	med = median(xs)
	dev := make([]float64, len(xs))
	for k, x := range xs {
		dev[k] = math.Abs(x - med)
	}
	return med, median(dev), true
}

// anchorWindow returns [from, to) of the calmest earlier window: the
// AnchorDays-long window, ending before the current baseline window and
// starting within AnchorLookback days, with the lowest mean composite over
// at least AnchorMinDays scored days. Nil until such a window exists.
func (s *scorer) anchorWindow(date time.Time) *[2]time.Time {
	type point struct {
		date      time.Time
		composite float64
	}
	var pts []point
	earliest := date.AddDate(0, 0, -s.p.AnchorLookback)
	latestEnd := date.AddDate(0, 0, -s.p.BaselineDays)
	for _, sc := range s.scores {
		t := mustDate(sc.Date)
		if sc.Status == StatusOK && !t.Before(earliest) && t.Before(latestEnd) {
			pts = append(pts, point{t, sc.Composite})
		}
	}
	if len(pts) < s.p.AnchorMinDays {
		return nil
	}
	var best *[2]time.Time
	bestMean := math.Inf(1)
	for k := range pts {
		start := pts[k].date
		end := start.AddDate(0, 0, s.p.AnchorDays)
		if end.After(latestEnd) {
			break
		}
		sum, n := 0.0, 0
		for _, q := range pts[k:] {
			if !q.date.Before(end) {
				break
			}
			sum += q.composite
			n++
		}
		if n >= s.p.AnchorMinDays && sum/float64(n) < bestMean {
			bestMean = sum / float64(n)
			best = &[2]time.Time{start, end}
		}
	}
	return best
}

func (s *scorer) guardrails(days []Day, i int) []Guardrail {
	d := days[i]
	date := mustDate(d.Date)
	var out []Guardrail
	weekly := 0.0
	from := date.AddDate(0, 0, -6).Format(time.DateOnly)
	for j := i; j >= 0 && days[j].Date >= from; j-- {
		weekly += days[j].Values["attended_hours"]
	}
	if weekly >= s.p.WeeklyHours {
		out = append(out, Guardrail{"weekly_hours",
			fmt.Sprintf("%.0f h of attended work in the last 7 days (limit %.0f h)", weekly, s.p.WeeklyHours), s.p.WeeklyPenalty})
	}
	if gap, ok := d.Values["overnight_gap"]; ok && gap < s.p.MinOvernight.Hours() {
		out = append(out, Guardrail{"overnight_gap",
			fmt.Sprintf("only %.1f h between yesterday's last activity and today's first (limit %.0f h)", gap, s.p.MinOvernight.Hours()), s.p.OvernightPenalty})
	}
	if date.Weekday() == time.Sunday {
		sat, ok := s.byDate[date.AddDate(0, 0, -1).Format(time.DateOnly)]
		if ok && days[sat].Values["attended_hours"] > s.p.WeekendHours && d.Values["attended_hours"] > s.p.WeekendHours {
			out = append(out, Guardrail{"weekend_work",
				fmt.Sprintf("more than %.0f h of work on both weekend days", s.p.WeekendHours), s.p.WeekendPenalty})
		}
	}
	return out
}

func median(xs []float64) float64 {
	xs = slices.Clone(xs)
	slices.Sort(xs)
	n := len(xs)
	if n%2 == 1 {
		return xs[n/2]
	}
	return (xs[n/2-1] + xs[n/2]) / 2
}

func isWeekend(t time.Time) bool {
	return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday
}

// mustDate parses a logical day. Dates come from the store's own
// formatting; a bad one sorts as the zero time rather than panicking.
func mustDate(s string) time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return t
}
