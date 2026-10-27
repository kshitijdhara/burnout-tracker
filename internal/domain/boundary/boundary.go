// Package boundary is the rules the user sets for their own work (system
// design §8.2): a shutdown time, no weekend work, a cap on meeting hours
// and a daily focus block. Each kind has at most one active rule; changing
// one ends the old rule and starts a new one, so adherence is always
// judged against the rule that applied on the day. Pure: no I/O, no clock.
package boundary

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Kind is what a boundary limits.
type Kind string

// Kinds. Workdays are Monday to Friday.
const (
	// Shutdown: on workdays, no work after a time of day (Value "21:30").
	Shutdown Kind = "shutdown"
	// NoWeekend: no work on Saturdays and Sundays (no Value).
	NoWeekend Kind = "no_weekend"
	// MeetingsMax: on workdays, at most this many hours of meetings and
	// calls (Value "4" or "3.5").
	MeetingsMax Kind = "meetings_max"
	// FocusBlock: on workdays, at least one focus block this many
	// minutes long (Value "90").
	FocusBlock Kind = "focus_block"
)

// Kinds lists every kind, in the order they are offered.
func Kinds() []Kind { return []Kind{Shutdown, NoWeekend, MeetingsMax, FocusBlock} }

// Grace is the work a shutdown or no-weekend day may still have, so a
// glance at a message doesn't break the rule.
const Grace = 15 * time.Minute

// Sources say where a boundary was set. A boundary set from the weekly
// review is how system design §8.6 counts "set or changed a boundary after
// a review", locally and without telemetry.
const (
	SourceCLI       = "cli"
	SourceDashboard = "dashboard"
	SourceReview    = "review"
)

// Boundary is one rule and the logical days it applies to.
type Boundary struct {
	ID    int64
	Kind  Kind
	Value string // normalised: "21:30", "", "3.5", "90"
	// Since is the first day the rule applies: the day after it was set,
	// since a rule set in the evening can't be kept that morning. Until is
	// the day it was removed or replaced, which it no longer applies to,
	// or "" while it is active.
	Since, Until string
	Source       string
}

// ErrInvalid means a boundary can't be stored.
var ErrInvalid = errors.New("boundary: invalid")

// Parse reads a kind and its value, as the user types them ("shutdown",
// "21:30"; "no-weekend"; "meetings-max", "4h"; "focus-block", "90m").
func Parse(kind, value string) (Boundary, error) {
	b := Boundary{Kind: Kind(strings.ReplaceAll(strings.TrimSpace(kind), "-", "_"))}
	value = strings.TrimSpace(value)
	switch b.Kind {
	case Shutdown:
		t, err := time.Parse("15:04", value)
		if err != nil {
			return b, fmt.Errorf("%w: shutdown wants a time like 21:30", ErrInvalid)
		}
		b.Value = t.Format("15:04")
	case NoWeekend:
		if value != "" {
			return b, fmt.Errorf("%w: no-weekend takes no value", ErrInvalid)
		}
	case MeetingsMax:
		h, err := strconv.ParseFloat(strings.TrimSuffix(value, "h"), 64)
		if err != nil || h <= 0 || h > 12 {
			return b, fmt.Errorf("%w: meetings-max wants hours between 0 and 12, like 4 or 3.5", ErrInvalid)
		}
		b.Value = strconv.FormatFloat(h, 'f', -1, 64)
	case FocusBlock:
		m, err := strconv.Atoi(strings.TrimSuffix(value, "m"))
		if err != nil || m < 15 || m > 240 {
			return b, fmt.Errorf("%w: focus-block wants minutes between 15 and 240, like 90", ErrInvalid)
		}
		b.Value = strconv.Itoa(m)
	default:
		return b, fmt.Errorf("%w: kind %q (want one of %s)", ErrInvalid, kind, Names())
	}
	return b, nil
}

// Validate checks a boundary read back or built by a caller.
func (b Boundary) Validate() error {
	p, err := Parse(string(b.Kind), b.Value)
	if err != nil {
		return err
	}
	if p.Value != b.Value || p.Kind != b.Kind {
		return fmt.Errorf("%w: value %q isn't normalised", ErrInvalid, b.Value)
	}
	return nil
}

// Names is the kinds as the user types them.
func Names() string {
	names := make([]string, 0, len(Kinds()))
	for _, k := range Kinds() {
		names = append(names, k.Name())
	}
	return strings.Join(names, ", ")
}

// Name is the kind as the user types it: "no-weekend".
func (k Kind) Name() string { return strings.ReplaceAll(string(k), "_", "-") }

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool { return slices.Contains(Kinds(), k) }

// Workdays reports whether the kind applies on workdays (else weekends).
func (k Kind) Workdays() bool { return k != NoWeekend }

// ActiveOn reports whether the rule applied on the logical day.
func (b Boundary) ActiveOn(day string) bool {
	return b.Since != "" && day >= b.Since && (b.Until == "" || day < b.Until)
}

// Active reports whether the rule hasn't been removed or replaced.
func (b Boundary) Active() bool { return b.Until == "" }

// ShutdownAt is a shutdown's time of day.
func (b Boundary) ShutdownAt() time.Duration {
	t, _ := time.Parse("15:04", b.Value)
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
}

// Number is a meetings cap in hours or a focus block in minutes.
func (b Boundary) Number() float64 {
	v, _ := strconv.ParseFloat(b.Value, 64)
	return v
}

// Describe says the rule in a sentence fragment: "stop work by 21:30 on
// workdays".
func (b Boundary) Describe() string {
	switch b.Kind {
	case Shutdown:
		return "stop work by " + b.Value + " on workdays"
	case NoWeekend:
		return "no work at the weekend"
	case MeetingsMax:
		unit := "hours"
		if b.Value == "1" {
			unit = "hour"
		}
		return fmt.Sprintf("at most %s %s of meetings on workdays", b.Value, unit)
	case FocusBlock:
		return "a focus block of at least " + b.Value + " minutes on workdays"
	}
	return string(b.Kind)
}

// Current returns the active rule of each kind, in Kinds order.
func Current(bs []Boundary) []Boundary {
	var out []Boundary
	for _, k := range Kinds() {
		for _, b := range bs {
			if b.Kind == k && b.Active() {
				out = append(out, b)
				break
			}
		}
	}
	return out
}
