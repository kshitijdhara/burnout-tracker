// Package attention turns desktop snapshots into presence and a
// distribution of attention over visible windows (ADR-0002, system design
// §4.1). It is pure: no I/O, no goroutines, no wall clock.
package attention

import "time"

// Profile adapts presence detection to how a person uses their computer, so
// assistive-technology users aren't marked away (system design §4.1).
type Profile int

// Input profiles.
const (
	ProfileDefault Profile = iota
	ProfileKeyboardOnly
	ProfileVoiceControl
	ProfileScreenReader
)

// Params are the model's tunable constants. Defaults are documented in
// DefaultParams; Phase 2 fits them from opt-in spot-labels.
type Params struct {
	// Score weights.
	AVisible, AKeys, AScroll, AHover, AVideo, ACall, ARecent float64

	// Temperature of the softmax over window scores. Lower is sharper.
	Temperature float64
	// Below this best score, attention goes to the unattributed bucket.
	UnattributedBelow float64

	// Saturation constants: sat(x) = 1 - exp(-x/k).
	KeysSat, ScrollSat float64
	HoverSat           time.Duration
	// Decay of the "recently interacted with" signal.
	RecentTau time.Duration

	// Presence stays at 1 this long after the last input.
	ActiveInput time.Duration
	// After that, presence decays linearly to 0 over an app-aware grace window.
	GraceDefault, GraceDocs, GraceAgentOutput time.Duration
	// An idle stretch that the user's input ends while the OS idle time,
	// past ActiveInput, is within this cap, with no lock or sleep in
	// between, counts as present in full (reading, thinking; ADR-0008).
	// Longer or interrupted stretches keep the grace rule. App-aware like
	// the grace windows.
	ReadingCapDefault, ReadingCapDocs, ReadingCapAgentOutput time.Duration

	// Window of recent input used for the key/scroll signals.
	InputWindow time.Duration
	// Ticks further apart than this are treated as a gap (sleep, suspend):
	// the gap is not attributed to anything.
	MaxGap time.Duration
	// Minimum visible fraction for a window playing video to hold presence.
	VideoMinVisible float64

	Profile Profile
}

// DefaultParams returns the documented v1 defaults.
func DefaultParams() Params {
	return Params{
		AVisible: 0.2, AKeys: 1.0, AScroll: 0.9, AHover: 0.5, AVideo: 0.8, ACall: 1.0, ARecent: 0.6,
		Temperature:       0.12,
		UnattributedBelow: 0.15,
		KeysSat:           3, ScrollSat: 2, HoverSat: 5 * time.Second,
		RecentTau:             60 * time.Second,
		ActiveInput:           30 * time.Second,
		GraceDefault:          60 * time.Second,
		GraceDocs:             2 * time.Minute,
		GraceAgentOutput:      3 * time.Minute,
		ReadingCapDefault:     4 * time.Minute,
		ReadingCapDocs:        8 * time.Minute,
		ReadingCapAgentOutput: 8 * time.Minute,
		InputWindow:           5 * time.Second,
		MaxGap:                5 * time.Second,
		VideoMinVisible:       0.2,
	}
}

// graceScale stretches grace windows for profiles whose users produce
// sparse or no pointer and keyboard input while still engaged.
func (p Params) graceScale() float64 {
	switch p.Profile {
	case ProfileScreenReader, ProfileVoiceControl:
		return 3
	case ProfileKeyboardOnly:
		return 1.5
	default:
		return 1
	}
}
