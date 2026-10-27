package calendar

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// rule is the part of RFC 5545 RRULE that calendar feeds use for meetings:
// FREQ DAILY, WEEKLY, MONTHLY or YEARLY with INTERVAL, COUNT, UNTIL,
// BYDAY (with ordinals for MONTHLY and YEARLY), BYMONTHDAY, BYMONTH and
// BYSETPOS. Anything else (BYHOUR, BYWEEKNO, sub-daily frequencies) is
// unsupported: the event then keeps only its first instance.
type rule struct {
	freq       string
	interval   int
	count      int
	until      time.Time
	byDay      []weekdayNum
	byMonthDay []int
	byMonth    []int
	bySetPos   []int
}

type weekdayNum struct {
	n   int // 0: every such weekday in the period; else its ordinal (−1: last)
	day time.Weekday
}

var errUnsupported = errors.New("calendar: unsupported recurrence rule")

var weekdays = map[string]time.Weekday{
	"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
}

func parseRule(s string, loc *time.Location, o Options) (rule, error) {
	r := rule{interval: 1}
	for part := range strings.SplitSeq(strings.ToUpper(strings.TrimSpace(s)), ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		var err error
		switch k {
		case "FREQ":
			r.freq = v
		case "INTERVAL":
			r.interval, err = strconv.Atoi(v)
			if err == nil && (r.interval < 1 || r.interval > 1000) {
				err = errUnsupported
			}
		case "COUNT":
			r.count, err = strconv.Atoi(v)
			if err == nil && r.count < 1 {
				err = errUnsupported
			}
		case "UNTIL":
			var allDay bool
			r.until, allDay, err = parseTime(v, nil, Options{Local: loc, Zone: o.Zone})
			if allDay {
				r.until = r.until.AddDate(0, 0, 1).Add(-time.Second) // the whole day is included
			}
		case "BYDAY":
			for d := range strings.SplitSeq(v, ",") {
				if len(d) < 2 {
					return r, errUnsupported
				}
				wd, ok := weekdays[d[len(d)-2:]]
				if !ok {
					return r, errUnsupported
				}
				n := 0
				if num := d[:len(d)-2]; num != "" {
					if n, err = strconv.Atoi(strings.TrimPrefix(num, "+")); err != nil || n == 0 || n < -53 || n > 53 {
						return r, errUnsupported
					}
				}
				r.byDay = append(r.byDay, weekdayNum{n, wd})
			}
		case "BYMONTHDAY":
			r.byMonthDay, err = ints(v, -31, 31)
		case "BYMONTH":
			r.byMonth, err = ints(v, 1, 12)
		case "BYSETPOS":
			r.bySetPos, err = ints(v, -366, 366)
		case "WKST":
		default:
			return r, errUnsupported // BYHOUR, BYMINUTE, BYWEEKNO, BYYEARDAY, …
		}
		if err != nil {
			return r, errUnsupported
		}
	}
	switch r.freq {
	case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
	default:
		return r, errUnsupported
	}
	return r, nil
}

func ints(v string, lo, hi int) ([]int, error) {
	var out []int
	for s := range strings.SplitSeq(v, ",") {
		n, err := strconv.Atoi(strings.TrimPrefix(s, "+"))
		if err != nil || n == 0 || n < lo || n > hi {
			return nil, fmt.Errorf("value %q", s)
		}
		out = append(out, n)
	}
	return out, nil
}

// each calls fn with every instance start in [from, stop), honouring
// COUNT and UNTIL, which count from dtstart. Instances keep dtstart's
// wall-clock time in its zone, so a 09:00 meeting stays at 09:00 across
// daylight-saving changes.
func (r rule) each(dtstart, from, stop time.Time, fn func(time.Time)) {
	emitted, seen, iterations := 0, 0, 0
	emit := func(t time.Time) bool {
		if t.Before(dtstart) {
			return true
		}
		if (!r.until.IsZero() && t.After(r.until)) || !t.Before(stop) {
			return false
		}
		seen++
		if r.count > 0 && seen > r.count {
			return false
		}
		if t.Before(from) {
			return true
		}
		fn(t)
		emitted++
		return emitted < maxOccurrences
	}
	y, m, d := dtstart.Date()
	hh, mm, ss := dtstart.Clock()
	loc := dtstart.Location()
	at := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, hh, mm, ss, 0, loc) }

	for period := 0; ; period++ {
		iterations++
		if iterations > maxIterations {
			return
		}
		var candidates []time.Time
		switch r.freq {
		case "DAILY":
			t := at(y, m, d+period*r.interval)
			if r.monthOK(t.Month()) && r.dayOK(t.Weekday()) && r.monthDayOK(t) {
				candidates = []time.Time{t}
			}
		case "WEEKLY":
			// The week of dtstart, shifted by whole intervals; weeks start Monday.
			weekStart := at(y, m, d-(int(dtstart.Weekday())+6)%7+7*period*r.interval)
			if len(r.byDay) == 0 {
				candidates = []time.Time{at(y, m, d+7*period*r.interval)}
				break
			}
			for i := range 7 {
				t := weekStart.AddDate(0, 0, i)
				t = at(t.Year(), t.Month(), t.Day())
				if r.dayOK(t.Weekday()) && r.monthOK(t.Month()) {
					candidates = append(candidates, t)
				}
			}
		case "MONTHLY":
			first := time.Date(y, m+time.Month(period*r.interval), 1, 0, 0, 0, 0, loc)
			if r.monthOK(first.Month()) {
				candidates = r.inMonth(first, d, at)
			}
		case "YEARLY":
			year := y + period*r.interval
			months := r.byMonth
			if len(months) == 0 {
				months = []int{int(m)}
			}
			for _, mo := range months {
				first := time.Date(year, time.Month(mo), 1, 0, 0, 0, 0, loc)
				candidates = append(candidates, r.inMonth(first, d, at)...)
			}
		}
		slices.SortFunc(candidates, func(a, b time.Time) int { return a.Compare(b) })
		candidates = r.setPos(candidates)
		for _, t := range candidates {
			if !emit(t) {
				return
			}
		}
		// Stop once whole periods start after the window or UNTIL.
		probe := at(y, m, d)
		switch r.freq {
		case "DAILY":
			probe = at(y, m, d+(period+1)*r.interval)
		case "WEEKLY":
			probe = at(y, m, d+7*(period+1)*r.interval-7)
		case "MONTHLY":
			probe = time.Date(y, m+time.Month((period+1)*r.interval), 1, 0, 0, 0, 0, loc).AddDate(0, 0, -7)
		case "YEARLY":
			probe = time.Date(y+(period+1)*r.interval, 1, 1, 0, 0, 0, 0, loc).AddDate(0, 0, -7)
		}
		if !probe.Before(stop) || (!r.until.IsZero() && probe.After(r.until)) {
			return
		}
	}
}

// inMonth returns the candidate days of the month starting at first: the
// BYDAY and BYMONTHDAY matches, or dtstart's day of the month.
func (r rule) inMonth(first time.Time, dtDay int, at func(int, time.Month, int) time.Time) []time.Time {
	y, m := first.Year(), first.Month()
	daysIn := time.Date(y, m+1, 0, 0, 0, 0, 0, first.Location()).Day()
	var out []time.Time
	switch {
	case len(r.byDay) > 0:
		for _, wd := range r.byDay {
			var matches []int
			for day := 1; day <= daysIn; day++ {
				if time.Date(y, m, day, 0, 0, 0, 0, first.Location()).Weekday() == wd.day {
					matches = append(matches, day)
				}
			}
			switch {
			case wd.n == 0:
				for _, day := range matches {
					out = append(out, at(y, m, day))
				}
			case wd.n > 0 && wd.n <= len(matches):
				out = append(out, at(y, m, matches[wd.n-1]))
			case wd.n < 0 && -wd.n <= len(matches):
				out = append(out, at(y, m, matches[len(matches)+wd.n]))
			}
		}
		if len(r.byMonthDay) > 0 {
			out = slices.DeleteFunc(out, func(t time.Time) bool { return !r.monthDayOK(t) })
		}
	case len(r.byMonthDay) > 0:
		for _, md := range r.byMonthDay {
			day := md
			if md < 0 {
				day = daysIn + md + 1
			}
			if day >= 1 && day <= daysIn {
				out = append(out, at(y, m, day))
			}
		}
	default:
		if dtDay <= daysIn { // a meeting on the 31st skips shorter months
			out = append(out, at(y, m, dtDay))
		}
	}
	return out
}

func (r rule) dayOK(wd time.Weekday) bool {
	if len(r.byDay) == 0 {
		return true
	}
	for _, d := range r.byDay {
		if d.day == wd {
			return true
		}
	}
	return false
}

func (r rule) monthOK(m time.Month) bool {
	return len(r.byMonth) == 0 || slices.Contains(r.byMonth, int(m))
}

func (r rule) monthDayOK(t time.Time) bool {
	if len(r.byMonthDay) == 0 {
		return true
	}
	daysIn := time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
	for _, md := range r.byMonthDay {
		if md == t.Day() || (md < 0 && daysIn+md+1 == t.Day()) {
			return true
		}
	}
	return false
}

// setPos keeps the BYSETPOS positions of a period's sorted candidates.
func (r rule) setPos(c []time.Time) []time.Time {
	if len(r.bySetPos) == 0 {
		return c
	}
	var out []time.Time
	for _, p := range r.bySetPos {
		switch {
		case p > 0 && p <= len(c):
			out = append(out, c[p-1])
		case p < 0 && -p <= len(c):
			out = append(out, c[len(c)+p])
		}
	}
	slices.SortFunc(out, func(a, b time.Time) int { return a.Compare(b) })
	return slices.Compact(out)
}
