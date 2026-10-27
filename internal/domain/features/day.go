// Package features computes the daily features the strain score is built
// from (system design §3.5, §7.1). Every feature is a declared entry in the
// registry with a pure compute function, so explanations, the stored schema
// version and the docs come from one place. Pure: no I/O, no clock.
package features

import (
	"fmt"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/model"
)

// Minute is one minute of attention, in the local time it was recorded in.
type Minute struct {
	At        time.Time      // start of the minute, local
	Label     string         // the app (or category) that held most attention
	Category  model.Category // the category of Label
	AttendedS float64        // all attention in the minute
	CallS     float64        // seconds a call held presence
	VideoS    float64        // seconds visible video held presence
}

// Span is one interval of a timeline, in local time.
type Span struct {
	Start, End time.Time
	State      string // agent or human state
	Session    string // agent session key
	Argv0      string // shell commands
	Group      string // shell command group: vcs, build, infra, remote, …
	Exit       int    // shell exit code
	Reason     string // presence reason: input, video, call
}

// Duration returns the span's length.
func (s Span) Duration() time.Duration { return s.End.Sub(s.Start) }

// Day is everything recorded for one logical day, the input to every
// feature.
type Day struct {
	Date    string // logical day, YYYY-MM-DD
	Weekend bool   // Saturday or Sunday

	Minutes  []Minute // in order; minutes without attention are absent
	Presence []Span   // present spans, in order
	Shell    []Span   // finished shell commands, in order of start
	Agent    []Span   // agent state segments
	Human    []Span   // human state segments
	// Busy are calendar busy blocks (State "tentative" when tentative),
	// and Calendar says whether a calendar sync covered this day, so a day
	// without meetings is told apart from a day with no calendar.
	Busy     []Span
	Calendar bool

	// PrevLastPresence is when the previous logical day's presence ended,
	// zero when unknown. It gives the overnight gap.
	PrevLastPresence time.Time

	// Coverage and Present say how much of the day was observed: the
	// share of present time a window was attributed to, and the time the
	// user was present. A barely observed day is missing, not rest.
	Coverage float64
	Present  time.Duration

	// Prior are earlier usable days (observed enough to be scored), in
	// any order. Only days before Date are read; days since a light day
	// needs them.
	Prior []PriorDay
}

// PriorDay is what one earlier day contributes to features that look back.
type PriorDay struct {
	Date      string  // logical day, YYYY-MM-DD
	AttendedH float64 // attended_hours
}

// Params are the definitions features depend on. Defaults follow the
// design; core hours come from the user's profile.
type Params struct {
	CoreStart, CoreEnd time.Duration // core hours, local time of day
	BreakMin           time.Duration // presence gap that counts as a break
	FocusGap           time.Duration // gap allowed inside a focus block
	LateStart, LateEnd time.Duration // late night, local time of day
	LoopWindow         time.Duration // failing commands this close form a loop
	BackToBack         time.Duration // meetings this close are back to back
	MinAttended        time.Duration // below this, rate features are skipped
	MinCommands        int           // below this, the failure rate is skipped
}

// DefaultParams returns the design defaults.
func DefaultParams() Params {
	return Params{
		CoreStart:   9 * time.Hour,
		CoreEnd:     18 * time.Hour,
		BreakMin:    5 * time.Minute,
		FocusGap:    2 * time.Minute,
		LateStart:   22 * time.Hour,
		LateEnd:     5 * time.Hour,
		LoopWindow:  10 * time.Minute,
		BackToBack:  5 * time.Minute,
		MinAttended: 30 * time.Minute,
		MinCommands: 10,
	}
}

// Key identifies the parameters a stored day's features were computed
// with, with the registry version. A day stored under another key is
// recomputed while its raw data is kept, so every scored day is measured
// the same way.
func (p Params) Key() string {
	if p == DefaultParams() {
		return DefaultKey // what days stored before keys existed were computed with
	}
	return fmt.Sprintf("r%d core=%s-%s break=%s focus=%s late=%s-%s loop=%s b2b=%s min=%s cmds=%d",
		RegistryVersion, clock(p.CoreStart), clock(p.CoreEnd), p.BreakMin, p.FocusGap,
		clock(p.LateStart), clock(p.LateEnd), p.LoopWindow, p.BackToBack, p.MinAttended, p.MinCommands)
}

// DefaultKey is DefaultParams' key. Stored days without a key have it.
const DefaultKey = "default"

func clock(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d.Hours()), int(d.Minutes())%60)
}

// timeOfDay returns how far t is into its local calendar day.
func timeOfDay(t time.Time) time.Duration {
	y, m, d := t.Date()
	return t.Sub(time.Date(y, m, d, 0, 0, 0, 0, t.Location()))
}

// work reports whether a minute counts as work: everything attended except
// media (films, music, games).
func work(m Minute) bool { return m.Category != model.CategoryMedia }

func attendedWork(d Day) float64 {
	total := 0.0
	for _, m := range d.Minutes {
		if work(m) {
			total += m.AttendedS
		}
	}
	return total
}
