// Package schedule describes recurring weekly windows of local time, such
// as private hours (privacy & security §5.1): "Mon-Fri 19:00-08:00",
// "Sat,Sun" or "12:00-13:00". Pure: no I/O, no clock.
package schedule

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const fullDay = 24 * time.Hour

// Window is a span of local time that recurs on some days of the week.
// When End is not after Start, the window runs past midnight into the next
// day: "Mon-Fri 19:00-08:00" starts on weekday evenings and ends on the
// following mornings.
type Window struct {
	Days       [7]bool       // by time.Weekday: the days the window starts on
	Start, End time.Duration // local time of day; a whole day is 0 to 24h
}

// Schedule is a set of windows; a time is inside it when it is inside any.
type Schedule []Window

// Contains reports whether t, read in its own location, falls in w.
func (w Window) Contains(t time.Time) bool {
	tod := time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second
	day := t.Weekday()
	if w.Start < w.End {
		return w.Days[day] && tod >= w.Start && tod < w.End
	}
	prev := (day + 6) % 7
	return (w.Days[day] && tod >= w.Start) || (w.Days[prev] && tod < w.End)
}

// Contains reports whether t falls in any window of s.
func (s Schedule) Contains(t time.Time) bool {
	for _, w := range s {
		if w.Contains(t) {
			return true
		}
	}
	return false
}

var dayNames = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// Parse reads one window: optional days, then an optional time range.
//
//	Mon-Fri 19:00-08:00   weekday evenings into the next morning
//	Sat,Sun               all of Saturday and Sunday
//	12:00-13:00           every day
//
// Days are three-letter names, ranges of them (Fri-Mon wraps), or daily,
// weekdays or weekends, joined by commas. Times are 24-hour HH:MM; 24:00
// may end a range.
func Parse(spec string) (Window, error) {
	fields := strings.Fields(spec)
	var w Window
	var daysSpec, timesSpec string
	switch len(fields) {
	case 1:
		if strings.Contains(fields[0], ":") {
			timesSpec = fields[0]
		} else {
			daysSpec = fields[0]
		}
	case 2:
		daysSpec, timesSpec = fields[0], fields[1]
	default:
		return w, fmt.Errorf("schedule: %q: want [DAYS] [HH:MM-HH:MM], for example \"Mon-Fri 19:00-08:00\"", spec)
	}
	if daysSpec == "" {
		daysSpec = "daily"
	}
	days, err := parseDays(daysSpec)
	if err != nil {
		return w, fmt.Errorf("schedule: %q: %w", spec, err)
	}
	w.Days = days
	if timesSpec == "" {
		w.Start, w.End = 0, fullDay
		return w, nil
	}
	start, end, ok := strings.Cut(timesSpec, "-")
	if !ok {
		return w, fmt.Errorf("schedule: %q: want a time range like 19:00-08:00", spec)
	}
	if w.Start, err = parseClock(start, false); err != nil {
		return w, fmt.Errorf("schedule: %q: %w", spec, err)
	}
	if w.End, err = parseClock(end, true); err != nil {
		return w, fmt.Errorf("schedule: %q: %w", spec, err)
	}
	if w.Start == w.End || (w.End == fullDay && w.Start == 0) {
		if w.Start != 0 {
			return w, fmt.Errorf("schedule: %q: the range is empty", spec)
		}
		w.End = fullDay
	}
	return w, nil
}

func parseDays(spec string) ([7]bool, error) {
	var days [7]bool
	for part := range strings.SplitSeq(strings.ToLower(spec), ",") {
		switch part {
		case "daily":
			for i := range days {
				days[i] = true
			}
			continue
		case "weekdays":
			for i := time.Monday; i <= time.Friday; i++ {
				days[i] = true
			}
			continue
		case "weekends":
			days[time.Saturday], days[time.Sunday] = true, true
			continue
		}
		from, to, isRange := strings.Cut(part, "-")
		first, ok := dayIndex(from)
		if !ok {
			return days, fmt.Errorf("unknown day %q (use Mon, Tue, … Sun, daily, weekdays or weekends)", from)
		}
		last := first
		if isRange {
			if last, ok = dayIndex(to); !ok {
				return days, fmt.Errorf("unknown day %q (use Mon, Tue, … Sun, daily, weekdays or weekends)", to)
			}
		}
		for d := first; ; d = (d + 1) % 7 {
			days[d] = true
			if d == last {
				break
			}
		}
	}
	return days, nil
}

func dayIndex(name string) (int, bool) {
	for i, n := range dayNames {
		if name == n {
			return i, true
		}
	}
	return 0, false
}

var errClock = errors.New("want a time like 08:00 or 19:30")

func parseClock(s string, allow24 bool) (time.Duration, error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) == 0 || len(h) > 2 || len(m) != 2 {
		return 0, errClock
	}
	hh, mm := 0, 0
	for _, c := range h {
		if c < '0' || c > '9' {
			return 0, errClock
		}
		hh = hh*10 + int(c-'0')
	}
	for _, c := range m {
		if c < '0' || c > '9' {
			return 0, errClock
		}
		mm = mm*10 + int(c-'0')
	}
	d := time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute
	if mm > 59 || d > fullDay || (d == fullDay && !allow24) {
		return 0, errClock
	}
	return d, nil
}

// String renders w in the form Parse reads.
func (w Window) String() string {
	var days string
	switch w.Days {
	case [7]bool{true, true, true, true, true, true, true}:
	case [7]bool{false, true, true, true, true, true, false}:
		days = "Mon-Fri"
	case [7]bool{true, false, false, false, false, false, true}:
		days = "Sat,Sun"
	default:
		var names []string
		for _, d := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday} {
			if w.Days[d] {
				names = append(names, strings.ToUpper(dayNames[d][:1])+dayNames[d][1:])
			}
		}
		days = strings.Join(names, ",")
	}
	if w.Start == 0 && w.End == fullDay {
		if days == "" {
			return "daily"
		}
		return days
	}
	clock := func(d time.Duration) string { return fmt.Sprintf("%02d:%02d", int(d.Hours()), int(d.Minutes())%60) }
	times := clock(w.Start) + "-" + clock(w.End)
	if days == "" {
		return times
	}
	return days + " " + times
}
