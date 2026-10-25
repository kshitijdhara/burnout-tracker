// Package annotation marks days with what was going on (system design
// §7.2, §8.2): vacation and sick days are kept out of baselines and not
// scored; planned crunch (on-call, exams, a release) is scored and tagged.
// Pure: no I/O, no clock.
package annotation

import (
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode"
	"unicode/utf8"
)

// Kind is what a stretch of days was.
type Kind string

// Kinds (system design §6).
const (
	OnCall   Kind = "on_call"
	Exam     Kind = "exam"
	Release  Kind = "release"
	Vacation Kind = "vacation"
	Sick     Kind = "sick"
	Other    Kind = "other"
)

// Kinds lists every kind, in the order they are offered.
func Kinds() []Kind { return []Kind{Vacation, Sick, OnCall, Release, Exam, Other} }

// MaxNote caps a note's length in characters.
const MaxNote = 200

// Annotation marks the logical days From to To, inclusive.
type Annotation struct {
	ID       int64
	From, To string // YYYY-MM-DD
	Kind     Kind
	Note     string // optional, the user's own words
}

// Excludes reports whether the annotated days are left out of baselines
// and scores: time off isn't the user's usual, and isn't rest from work
// either.
func (a Annotation) Excludes() bool { return a.Kind == Vacation || a.Kind == Sick }

// Covers reports whether the annotation includes the logical day.
func (a Annotation) Covers(day string) bool { return day >= a.From && day <= a.To }

// Days returns how many days the annotation spans.
func (a Annotation) Days() int {
	from, _ := time.Parse(time.DateOnly, a.From)
	to, _ := time.Parse(time.DateOnly, a.To)
	return int(to.Sub(from).Hours()/24) + 1
}

// ErrInvalid means an annotation can't be stored.
var ErrInvalid = errors.New("annotation: invalid")

// Validate checks the dates, kind and note.
func (a Annotation) Validate() error {
	if !slices.Contains(Kinds(), a.Kind) {
		return fmt.Errorf("%w: kind %q (want one of %v)", ErrInvalid, a.Kind, Kinds())
	}
	from, err1 := time.Parse(time.DateOnly, a.From)
	to, err2 := time.Parse(time.DateOnly, a.To)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("%w: dates must be YYYY-MM-DD", ErrInvalid)
	}
	if to.Before(from) {
		return fmt.Errorf("%w: ends before it starts", ErrInvalid)
	}
	if to.Sub(from) > 366*24*time.Hour {
		return fmt.Errorf("%w: longer than a year", ErrInvalid)
	}
	if utf8.RuneCountInString(a.Note) > MaxNote {
		return fmt.Errorf("%w: note longer than %d characters", ErrInvalid, MaxNote)
	}
	for _, r := range a.Note {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("%w: note contains a control character", ErrInvalid)
		}
	}
	return nil
}

// Excluded returns the set of days, among days, that an excluding
// annotation covers.
func Excluded(as []Annotation, days []string) map[string]bool {
	out := map[string]bool{}
	for _, d := range days {
		for _, a := range as {
			if a.Excludes() && a.Covers(d) {
				out[d] = true
				break
			}
		}
	}
	return out
}
