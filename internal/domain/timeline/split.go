package timeline

import (
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// SplitAtRollover splits iv at logical-day boundaries (local midnight plus
// rollover, in iv.Start's location) and fills LogicalDay and UTCOffsetMin on
// each piece. DST changes are handled because boundaries are computed with
// time.Date in the interval's location.
func SplitAtRollover(iv model.Interval, rollover time.Duration) []model.Interval {
	var out []model.Interval
	for iv.End.After(iv.Start) {
		next := nextBoundary(iv.Start, rollover)
		piece := iv
		if next.Before(iv.End) {
			piece.End = next
		}
		piece.LogicalDay = model.LogicalDay(piece.Start, rollover)
		piece.UTCOffsetMin = model.UTCOffsetMinutes(piece.Start)
		out = append(out, piece)
		iv.Start = piece.End
	}
	return out
}

// nextBoundary returns the first logical-day start strictly after t.
func nextBoundary(t time.Time, rollover time.Duration) time.Time {
	day := t.Add(-rollover)
	y, m, d := day.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, t.Location()).Add(rollover)
}

// ShellInterval converts a finished shell command into intervals. category
// is the command's category from the classifier.
func ShellInterval(c model.ShellCommand, category string, rollover time.Duration) []model.Interval {
	exit := c.ExitCode
	return SplitAtRollover(model.Interval{
		Start: c.Start, End: maxTime(c.End, c.Start.Add(time.Millisecond)),
		Kind: model.IntervalShellCmd, Category: model.Category(category),
		Attrs: model.IntervalAttrs{Argv0: c.Argv0, Subcommand: c.Subcommand, ExitCode: &exit, ProjectKey: c.ProjectKey},
	}, rollover)
}
