package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/control"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/dashboard"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/tray"
	"github.com/kshitijdhara/burnout-tracker/internal/app"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
)

// trayMenu is the tray icon's menu (privacy & security §5.1). It pauses
// and forgets the way the CLI does, through the control files the daemon's
// loop reads, so the menu and `burnout pause` or `forget` can't disagree.
func trayMenu(log *slog.Logger, dataDir string, settings *liveConfig, dash *trayDashboard) tray.Menu {
	rollover := app.DefaultConfig().Rollover
	return tray.Menu{
		Status: func() tray.Status { return trayStatus(dataDir, settings.current(), time.Now()) },
		Pause: func(p tray.PauseFor) error {
			now := time.Now()
			var until time.Time // zero: until resumed
			switch p {
			case tray.PauseHour:
				until = now.Add(time.Hour)
			case tray.PauseTomorrow:
				until, _ = pauseUntil("tomorrow", now, rollover)
			}
			return control.SetPause(dataDir, until)
		},
		Resume: func() error { return control.Resume(dataDir) },
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
			return control.SetReminderSeen(dataDir, time.Now())
		},
		Log: log,
	}
}

// trayStatus is what the icon shows: paused (by the user or private
// hours) or recording, and whether the monthly reminder is due. The
// reminder shows whatever the recording state (AC-4).
func trayStatus(dataDir string, c config.Config, now time.Time) tray.Status {
	st := tray.Status{Text: "Recording"}
	if paused, p := control.PauseState(dataDir, now); paused {
		st = tray.Status{Paused: true, Text: "Paused " + describePause(p, now)}
	} else if c.PrivateHours().Contains(now) {
		st = tray.Status{Paused: true, Text: "Private hours: nothing is recorded"}
	}
	consented, _ := consentedAt()
	seen, _ := control.ReminderSeen(dataDir)
	st.Reminder = reminderDue(consented, seen, now)
	return st
}

// trayDashboard is the dashboard the tray opens. The daemon serves it,
// read-only, from the first click until it stops; each click opens it with
// a new one-time link.
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
		srv, err := dashboard.Listen(dashSource{store: store, dataDir: t.dataDir}, t.log)
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
	return openBrowser(t.srv.LaunchURLTo(view))
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
