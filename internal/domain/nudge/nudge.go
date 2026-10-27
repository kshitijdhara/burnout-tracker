// Package nudge decides the day's nudge (system design §8.3): at most one
// a day, from the user's boundaries and the strain guardrails, in
// supportive copy from a fixed table, and never while it could embarrass
// or interrupt: outside waking hours, during a call, video, a screen share
// or Focus, while paused or in private hours (privacy & security §5.1
// "Screen-share safety"). Pure: no I/O, no clock.
package nudge

import (
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/schedule"
)

// Kind is what a nudge is about. Each can be turned off.
type Kind string

// The kinds. The guardrail kinds share the names of strain's guardrails.
const (
	Shutdown     Kind = "shutdown"      // the shutdown time has passed on a workday
	DayOff       Kind = "day_off"       // working on a weekend the user keeps free
	WeeklyHours  Kind = "weekly_hours"  // a long week
	OvernightGap Kind = "overnight_gap" // a short night
	WeekendWork  Kind = "weekend_work"  // a busy weekend
)

// Kinds lists every kind, in the order Pick prefers them.
func Kinds() []Kind {
	return []Kind{Shutdown, DayOff, WeeklyHours, OvernightGap, WeekendWork}
}

// Copy is what a nudge says. Neither part names a number: nudges are
// read at a glance, maybe by someone else, and are never a scorecard.
type Copy struct {
	Title string // beside the icon, where the platform shows titles
	Text  string // the menu item
}

var copies = map[Kind]Copy{
	Shutdown:     {"Wrap up?", "It's around the shutdown time you set. A good moment to wrap up for today?"},
	DayOff:       {"Day off", "Today is one of your days off. Could the rest wait for your next workday?"},
	WeeklyHours:  {"Lighter day?", "It's been a full week. Could tomorrow be a lighter day?"},
	OvernightGap: {"Rest", "Last night was a short one. An earlier finish tonight could help."},
	WeekendWork:  {"Free evening?", "The weekend was a busy one. Is there an evening this week you could keep free?"},
}

// CopyOf returns what kind says.
func CopyOf(k Kind) Copy { return copies[k] }

// Boundaries are the user's boundaries that a nudge can point to.
type Boundaries struct {
	Shutdown    time.Duration // when work should end on workdays, from the logical day's midnight: 01:00 is 25h
	HasShutdown bool
	WeekendsOff bool // Saturday and Sunday are kept free
}

// Now is what is happening at the moment of the decision.
type Now struct {
	At  time.Time // local time
	Day string    // the logical day, YYYY-MM-DD

	// Known is true when the desktop collector reported recently. Without
	// it calls and video can't be seen, so nothing shows.
	Known  bool
	Active bool // the user is at the device: recent input, screen unlocked

	InCall      bool
	Video       bool
	ScreenShare bool
	Focus       bool // Do Not Disturb or a Focus mode
	Paused      bool
	Private     bool // private hours
}

// Settings are the user's nudge settings: the config file's, with the
// kinds turned off from the tray added to Off.
type Settings struct {
	Enabled bool
	Waking  schedule.Window
	Off     map[Kind]bool
}

// Today is what has happened with nudges on the current logical day.
type Today struct {
	Day     string // the day Kind was shown on; "" for never
	Kind    Kind
	Snoozed bool // snoozed until tomorrow
}

// Decision is the outcome: Kind is the day's nudge ("" for none), and
// Show is whether it may show right now. Why says why not, for tests and
// the log.
type Decision struct {
	Kind Kind
	Show bool
	Why  string
}

// Decide decides the nudge at now. A Kind that shows for the first time
// today must be recorded in Today by the caller, so no other kind shows
// the same day. Call it on every refresh: suppression is never cached.
func Decide(s Settings, b Boundaries, guardrails []string, today Today, now Now) Decision {
	if !s.Enabled {
		return Decision{Why: "nudges are off"}
	}
	var kind Kind
	if today.Day == now.Day {
		if today.Snoozed {
			return Decision{Why: "snoozed until tomorrow"}
		}
		kind = today.Kind
		if s.Off[kind] {
			return Decision{Why: "this kind is off"}
		}
	} else {
		kind = Pick(b, guardrails, now, s.Off)
	}
	if kind == "" {
		return Decision{Why: "nothing to say"}
	}
	if why := Suppressed(s, now); why != "" {
		return Decision{Kind: kind, Why: why}
	}
	return Decision{Kind: kind, Show: true}
}

// Suppressed returns why no nudge may show at now, or "" when one may.
// Where calls can't be seen (Windows and Linux so far) InCall and Video
// stay false and nudges still show: there the nudge lives only inside the
// menu, which a screen share reveals only if the user opens it, and the
// gap is listed in the collector's limitations.
func Suppressed(s Settings, now Now) string {
	switch {
	case now.Paused:
		return "paused"
	case now.Private:
		return "private hours"
	case !s.Waking.Contains(now.At):
		return "outside waking hours"
	case !now.Known:
		return "the desktop collector hasn't reported"
	case now.InCall:
		return "in a call"
	case now.Video:
		return "video is playing"
	case now.ScreenShare:
		return "the screen is shared"
	case now.Focus:
		return "Focus or Do Not Disturb is on"
	}
	return ""
}

// sinceMidnight is at's wall-clock time from the start of day's calendar
// date. Past midnight it keeps counting: 01:30 the next morning is
// 25h30m. It reads the clock, not the elapsed time, so a 21:30 shutdown
// is 21h30m on the days the clocks change too.
func sinceMidnight(at, day time.Time) time.Duration {
	y, m, d := at.Date()
	days := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Sub(time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC))
	clock := time.Duration(at.Hour())*time.Hour + time.Duration(at.Minute())*time.Minute +
		time.Duration(at.Second())*time.Second + time.Duration(at.Nanosecond())
	return days + clock
}

// Pick returns the kind that applies at now and isn't off, or "" for none.
// guardrails are the names of the limits the latest scored day crossed.
func Pick(b Boundaries, guardrails []string, now Now, off map[Kind]bool) Kind {
	day, err := time.ParseInLocation(time.DateOnly, now.Day, now.At.Location())
	if err != nil {
		return ""
	}
	weekend := day.Weekday() == time.Saturday || day.Weekday() == time.Sunday
	tod := sinceMidnight(now.At, day)
	applies := map[Kind]bool{
		Shutdown: b.HasShutdown && !weekend && now.Active && tod >= b.Shutdown,
		DayOff:   b.WeekendsOff && weekend && now.Active,
	}
	for _, g := range guardrails {
		applies[Kind(g)] = true
	}
	for _, k := range Kinds() {
		if applies[k] && !off[k] {
			return k
		}
	}
	return ""
}
