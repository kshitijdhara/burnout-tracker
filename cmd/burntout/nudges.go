package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/control"
	"github.com/kshitijdhara/burntout/internal/adapters/paths"
	"github.com/kshitijdhara/burntout/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burntout/internal/adapters/tray"
	"github.com/kshitijdhara/burntout/internal/config"
	"github.com/kshitijdhara/burntout/internal/domain/boundary"
	"github.com/kshitijdhara/burntout/internal/domain/model"
	"github.com/kshitijdhara/burntout/internal/domain/nudge"
	"github.com/kshitijdhara/burntout/internal/domain/ports"
	"github.com/kshitijdhara/burntout/internal/domain/strain"
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

// recordNudge decides the tray's nudge at now (system design §8.3) and
// records the day's first one in the data folder. It returns the nudge
// state as recorded, which shownNudge decides from until the next call;
// false when the state file is unreadable, and no nudge shows.
func recordNudge(dataDir string, c config.Config, now time.Time, paused, private bool) (control.Nudges, bool) {
	n, err := control.ReadNudges(dataDir)
	if err != nil {
		return control.Nudges{}, false
	}
	d, day := decideNudge(n, c, now, paused, private)
	if d.Show && n.Day != day {
		if err := control.ShowNudge(dataDir, day, string(d.Kind)); err != nil {
			return n, true // unrecorded, it could be followed by another today: shownNudge won't show it
		}
		n.Day, n.Kind, n.Snoozed = day, string(d.Kind), false
	}
	return n, true
}

// shownNudge is the nudge the tray shows at now, if any: the day's
// recorded nudge, unless something holds it back at now. It writes
// nothing, so the tray can ask every few seconds. Everything unknown, from
// an unreadable state file to a quiet collector, shows nothing.
func shownNudge(n control.Nudges, c config.Config, now time.Time, paused, private bool) tray.Nudge {
	d, day := decideNudge(n, c, now, paused, private)
	if !d.Show || n.Day != day {
		return tray.Nudge{}
	}
	cp := nudge.CopyOf(d.Kind)
	return tray.Nudge{Kind: string(d.Kind), Title: cp.Title, Text: cp.Text}
}

// decideNudge decides the nudge at now from the state n, and returns the
// logical day.
func decideNudge(n control.Nudges, c config.Config, now time.Time, paused, private bool) (nudge.Decision, string) {
	s := c.NudgeSettings()
	for _, k := range n.Off {
		s.Off[nudge.Kind(k)] = true
	}
	// No collector reports screen sharing or Focus yet (each lists it in
	// its limitations), so ScreenShare and Focus stay false.
	at := nudge.Now{At: now, Day: model.LogicalDay(now, rollover), Paused: paused, Private: private}
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
	return nudge.Decide(s, nudgeBoundaries(at.Day), guardrails, today, at), at.Day
}

// snoozeNudge and turnOffNudge are the nudge's menu items.
func snoozeNudge(dataDir, kind string, now time.Time) error {
	if kind == "" {
		return nil
	}
	return control.SnoozeNudge(dataDir, model.LogicalDay(now, rollover), kind)
}

// errConfigInvalid stands in for the config file's own error, which can
// quote excluded sites and apps, where it would reach the daemon log.
var errConfigInvalid = errors.New("the config file is invalid; run `burntout config validate`")

// turnOffNudge turns kind off in the config file, where `burntout config
// set nudges.KIND true` turns it back on, and hides it for the rest of the
// day: the daemon picks the file up within seconds.
func turnOffNudge(dataDir, kind string, now time.Time) error {
	if kind == "" {
		return nil
	}
	path, err := configPath()
	if err != nil {
		return err
	}
	c, err := config.Load(path)
	if err != nil {
		return errConfigInvalid
	}
	if err := c.Set("nudges."+kind, []string{"false"}); err != nil {
		return err
	}
	if err := config.Save(path, c); err != nil {
		return err
	}
	return snoozeNudge(dataDir, kind, now)
}

// nudgeTurnedOn clears a kind the tray of v0.1.0-alpha.1 turned off from
// the data folder, once `burntout config` turns it on in c: from then on
// the file alone decides. key is the config key just changed.
func nudgeTurnedOn(c config.Config, key string) error {
	kind, ok := strings.CutPrefix(key, "nudges.")
	if !ok {
		return nil
	}
	if vals, err := c.Get(key); err != nil || len(vals) != 1 || vals[0] != "true" {
		return nil
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	return control.NudgeOn(dataDir, kind)
}
