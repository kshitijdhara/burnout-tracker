package main

import (
	"context"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/control"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/tray"
	"github.com/kshitijdhara/burnout-tracker/internal/app"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/boundary"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/nudge"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/strain"
)

const (
	// presenceFresh is how old the last snapshot may be before calls and
	// video count as unknown, and no nudge shows.
	presenceFresh = 15 * time.Second
	// activeIdle is the idle time after which the user isn't working.
	activeIdle = 5 * time.Minute
)

// presence is what the latest desktop snapshot said about the user, for
// the tray's nudges.
type presence struct {
	at     time.Time
	active bool
	call   bool
	video  bool
}

// lastPresence is the latest snapshot's presence; nil until the desktop
// collector reports (never in safe mode).
var lastPresence atomic.Pointer[presence]

// latestGuardrails are the limits the latest scored day crossed, from the
// last maintenance.
var latestGuardrails atomic.Pointer[scoredGuardrails]

type scoredGuardrails struct {
	day   string // logical day
	names []string
}

// guardrailsFresh is how many days a scored day's guardrails speak for:
// yesterday's, or the day before after a quiet day.
const guardrailsFresh = 2

// presenceCollector records each snapshot's presence on its way to the
// daemon.
type presenceCollector struct{ ports.Collector }

func (c presenceCollector) Snapshot(ctx context.Context) (model.Snapshot, error) {
	s, err := c.Collector.Snapshot(ctx)
	if err == nil {
		p := &presence{at: s.At, active: !s.Locked && s.IdleFor < activeIdle, call: s.CallActive}
		p.video = slices.ContainsFunc(s.Windows, func(w model.Window) bool { return w.HasVideo })
		lastPresence.Store(p)
	}
	return s, err
}

// publishGuardrails keeps the latest scored day's guardrails for nudges.
func publishGuardrails(scores []strain.Score) {
	for i := len(scores) - 1; i >= 0; i-- {
		if scores[i].Status != strain.StatusOK {
			continue
		}
		names := make([]string, 0, len(scores[i].Guardrails))
		for _, g := range scores[i].Guardrails {
			names = append(names, g.Name)
		}
		latestGuardrails.Store(&scoredGuardrails{day: scores[i].Date, names: names})
		return
	}
}

// boundariesEvery is how often the daemon rereads the boundaries for
// nudges. A new rule applies from the next day, so this only needs to be
// well within a day.
const boundariesEvery = 5 * time.Minute

// latestBoundaries are the stored boundaries, history included.
var latestBoundaries atomic.Pointer[[]boundary.Boundary]

// boundaryLoop keeps latestBoundaries current until ctx ends. A failed
// read keeps the last boundaries.
func boundaryLoop(ctx context.Context, log *slog.Logger, store *sqlite.Store) {
	for {
		if bs, err := store.Boundaries(ctx); err != nil {
			if ctx.Err() == nil {
				log.Warn("could not read boundaries for nudges", "err", err)
			}
		} else {
			latestBoundaries.Store(&bs)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(boundariesEvery):
		}
	}
}

// nudgeBoundaries are the boundaries that apply on the logical day.
func nudgeBoundaries(day string) nudge.Boundaries {
	var nb nudge.Boundaries
	bs := latestBoundaries.Load()
	if bs == nil {
		return nb
	}
	for _, b := range *bs {
		if !b.ActiveOn(day) {
			continue
		}
		switch b.Kind {
		case boundary.Shutdown:
			nb.Shutdown, nb.HasShutdown = b.ShutdownAt(), true
		case boundary.NoWeekend:
			nb.WeekendsOff = true
		}
	}
	return nb
}

// trayNudge is the nudge the tray shows at now, if any (system design
// §8.3), and records the day's first one. Everything unknown, from an
// unreadable state file to a quiet collector, shows nothing.
func trayNudge(dataDir string, c config.Config, now time.Time, paused, private bool) tray.Nudge {
	n, err := control.ReadNudges(dataDir)
	if err != nil {
		return tray.Nudge{}
	}
	s := c.NudgeSettings()
	for _, k := range n.Off {
		s.Off[nudge.Kind(k)] = true
	}
	at := nudge.Now{At: now, Day: model.LogicalDay(now, app.DefaultConfig().Rollover), Paused: paused, Private: private}
	if p := lastPresence.Load(); p != nil && !safeMode.Load() && now.Sub(p.at) < presenceFresh && !p.at.After(now) {
		at.Known, at.Active, at.InCall, at.Video = true, p.active, p.call, p.video
	}
	var guardrails []string
	if g := latestGuardrails.Load(); g != nil {
		if d, err := time.Parse(time.DateOnly, g.day); err == nil {
			if t, _ := time.Parse(time.DateOnly, at.Day); t.Sub(d) <= guardrailsFresh*24*time.Hour {
				guardrails = g.names
			}
		}
	}
	today := nudge.Today{Day: n.Day, Kind: nudge.Kind(n.Kind), Snoozed: n.Snoozed}
	d := nudge.Decide(s, nudgeBoundaries(at.Day), guardrails, today, at)
	if !d.Show {
		return tray.Nudge{}
	}
	if n.Day != at.Day {
		if err := control.ShowNudge(dataDir, at.Day, string(d.Kind)); err != nil {
			return tray.Nudge{} // unrecorded, it could be followed by another today
		}
	}
	cp := nudge.CopyOf(d.Kind)
	return tray.Nudge{Kind: string(d.Kind), Title: cp.Title, Text: cp.Text}
}

// snoozeNudge and turnOffNudge are the nudge's menu items.
func snoozeNudge(dataDir, kind string, now time.Time) error {
	if kind == "" {
		return nil
	}
	return control.SnoozeNudge(dataDir, model.LogicalDay(now, app.DefaultConfig().Rollover), kind)
}

func turnOffNudge(dataDir, kind string, now time.Time) error {
	if kind == "" {
		return nil
	}
	return control.NudgeOff(dataDir, model.LogicalDay(now, app.DefaultConfig().Rollover), kind)
}
