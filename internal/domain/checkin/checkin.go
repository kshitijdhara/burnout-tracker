// Package checkin defines the self-report check-ins (system design §4.5):
// the weekly Copenhagen Burnout Inventory and an optional daily micro
// check-in. They are the labels the strain score is validated against.
// Pure: no I/O, no clock.
package checkin

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// Choice is one answer option and the value it scores.
type Choice struct {
	Label string
	Value int
}

// Item is one question.
type Item struct {
	ID      string
	Text    string
	Choices []Choice
	// Reverse marks an item where a higher answer means less burnout; its
	// value is subtracted from the scale maximum before averaging.
	Reverse bool
}

// Instrument is a fixed questionnaire in one language. Only the published,
// validated wording is used; translations are added only when a validated
// one exists (system design §4.5).
type Instrument struct {
	ID    string // stored with the answers, e.g. "cbi-work-en"
	Name  string
	Intro string
	Items []Item
	Max   int // the highest choice value
}

// The CBI's two response scales (Kristensen et al., 2005).
var (
	frequency = []Choice{
		{"Always", 100}, {"Often", 75}, {"Sometimes", 50}, {"Seldom", 25}, {"Never / almost never", 0},
	}
	degree = []Choice{
		{"To a very high degree", 100}, {"To a high degree", 75}, {"Somewhat", 50},
		{"To a low degree", 25}, {"To a very low degree", 0},
	}
)

// Instrument IDs.
const (
	CBIPersonalID = "cbi-personal-en"
	CBIWorkID     = "cbi-work-en"
	DailyID       = "daily-v1"
)

// CBIPersonal is the CBI personal burnout subscale: "a state of prolonged
// physical and psychological exhaustion".
func CBIPersonal() Instrument {
	return Instrument{
		ID: CBIPersonalID, Name: "Personal burnout", Max: 100,
		Intro: "These questions are about how you feel in general.",
		Items: []Item{
			{ID: "p1", Text: "How often do you feel tired?", Choices: frequency},
			{ID: "p2", Text: "How often are you physically exhausted?", Choices: frequency},
			{ID: "p3", Text: "How often are you emotionally exhausted?", Choices: frequency},
			{ID: "p4", Text: `How often do you think: "I can't take it anymore"?`, Choices: frequency},
			{ID: "p5", Text: "How often do you feel worn out?", Choices: frequency},
			{ID: "p6", Text: "How often do you feel weak and susceptible to illness?", Choices: frequency},
		},
	}
}

// CBIWork is the CBI work-related burnout subscale: exhaustion the person
// attributes to their work.
func CBIWork() Instrument {
	return Instrument{
		ID: CBIWorkID, Name: "Work-related burnout", Max: 100,
		Intro: "These questions are about your work.",
		Items: []Item{
			{ID: "w1", Text: "Is your work emotionally exhausting?", Choices: degree},
			{ID: "w2", Text: "Do you feel burnt out because of your work?", Choices: degree},
			{ID: "w3", Text: "Does your work frustrate you?", Choices: degree},
			{ID: "w4", Text: "Do you feel worn out at the end of the working day?", Choices: frequency},
			{ID: "w5", Text: "Are you exhausted in the morning at the thought of another day at work?", Choices: frequency},
			{ID: "w6", Text: "Do you feel that every working hour is tiring for you?", Choices: frequency},
			{ID: "w7", Text: "Do you have enough energy for family and friends during leisure time?", Choices: frequency, Reverse: true},
		},
	}
}

// Weekly returns the weekly check-in: both CBI subscales.
func Weekly() []Instrument { return []Instrument{CBIPersonal(), CBIWork()} }

// Daily is the optional one-tap micro check-in. It is not a validated
// instrument; it gives denser labels between weekly check-ins (§7.5).
func Daily() Instrument {
	scale := []Choice{{"Not at all", 0}, {"A little", 25}, {"Moderately", 50}, {"Quite a bit", 75}, {"Extremely", 100}}
	return Instrument{
		ID: DailyID, Name: "Today", Max: 100,
		Intro: "Two quick questions about today.",
		Items: []Item{
			{ID: "energy", Text: "How much energy do you have left?", Choices: scale, Reverse: true},
			{ID: "drained", Text: "How drained do you feel?", Choices: scale},
		},
	}
}

// ByID returns a known instrument.
func ByID(id string) (Instrument, bool) {
	for _, in := range []Instrument{CBIPersonal(), CBIWork(), Daily()} {
		if in.ID == id {
			return in, true
		}
	}
	return Instrument{}, false
}

// Answers maps item IDs to the chosen value.
type Answers map[string]int

// ErrIncomplete means not every item was answered.
var ErrIncomplete = errors.New("checkin: not every question was answered")

// Score validates answers and returns the instrument's score, 0–100: the
// mean of item values, with reversed items flipped. Every item must be
// answered; a partial check-in is not stored.
func (in Instrument) Score(a Answers) (float64, error) {
	if len(a) != len(in.Items) {
		return 0, ErrIncomplete
	}
	sum := 0
	for _, it := range in.Items {
		v, ok := a[it.ID]
		if !ok {
			return 0, ErrIncomplete
		}
		if !slices.ContainsFunc(it.Choices, func(c Choice) bool { return c.Value == v }) {
			return 0, fmt.Errorf("checkin: %d is not an answer to %s", v, it.ID)
		}
		if it.Reverse {
			v = in.Max - v
		}
		sum += v
	}
	return float64(sum) / float64(len(in.Items)), nil
}

// Band names a CBI score range. The CBI has no official cut-offs; these
// are the bands commonly used with it (Borritz et al., 2006): below 50 low,
// 50–74 moderate, 75–99 high, 100 severe.
type Band string

// Bands.
const (
	BandLow      Band = "low"
	BandModerate Band = "moderate"
	BandHigh     Band = "high"
	BandSevere   Band = "severe"
)

// BandOf returns the band of a 0–100 score.
func BandOf(score float64) Band {
	switch {
	case score >= 100:
		return BandSevere
	case score >= 75:
		return BandHigh
	case score >= 50:
		return BandModerate
	default:
		return BandLow
	}
}

// NeedsSupport reports whether a result should show the safety flow
// (system design §8.4): a CBI subscale in the high band or above.
func NeedsSupport(instrumentID string, score float64) bool {
	if instrumentID != CBIPersonalID && instrumentID != CBIWorkID {
		return false
	}
	b := BandOf(score)
	return b == BandHigh || b == BandSevere
}

// Record is one completed check-in with one instrument.
type Record struct {
	At         time.Time
	Instrument string
	Answers    Answers
	Score      float64
}
