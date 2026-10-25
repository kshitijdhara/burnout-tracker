package app

import (
	"context"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/features"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/strain"
)

// ScoreStore is where scoring reads its inputs.
type ScoreStore interface {
	DataDays(ctx context.Context, from, to string) ([]string, error)
	DayInput(ctx context.Context, logicalDay string) (features.Day, error)
	FeatureDays(ctx context.Context, from, to string) ([]strain.Day, error)
}

// Scoring horizons: features are kept two years (privacy & security §3),
// but can only be computed while the raw minutes and intervals behind them
// are kept (180 days). The most recent days are always recomputed, because
// a day's last minutes can reach the store after it ends.
const (
	featureHistory = 730
	rawHistory     = 180
	recomputeDays  = 2
)

// Scoring is one pass over the history.
type Scoring struct {
	// Scores has one score per day with data, oldest first; the last is
	// today's when it was asked for.
	Scores []strain.Score
	// Computed are completed days whose features weren't stored yet (or
	// were recomputed). The daemon saves them; read-only callers don't.
	Computed []strain.Day
}

// ScoreDays scores every day with data up to now. Today's features are
// partial, so it is only included when includeToday is set, and is never
// in Computed.
func ScoreDays(ctx context.Context, st ScoreStore, now time.Time, rollover time.Duration, includeToday bool) (Scoring, error) {
	today := model.LogicalDay(now, rollover)
	todayDate, _ := time.Parse(time.DateOnly, today)
	day := func(back int) string { return todayDate.AddDate(0, 0, -back).Format(time.DateOnly) }
	yesterday := day(1)

	stored, err := st.FeatureDays(ctx, day(featureHistory), yesterday)
	if err != nil {
		return Scoring{}, err
	}
	have := make(map[string]bool, len(stored))
	for _, d := range stored {
		have[d.Date] = true
	}
	withData, err := st.DataDays(ctx, day(rawHistory), today)
	if err != nil {
		return Scoring{}, err
	}
	var out Scoring
	days := make([]strain.Day, 0, len(stored)+recomputeDays+1)
	recomputeFrom := day(recomputeDays)
	for _, d := range stored {
		if d.Date < recomputeFrom {
			days = append(days, d)
		}
	}
	for _, date := range withData {
		isToday := date == today
		if (isToday && !includeToday) || (!isToday && have[date] && date < recomputeFrom) {
			continue
		}
		in, err := st.DayInput(ctx, date)
		if err != nil {
			return Scoring{}, err
		}
		d := strain.Day{
			Date: date, Coverage: in.Coverage, Present: in.Present,
			Values: features.Values(in, features.DefaultParams()),
		}
		days = append(days, d)
		if !isToday {
			out.Computed = append(out.Computed, d)
		}
	}
	out.Scores = strain.Compute(days, strain.DefaultParams())
	return out, nil
}
