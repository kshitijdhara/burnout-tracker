package main

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/control"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/dashboard"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/tray"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
)

// trayMenu is the tray icon's menu (privacy & security §5.1). It pauses
// and forgets the way the CLI does, through the control files the daemon's
// loop reads, so the menu and `burnout pause` or `forget` can't disagree.
func trayMenu(log *slog.Logger, dataDir string, ts *trayState, dash *trayDashboard) tray.Menu {
	// then refreshes the slower part of the status after the actions that
	// change it, so the menu shows the outcome at once.
	then := func(err error) error {
		if err == nil {
			ts.refresh(time.Now())
		}
		return err
	}
	return tray.Menu{
		Status: func() tray.Status { return ts.status(time.Now()) },
		Pause: func(p tray.PauseFor) error {
			now := time.Now()
			var until time.Time // zero: until resumed
			switch p {
			case tray.PauseHour:
				until = now.Add(time.Hour)
			case tray.PauseTomorrow:
				until, _ = pauseUntil("tomorrow", now, rollover)
			}
			return then(control.SetPause(dataDir, until))
		},
		Resume: func() error { return then(control.Resume(dataDir)) },
		Delete: func(r tray.Range) error {
			last, today := time.Duration(0), false
			if r == tray.LastHour {
				last = time.Hour
			} else {
				today = true
			}
			from, _, err := forgetFrom(last, today, "", false, time.Now(), rollover)
			if err != nil {
				return err
			}
			return forgetThroughDaemon(dataDir, from)
		},
		OpenDashboard: func() error { return dash.open("") },
		Review: func() error {
			if err := dash.open("privacy"); err != nil {
				return err
			}
			return then(control.SetReminderSeen(dataDir, time.Now()))
		},
		SnoozeNudge:  func(kind string) error { return then(snoozeNudge(dataDir, kind, time.Now())) },
		TurnOffNudge: func(kind string) error { return then(turnOffNudge(dataDir, kind, time.Now())) },
		Log:          log,
	}
}

// trayRefreshEvery is how often the tray rereads the slower part of its
// status: the update notice, the monthly reminder and the day's nudge.
// The menu asks for the status every 2 s; that only reads the pause.
const trayRefreshEvery = time.Minute

// trayState is the tray's status, kept in two parts: the recording state,
// read each time the menu asks, and trayExtras, read by loop.
type trayState struct {
	dataDir  string
	settings func() config.Config
	mu       sync.Mutex // one refresh at a time
	extras   atomic.Pointer[trayExtras]
}

// trayExtras are the slower parts of the tray's status.
type trayExtras struct {
	update   string // the newer release, if the weekly check found one
	reminder bool
	nudges   control.Nudges // the nudge state as recorded
	nudgesOK bool           // false: unreadable, so no nudge shows
}

// loop keeps the extras current until ctx ends.
func (t *trayState) loop(ctx context.Context) {
	for {
		t.refresh(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(trayRefreshEvery):
		}
	}
}

// refresh rereads the extras, recording the day's first nudge.
func (t *trayState) refresh(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	x := readTrayExtras(t.dataDir, t.settings(), now)
	t.extras.Store(&x)
}

// status is what the icon shows at now. It writes nothing.
func (t *trayState) status(now time.Time) tray.Status {
	var x trayExtras
	if p := t.extras.Load(); p != nil {
		x = *p
	}
	return trayStatus(t.dataDir, t.settings(), now, x)
}

// readTrayExtras reads a newer release if the weekly update check found
// one, whether the monthly reminder is due, and the nudge state, recording
// the day's first nudge (recordNudge).
func readTrayExtras(dataDir string, c config.Config, now time.Time) trayExtras {
	var x trayExtras
	if v, ok := newerRelease(dataDir, c); ok {
		x.update = v.String()
	}
	consented, _ := consentedAt()
	seen, _ := control.ReminderSeen(dataDir)
	x.reminder = reminderDue(consented, seen, now)
	paused, _ := control.PauseState(dataDir, now)
	x.nudges, x.nudgesOK = recordNudge(dataDir, c, now, paused, c.PrivateHours().Contains(now))
	return x
}

// trayStatus is what the icon shows: paused (by the user or private
// hours), safe mode or recording, then safe mode again if a pause hides
// it, and a newer release; whether the monthly reminder is due, and the
// day's nudge. The reminder shows whatever the recording state (AC-4); the
// nudge only while nothing holds it back (shownNudge), decided afresh at
// each call.
func trayStatus(dataDir string, c config.Config, now time.Time, x trayExtras) tray.Status {
	st := tray.Status{Text: "Recording"}
	paused, p := control.PauseState(dataDir, now)
	private := c.PrivateHours().Contains(now)
	safe := safeMode.Load()
	if paused {
		st = tray.Status{Paused: true, Text: "Paused " + describePause(p, now)}
	} else if private {
		st = tray.Status{Paused: true, Text: "Private hours: nothing is recorded"}
	} else if safe {
		st = tray.Status{Text: "Safe mode: recording hooks only, after repeated crashes"}
	}
	if safe && st.Paused {
		// The desktop collector stays off after the pause (system design §11).
		st.Text += " · safe mode"
	}
	if x.update != "" {
		// Short: it is also the tooltip, which Windows cuts at 128 characters.
		st.Text += " · update " + x.update + " available"
	}
	st.Reminder = x.reminder
	if x.nudgesOK {
		st.Nudge = shownNudge(x.nudges, c, now, paused, private)
	}
	return st
}

// trayDashboard is the dashboard the tray opens. The daemon serves it,
// reading through a read-only connection of its own, from the first click
// until it stops; each click opens it with a new one-time link. Its
// changes go through dashControl, like the CLI's dashboard.
type trayDashboard struct {
	dataDir string
	log     *slog.Logger

	mu     sync.Mutex
	srv    *dashboard.Server
	store  *sqlite.Store
	cancel context.CancelFunc
	done   chan struct{}
}

// open opens the dashboard at view ("" for the default one).
func (t *trayDashboard) open(view string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.srv != nil {
		select {
		case <-t.done: // it stopped on an error: start again
			t.stop()
		default:
		}
	}
	if t.srv == nil {
		ctx, cancel := context.WithCancel(context.Background())
		store, err := sqlite.OpenReadOnly(ctx, t.dataDir)
		if err != nil {
			cancel()
			return err
		}
		srv, err := dashboard.Listen(dashSource{store: store, dataDir: t.dataDir}, dashControl{dataDir: t.dataDir}, t.log)
		if err != nil {
			cancel()
			_ = store.Close()
			return err
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			if err := srv.Serve(ctx); err != nil {
				t.log.Warn("dashboard stopped", "err", err)
			}
		}()
		t.srv, t.store, t.cancel, t.done = srv, store, cancel, done
	}
	return openLaunch(t.dataDir, t.srv.LaunchURLTo(view))
}

// close stops the dashboard, if it was opened.
func (t *trayDashboard) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.srv != nil {
		t.stop()
	}
}

func (t *trayDashboard) stop() {
	t.cancel()
	<-t.done
	_ = t.store.Close()
	t.srv = nil
}
