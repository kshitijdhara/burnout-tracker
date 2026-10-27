// Package tray shows the menu-bar icon (macOS) or notification-area icon
// (Windows, Linux) while the daemon runs, and its menu: whether anything
// is being recorded, pause and resume, delete the last hour or today, and
// open the dashboard (privacy & security §5.1). The icon has no hide or
// quit item: while a collector runs it stays visible (§5.2 AC-1).
package tray

import (
	"context"
	"log/slog"
)

// Status is the recording state the icon shows.
type Status struct {
	// Paused is true while nothing is recorded: a pause or private hours.
	Paused bool
	// Text is the menu's first line, e.g. "Recording" or "Paused until 15:30".
	Text string
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
	Log           *slog.Logger
}

// Work is what runs while the icon shows. shown reports whether the icon is
// visible: collectors that watch the screen must not run when it isn't.
type Work func(ctx context.Context, shown bool) error

// Run shows the icon, then runs work until it returns, and removes the
// icon. Call it on the main goroutine: on macOS the menu bar belongs to the
// main thread. It returns work's error. The standard logger goes to m.Log
// from then on.
func Run(ctx context.Context, m Menu, work Work) error {
	logTo(m.Log)
	return run(ctx, m, work)
}
