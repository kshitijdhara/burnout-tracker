package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/app"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/checkin"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/review"
)

// runReview shows the weekly review: the last 7 completed days against the
// 7 before, recurring drivers, limits crossed, check-ins and suggestions.
func runReview(ctx context.Context, _ []string) error {
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	store, err := sqlite.OpenReadOnly(ctx, dataDir)
	if errors.Is(err, sqlite.ErrNoDatabase) {
		fmt.Println("Nothing recorded yet. Run `burnout setup`, then `burnout service install`.")
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	now := time.Now()
	rollover := app.DefaultConfig().Rollover
	sc, err := app.ScoreDays(ctx, store, now, rollover, false)
	if err != nil {
		return err
	}
	records, err := store.Checkins(ctx, now.AddDate(0, -3, 0))
	if err != nil {
		return err
	}
	w := review.Build(sc.Scores, records, model.LogicalDay(now, rollover))
	due, _ := review.WeeklyDue(records, now)
	fmt.Print(formatReview(w, due))
	return nil
}

func formatReview(w review.Week, checkinDue bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Week of %s to %s\n\n", w.From, w.To)
	if w.Calibrating {
		fmt.Fprintf(&b, "Strain: still calibrating (%d of 14 days with enough data), so there's no week to compare yet.\n",
			w.CalibratedOf)
	} else {
		fmt.Fprintf(&b, "Strain: %.0f on average over %d scored days", w.Average, w.Scored)
		if w.PrevScored > 0 {
			fmt.Fprintf(&b, " (the week before: %.0f, %+.0f)", w.PrevAverage, w.Average-w.PrevAverage)
		}
		fmt.Fprintf(&b, "\n  Highest: %s, %.0f (%s)\n", w.Peak.Date, w.Peak.Strain, strainBand(w.Peak.Strain))
	}
	if len(w.Drivers) > 0 {
		b.WriteString("\nWhat kept coming up:\n")
		for _, d := range w.Drivers[:min(3, len(w.Drivers))] {
			fmt.Fprintf(&b, "  ↑ %s (%s)\n", d.Label, days(d.Days))
		}
	}
	if len(w.Guardrails) > 0 {
		b.WriteString("\nLimits crossed:\n")
		for _, g := range w.Guardrails {
			fmt.Fprintf(&b, "  ! %s (%s)\n", g.Label, days(g.Days))
		}
	}
	b.WriteString("\nCheck-in: ")
	switch {
	case len(w.Checkin) == 0:
		b.WriteString("none yet.")
	default:
		var parts []string
		for _, id := range []string{checkin.CBIWorkID, checkin.CBIPersonalID} {
			r, ok := w.Checkin[id]
			if !ok {
				continue
			}
			inst, _ := checkin.ByID(id)
			part := fmt.Sprintf("%s %.0f (%s)", strings.ToLower(inst.Name), r.Score, checkin.BandOf(r.Score))
			if p, ok := w.PrevCheckin[id]; ok {
				part += fmt.Sprintf(", before %.0f", p.Score)
			}
			parts = append(parts, part)
		}
		b.WriteString(strings.Join(parts, "; ") + ".")
	}
	if checkinDue {
		b.WriteString(" This week's is due: `burnout checkin` (about a minute).")
	}
	b.WriteString("\n")
	if len(w.Suggestions) > 0 {
		b.WriteString("\nOne or two things to try:\n")
		for _, s := range w.Suggestions {
			fmt.Fprintf(&b, "  • %s\n", s)
		}
	}
	return b.String()
}

func days(n int) string {
	if n == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", n)
}
