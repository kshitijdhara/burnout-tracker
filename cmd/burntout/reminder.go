package main

import "time"

// reminderEvery is how often the tray asks the user to look at what is
// collected (privacy & security §5.2 AC-4: at least monthly).
const reminderEvery = 30 * 24 * time.Hour

// reminderDue reports whether the monthly "what's collected" reminder
// should show at now. consented is when the user consented (zero if
// unknown); seen is when they last opened the reminder (zero if never).
// The month runs from the later of the two. With no usable date, or one in
// the future (a clock change), it shows: an extra reminder is harmless, a
// missed one breaks AC-4.
func reminderDue(consented, seen, now time.Time) bool {
	from := consented
	if seen.After(from) {
		from = seen
	}
	if from.IsZero() || from.After(now) {
		return true
	}
	return now.Sub(from) >= reminderEvery
}
