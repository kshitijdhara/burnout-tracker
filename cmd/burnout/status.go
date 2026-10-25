package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/app"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/review"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/strain"
)

func runStatus(ctx context.Context, _ []string) error {
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	store, err := sqlite.OpenReadOnly(ctx, dataDir)
	if errors.Is(err, sqlite.ErrNoDatabase) {
		fmt.Println("Nothing recorded yet. Run `burnout setup`, then `burnout service install` (or `burnout daemon` in a terminal).")
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	now := time.Now()
	day := model.LogicalDay(now, app.DefaultConfig().Rollover)
	if sc, err := app.ScoreDays(ctx, store, now, app.DefaultConfig().Rollover, false); err != nil {
		fmt.Printf("Strain: unavailable (%v)\n\n", err)
	} else {
		fmt.Println(formatStrain(sc.Scores, day))
	}
	if records, err := store.Checkins(ctx, now.AddDate(0, -3, 0)); err == nil {
		if due, _ := review.WeeklyDue(records, now); due {
			fmt.Print("Weekly check-in due: run `burnout checkin` (about a minute).\n\n")
		}
	}
	s, err := store.DaySummary(ctx, day)
	if err != nil {
		return err
	}
	printSummary(s)
	return nil
}

// strainBand names a strain value's range; 70 starts the high-strain zone
// (system design §7.3).
func strainBand(v float64) string {
	switch {
	case v >= 70:
		return "high"
	case v >= 40:
		return "elevated"
	default:
		return "low"
	}
}

// formatStrain describes the latest completed day's strain, or how far
// calibration has come, and the last 7 days. scores are oldest first and
// end before today.
func formatStrain(scores []strain.Score, today string) string {
	var b strings.Builder
	if len(scores) == 0 {
		return "Strain: no completed days recorded yet.\n"
	}
	last := scores[len(scores)-1]
	var latestOK *strain.Score
	for i := len(scores) - 1; i >= 0; i-- {
		if scores[i].Status == strain.StatusOK {
			latestOK = &scores[i]
			break
		}
	}
	need := strain.DefaultParams().CalibrationDays
	switch {
	case latestOK == nil:
		fmt.Fprintf(&b, "Strain: calibrating, %d of %d days with enough data so far.\n", last.UsableDays, need)
		b.WriteString("  Scores start once burnout knows your usual weekdays and weekends.\n")
		if last.Status == strain.StatusMissing {
			fmt.Fprintf(&b, "  %s didn't count: %s.\n", last.Date, last.Reason)
		}
	case latestOK == &scores[len(scores)-1]:
		fmt.Fprintf(&b, "Strain on %s: %.0f/100, %s\n", last.Date, last.Strain, strainBand(last.Strain))
	default:
		fmt.Fprintf(&b, "Strain: no score for %s (%s). Last score: %.0f/100 on %s, %s\n",
			last.Date, last.Reason, latestOK.Strain, latestOK.Date, strainBand(latestOK.Strain))
	}
	if last.Status == strain.StatusOK {
		for _, d := range last.Drivers {
			fmt.Fprintf(&b, "  ↑ %s\n", d.Text())
		}
	}
	for _, g := range last.Guardrails {
		fmt.Fprintf(&b, "  ! %s\n", g.Text)
	}
	byDate := make(map[string]strain.Score, len(scores))
	for _, sc := range scores {
		byDate[sc.Date] = sc
	}
	t, _ := time.Parse(time.DateOnly, today)
	b.WriteString("  Last 7 days:")
	for back := 7; back >= 1; back-- {
		d := t.AddDate(0, 0, -back)
		v := "–"
		if sc, ok := byDate[d.Format(time.DateOnly)]; ok && sc.Status == strain.StatusOK {
			v = fmt.Sprintf("%.0f", sc.Strain)
		}
		fmt.Fprintf(&b, " %s %s", d.Weekday().String()[:3], v)
	}
	b.WriteString("\n  (Compared with your own usual days. A signal for reflection, not a diagnosis.)\n")
	return b.String()
}

func printSummary(s sqlite.DaySummary) {
	total := 0.0
	for _, v := range s.CategorySeconds {
		total += v
	}
	fmt.Printf("Today (%s): %s attended\n", s.LogicalDay, hm(total))
	if total == 0 && s.ShellCommands == 0 && s.AgentSessions == 0 {
		fmt.Println("  Nothing recorded yet. Is the daemon running? Try `burnout doctor`.")
		return
	}
	for _, c := range slices.Sorted(maps.Keys(s.CategorySeconds)) {
		fmt.Printf("  %-16s %s\n", c, hm(s.CategorySeconds[c]))
	}
	if s.UnattributedSeconds > 0 {
		fmt.Printf("  %-16s %s\n", "unattributed", hm(s.UnattributedSeconds))
	}
	if len(s.TopApps) > 0 {
		fmt.Println("Top apps:")
		for _, a := range s.TopApps {
			fmt.Printf("  %-32s %s\n", printable(a.Name), hm(a.Seconds))
		}
	}
	if s.ShellCommands > 0 {
		fmt.Printf("Shell: %d commands, %d failed\n", s.ShellCommands, s.ShellFailures)
		for _, c := range s.TopCommands {
			name := c.Name
			if name == "" {
				name = "(unrecognised)"
			}
			fmt.Printf("  %-16s %d\n", printable(name), c.Count)
		}
	}
	if s.AgentSessions > 0 {
		fmt.Printf("Agents: %d sessions\n", s.AgentSessions)
		for _, k := range slices.Sorted(maps.Keys(s.AgentStateSeconds)) {
			fmt.Printf("  agent %-18s %s\n", k, hm(s.AgentStateSeconds[k]))
		}
		for _, k := range slices.Sorted(maps.Keys(s.HumanStateSeconds)) {
			fmt.Printf("  you   %-18s %s\n", k, hm(s.HumanStateSeconds[k]))
		}
	}
}

// printable makes a stored label safe to print to a terminal: control and
// invisible format characters (escape sequences, bidi overrides) become
// visible escapes. Collectors drop them already; this covers older data.
func printable(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			fmt.Fprintf(&b, "\\u%04x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func hm(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
