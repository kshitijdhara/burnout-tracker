// Package tray shows the menu-bar icon (macOS) or notification-area icon
// (Windows, Linux) while the daemon runs, and its menu: whether anything
// is being recorded, pause and resume, delete the last hour or today, open
// the dashboard (privacy & security §5.1), the monthly "what's
// collected" reminder (§5.2 AC-4) and the day's nudge (system design §8.3). The icon has no hide or
// quit item: while a collector runs it stays visible (§5.2 AC-1).
package tray

import (
	"context"
	"errors"
	"log/slog"
)

// ErrIconGone means the icon went away while work ran, so work was
// stopped: nothing is recorded without it (privacy & security §5.2 AC-1).
// The service manager restarts the daemon, which shows the icon again.
var ErrIconGone = errors.New("tray: the icon went away, so recording stopped")

// Status is the recording state the icon shows.
type Status struct {
	// Paused is true while nothing is recorded: a pause or private hours.
	Paused bool
	// Text is the menu's first line, e.g. "Recording" or "Paused until 15:30".
	Text string
	// Reminder is true while the monthly "what's collected" reminder is
	// due: the menu shows Review, and on macOS the menu bar says so too.
	Reminder bool
	// Nudge is the day's nudge while it may show; the zero Nudge for none.
	// It shows as a menu item, and on macOS beside the icon unless the
	// reminder is due. It never shows in the first line or the tooltip.
	Nudge Nudge
}

// Nudge is a nudge as the menu shows it.
type Nudge struct {
	Kind  string // what Snooze and TurnOff act on
	Title string // beside the icon
	Text  string // the menu item
}

// PauseFor is how long a pause from the menu lasts.
type PauseFor int

// The menu's pauses.
const (
	PauseHour PauseFor = iota
	PauseTomorrow
	PauseUntilResumed
)

// Range is what a delete from the menu removes.
type Range int

// The menu's deletes.
const (
	LastHour Range = iota
	Today
)

// Menu is what the menu does. Its functions run off the menu's own
// goroutine, one at a time; the outcome shows in the first line for a few
// seconds.
type Menu struct {
	Status        func() Status
	Pause         func(PauseFor) error
	Resume        func() error
	Delete        func(Range) error
	OpenDashboard func() error
	// Review opens what's collected and marks the reminder seen.
	Review func() error
	// SnoozeNudge hides a nudge of kind until tomorrow; TurnOffNudge hides
	// that kind for good.
	SnoozeNudge  func(kind string) error
	TurnOffNudge func(kind string) error
	Log          *slog.Logger
}

// Work is what runs while the icon shows. shown reports whether the icon is
// visible: collectors that watch the screen must not run when it isn't.
type Work func(ctx context.Context, shown bool) error

// Run shows the icon, then runs work until it returns, and removes the
// icon. Call it on the main goroutine: on macOS the menu bar belongs to the
// main thread. It returns work's error. On Windows and Linux, if the icon
// goes away while work runs, work's context is cancelled and Run returns
// ErrIconGone. The standard logger goes to m.Log
// from then on.
func Run(ctx context.Context, m Menu, work Work) error {
	logTo(m.Log)
	return run(ctx, m, work)
}
