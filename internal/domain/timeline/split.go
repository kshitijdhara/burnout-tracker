package timeline

import (
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/classify"
	"github.com/kshitijdhara/burntout/internal/domain/model"
)

// MaxSplitDays bounds how many logical days one interval may be split into.
// Callers validate durations long before this; it is a last line of defence
// so a bad timestamp can never turn into millions of pieces.
const MaxSplitDays = 62

// SplitAtRollover splits iv at logical-day boundaries (local midnight plus
// rollover, in iv.Start's location) and fills LogicalDay and UTCOffsetMin on
// each piece. DST changes are handled because boundaries are computed with
// time.Date in the interval's location. An interval spanning more than
// MaxSplitDays is truncated to its last MaxSplitDays days.
func SplitAtRollover(iv model.Interval, rollover time.Duration) []model.Interval {
	if earliest := iv.End.AddDate(0, 0, -MaxSplitDays); iv.Start.Before(earliest) {
		iv.Start = earliest
	}
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

// ShellInterval converts a finished shell command into intervals. group is
// the command's group from the classifier; it is recorded as an attribute,
// not as an activity category, because the two taxonomies differ.
func ShellInterval(c model.ShellCommand, group classify.CommandCategory, rollover time.Duration) []model.Interval {
	exit := c.ExitCode
	return SplitAtRollover(model.Interval{
		Start: c.Start, End: maxTime(c.End, c.Start.Add(time.Millisecond)),
		Kind: model.IntervalShellCmd,
		Attrs: model.IntervalAttrs{
			Argv0: c.Argv0, Subcommand: c.Subcommand, ExitCode: &exit, ProjectKey: c.ProjectKey, Group: string(group),
		},
	}, rollover)
}
