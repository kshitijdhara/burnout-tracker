package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// mediaMinS is how much of a minute a call or video must hold presence for
// the minute to be tagged with it.
const mediaMinS = 30

// runTimeline prints a day as runs of minutes, so it can be checked
// against the user's own notes of what they did.
func runTimeline(ctx context.Context, args []string) error {
	day := model.LogicalDay(time.Now(), rollover)
	switch len(args) {
	case 0:
	case 1:
		if _, err := time.Parse(time.DateOnly, args[0]); err != nil {
			return fmt.Errorf("timeline: want a day as YYYY-MM-DD, got %q", args[0])
		}
		day = args[0]
	default:
		return errors.New("usage: burnout timeline [YYYY-MM-DD]")
	}
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
	minutes, err := store.DayMinutes(ctx, day)
	if err != nil {
		return err
	}
	printTimeline(os.Stdout, day, minutes)
	return nil
}

// run is consecutive minutes with the same label and media tag.
type run struct {
	start, end time.Time
	label, tag string
	cat        model.Category
	attendedS  float64
}

func toRuns(minutes []sqlite.MinuteActivity) []run {
	var runs []run
	for _, m := range minutes {
		tag := ""
		switch {
		case m.CallS >= mediaMinS:
			tag = "call"
		case m.VideoS >= mediaMinS:
			tag = "video"
		}
		end := m.Minute.Add(time.Minute)
		if n := len(runs); n > 0 {
			last := &runs[n-1]
			if last.end.Equal(m.Minute) && last.label == m.Label && last.tag == tag {
				last.end = end
				last.attendedS += m.AttendedS
				continue
			}
		}
		runs = append(runs, run{start: m.Minute, end: end, label: m.Label, tag: tag, cat: m.Category, attendedS: m.AttendedS})
	}
	return runs
}

func printTimeline(w io.Writer, day string, minutes []sqlite.MinuteActivity) {
	_, _ = fmt.Fprintf(w, "Timeline for %s, minute by minute (the app holding most attention each minute)\n", day)
	if len(minutes) == 0 {
		_, _ = fmt.Fprintln(w, "  Nothing recorded for this day.")
		return
	}
	var prevEnd time.Time
	total := 0.0
	for _, r := range toRuns(minutes) {
		if !prevEnd.IsZero() && r.start.After(prevEnd) {
			_, _ = fmt.Fprintf(w, "  %s–%s %7s  away, locked or idle\n", clock(prevEnd), clock(r.start), span(r.start.Sub(prevEnd)))
		}
		label := printable(r.label)
		switch {
		case label == sqlite.UnattributedLabel:
			label = "present, no clear window"
		case string(r.cat) != label:
			label += " (" + string(r.cat) + ")"
		}
		if r.tag != "" {
			label += " · " + r.tag
		}
		_, _ = fmt.Fprintf(w, "  %s–%s %7s  %s\n", clock(r.start), clock(r.end), span(r.end.Sub(r.start)), label)
		prevEnd = r.end
		total += r.attendedS
	}
	_, _ = fmt.Fprintf(w, "Attended in total: %s\n", hm(total))
}

func clock(t time.Time) string { return t.Format("15:04") }

// span renders a duration as 7m or 1h05m.
func span(d time.Duration) string {
	m := int(d.Round(time.Minute).Minutes())
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%02dm", m/60, m%60)
}
