//go:build !darwin || cgo

package tray

import (
	"context"
	"slices"
	"time"

	"fyne.io/systray"
)

const (
	// refreshEvery is how often the menu rereads the status, so a pause
	// from the CLI or the start of private hours shows within seconds.
	refreshEvery = 2 * time.Second
	// noteFor is how long an action's outcome replaces the first line.
	noteFor = 6 * time.Second
	// tooltip names the icon.
	tooltip = "BurntOut"
	// reviewTitle sits beside the icon while the reminder is due, where the
	// platform shows titles (the macOS menu bar; most Linux panels don't).
	reviewTitle = "Review"
)

// action is a menu item's request, run off the menu goroutine.
type action struct {
	run  func() error
	done string // the first line after it succeeds; "" shows the status
	fail string // the first line after it fails
}

// actions are the menu's actions; the nudge's act on nudge, the kind the
// menu shows.
func (m Menu) actions(nudge string) map[string]action {
	return map[string]action{
		"pause-hour":     {func() error { return m.Pause(PauseHour) }, "", "Couldn't pause"},
		"pause-tomorrow": {func() error { return m.Pause(PauseTomorrow) }, "", "Couldn't pause"},
		"pause":          {func() error { return m.Pause(PauseUntilResumed) }, "", "Couldn't pause"},
		"resume":         {m.Resume, "", "Couldn't resume"},
		"delete-hour":    {func() error { return m.Delete(LastHour) }, "Deleted the last hour", "Couldn't delete the last hour"},
		"delete-today":   {func() error { return m.Delete(Today) }, "Deleted today", "Couldn't delete today"},
		"dashboard":      {m.OpenDashboard, "", "Couldn't open the dashboard"},
		"review":         {m.Review, "", "Couldn't open what's collected"},
		"nudge-snooze":   {func() error { return m.SnoozeNudge(nudge) }, "Snoozed until tomorrow", "Couldn't snooze"},
		"nudge-off":      {func() error { return m.TurnOffNudge(nudge) }, "You won't see that one again", "Couldn't turn it off"},
	}
}

// view is what the menu shows at one moment.
type view struct {
	Paused   bool   // which icon, and Resume instead of the pauses
	Line     string // the first line
	Busy     bool   // an action is running: the action items are off
	Reminder bool   // the review item and the title beside the icon
	Nudge    Nudge  // the nudge item and, without the reminder, the title
}

// title is what shows beside the icon: the reminder before the nudge,
// because AC-4 must not be crowded out.
func (v view) title() string {
	if v.Reminder {
		return reviewTitle
	}
	return v.Nudge.Title
}

// state is the menu's own state between refreshes: the running action and
// the outcome of the last one.
type state struct {
	busy      bool
	note      string
	noteUntil time.Time
}

func (s *state) view(st Status, now time.Time) view {
	line := st.Text
	if s.note != "" && now.Before(s.noteUntil) {
		line = s.note
	}
	return view{Paused: st.Paused, Line: line, Busy: s.busy, Reminder: st.Reminder, Nudge: st.Nudge}
}

// finished records an action's outcome.
func (s *state) finished(a action, err error, now time.Time) {
	s.busy = false
	s.note = a.done
	if err != nil {
		s.note = a.fail
	}
	s.noteUntil = now.Add(noteFor)
}

// icons are the platform's icon images.
type icons struct {
	recording, paused []byte
	template          bool // macOS template images, recoloured by the system
}

func (ic icons) set(paused bool) {
	img := ic.recording
	if paused {
		img = ic.paused
	}
	if ic.template {
		systray.SetTemplateIcon(img, img)
		return
	}
	systray.SetIcon(img)
}

type item struct {
	key string
	mi  *systray.MenuItem
}

// show builds the menu and keeps it current until ctx ends. It runs once
// the tray is ready, on its own goroutine.
func show(ctx context.Context, m Menu, ic icons) {
	systray.SetTooltip(tooltip)
	first := systray.AddMenuItem("", "")
	first.Disable()
	add := func(key, title, tip string) item { return item{key, systray.AddMenuItem(title, tip)} }
	review := add("review", "Review what's collected…", "Your monthly look at what BurntOut records, with pause, settings and uninstall")
	nudge := add("nudge", "", "")
	nudgeItems := []item{
		{"nudge-snooze", nudge.mi.AddSubMenuItem("Snooze until tomorrow", "")},
		{"nudge-off", nudge.mi.AddSubMenuItem("Don't show this kind again", "You can turn it back on with `burntout config`")},
	}
	systray.AddSeparator()
	pauses := []item{
		add("pause-hour", "Pause for an hour", ""),
		add("pause-tomorrow", "Pause until tomorrow", ""),
		add("pause", "Pause until I resume", ""),
	}
	resume := add("resume", "Resume", "")
	systray.AddSeparator()
	deletes := []item{
		add("delete-hour", "Delete the last hour", "Delete everything recorded in the last hour, for good"),
		add("delete-today", "Delete today", "Delete everything recorded today, for good"),
	}
	systray.AddSeparator()
	dash := add("dashboard", "Open dashboard", "Your data in the browser, from this device only")
	all := slices.Concat([]item{review, nudge}, nudgeItems, pauses, []item{resume}, deletes, []item{dash})

	// One goroutine per item turns its clicks into keys.
	clicks := make(chan string)
	for _, it := range all {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-it.mi.ClickedCh:
					select {
					case clicks <- it.key:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

	var (
		st      state
		shown   *view
		results = make(chan error, 1)
		running action
	)
	refresh := func() {
		v := st.view(m.Status(), time.Now())
		if shown == nil || v.Paused != shown.Paused {
			ic.set(v.Paused)
			for _, p := range pauses {
				visible(p.mi, !v.Paused)
			}
			visible(resume.mi, v.Paused)
		}
		if shown == nil || v.Reminder != shown.Reminder {
			visible(review.mi, v.Reminder)
		}
		if shown == nil || v.Nudge != shown.Nudge {
			nudge.mi.SetTitle(v.Nudge.Text)
			visible(nudge.mi, v.Nudge.Kind != "")
		}
		if shown == nil || v.title() != shown.title() {
			systray.SetTitle(v.title())
		}
		if shown == nil || v.Line != shown.Line {
			first.SetTitle(v.Line)
			systray.SetTooltip(tooltip + ": " + v.Line)
		}
		if shown == nil || v.Busy != shown.Busy {
			for _, it := range all {
				enabled(it.mi, !v.Busy)
			}
		}
		shown = &v
	}

	refresh()
	tick := time.NewTicker(refreshEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			refresh()
		case key := <-clicks:
			a, ok := m.actions(shown.Nudge.Kind)[key]
			if !ok || st.busy {
				continue
			}
			st.busy, running = true, a
			refresh()
			go func() { results <- a.run() }()
		case err := <-results:
			if err != nil {
				m.Log.Warn("tray action failed", "action", running.fail, "err", err)
			}
			st.finished(running, err, time.Now())
			refresh()
		}
	}
}

func visible(mi *systray.MenuItem, on bool) {
	if on {
		mi.Show()
	} else {
		mi.Hide()
	}
}

func enabled(mi *systray.MenuItem, on bool) {
	if on {
		mi.Enable()
	} else {
		mi.Disable()
	}
}
