package app

import (
	"context"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/annotation"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/features"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/strain"
)

// ScoreStore is where scoring reads its inputs.
type ScoreStore interface {
	DataDays(ctx context.Context, from, to string) ([]string, error)
	DayInput(ctx context.Context, logicalDay string) (features.Day, error)
	FeatureDays(ctx context.Context, from, to, paramsKey string) ([]strain.Day, error)
	// OlderFeatureDays are the stored days FeatureDays leaves out: another
	// registry version, or other feature parameters.
	OlderFeatureDays(ctx context.Context, from, to, paramsKey string) ([]strain.Day, error)
	// RawKeptFrom is the first day whose minutes and intervals are all
	// still kept, or "" for none.
	RawKeptFrom(ctx context.Context) (string, error)
	Annotations(ctx context.Context, from, to string) ([]annotation.Annotation, error)
}

// Scoring horizons: features are kept two years (privacy & security §3),
// but can only be computed while the raw minutes and intervals behind them
// are kept (180 days by default, less if the user shortened retention).
// The most recent days are always recomputed, because a day's last minutes
// can reach the store after it ends.
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

// ScoreOptions say how to score.
type ScoreOptions struct {
	Rollover time.Duration
	// Features are the feature definitions (core hours come from the
	// user's profile); Role weights the features in the score.
	Features features.Params
	Role     features.Role
	// IncludeToday scores today's partial day too.
	IncludeToday bool
}

// DefaultScoreOptions are the defaults: a developer with 09:00–18:00 core
// hours, today left out.
func DefaultScoreOptions() ScoreOptions {
	return ScoreOptions{Rollover: DefaultConfig().Rollover, Features: features.DefaultParams(), Role: features.RoleDeveloper}
}

// ScoreDays scores every day with data up to now. Today's features are
// partial, so it is only included with IncludeToday, and is never in
// Computed. Stored features from another registry version, or computed
// with other feature parameters (the user changed their core hours), are
// recomputed only while all their raw data is kept (RawKeptFrom); before
// that they keep their stored values, so a recompute from partially
// trimmed data never replaces them. A day before that with no stored
// features at all isn't scored, for the same reason.
func ScoreDays(ctx context.Context, st ScoreStore, now time.Time, o ScoreOptions) (Scoring, error) {
	rollover, includeToday := o.Rollover, o.IncludeToday
	today := model.LogicalDay(now, rollover)
	todayDate, _ := time.Parse(time.DateOnly, today)
	day := func(back int) string { return todayDate.AddDate(0, 0, -back).Format(time.DateOnly) }
	yesterday := day(1)

	stored, err := st.FeatureDays(ctx, day(featureHistory), yesterday, o.Features.Key())
	if err != nil {
		return Scoring{}, err
	}
	have := make(map[string]bool, len(stored))
	for _, d := range stored {
		have[d.Date] = true
	}
	older, err := st.OlderFeatureDays(ctx, day(featureHistory), yesterday, o.Features.Key())
	if err != nil {
		return Scoring{}, err
	}
	keptFrom, err := st.RawKeptFrom(ctx)
	if err != nil {
		return Scoring{}, err
	}
	// whole reports whether a day's raw data is all kept, so its features
	// can be (re)computed.
	whole := func(date string) bool { return keptFrom != "" && date >= keptFrom }
	withData, err := st.DataDays(ctx, day(rawHistory), today)
	if err != nil {
		return Scoring{}, err
	}
	var out Scoring
	days := make([]strain.Day, 0, len(stored)+recomputeDays+1)
	recomputeFrom := day(recomputeDays)
	// prior holds every usable day's attended hours so far, for features
	// that look back. A computed day can fill a gap older than stored
	// days, so each day is given only the days before it.
	sp := strain.DefaultParams()
	prior := map[string]float64{}
	remember := func(d strain.Day) {
		if h, ok := d.Values["attended_hours"]; ok && d.Coverage >= sp.MinCoverage && d.Present >= sp.MinPresent {
			prior[d.Date] = h
		}
	}
	for _, d := range stored {
		if d.Date < recomputeFrom {
			days = append(days, d)
			remember(d)
		}
	}
	// Older rows whose raw data is partly or wholly gone keep their stored
	// values: a recompute would see a day with its minutes trimmed as a
	// light one.
	for _, d := range older {
		if d.Date < recomputeFrom && !have[d.Date] && !whole(d.Date) {
			days = append(days, d)
			remember(d)
			have[d.Date] = true
		}
	}
	for _, date := range withData {
		isToday := date == today
		if (isToday && !includeToday) || (!isToday && date < recomputeFrom && (have[date] || !whole(date))) {
			continue
		}
		in, err := st.DayInput(ctx, date)
		if err != nil {
			return Scoring{}, err
		}
		for pd, h := range prior {
			if pd < date {
				in.Prior = append(in.Prior, features.PriorDay{Date: pd, AttendedH: h})
			}
		}
		d := strain.Day{
			Date: date, Coverage: in.Coverage, Present: in.Present,
			Values: features.Values(in, o.Features),
		}
		days = append(days, d)
		remember(d)
		if !isToday {
			out.Computed = append(out.Computed, d)
		}
	}
	// Vacation and sick days are applied at scoring time, so an annotation
	// added later also covers days whose features are already stored.
	notes, err := st.Annotations(ctx, day(featureHistory), today)
	if err != nil {
		return Scoring{}, err
	}
	dates := make([]string, len(days))
	for i, d := range days {
		dates[i] = d.Date
	}
	excluded := annotation.Excluded(notes, dates)
	for i := range days {
		days[i].Excluded = excluded[days[i].Date]
	}
	sp.Role = o.Role
	out.Scores = strain.Compute(days, sp)
	return out, nil
}
