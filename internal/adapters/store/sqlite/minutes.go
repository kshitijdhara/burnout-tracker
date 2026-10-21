package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// UnattributedLabel labels minutes where no window clearly held attention.
const UnattributedLabel = "unattributed"

// MinuteActivity is one minute of a day's timeline, in the local time it
// was recorded in.
type MinuteActivity struct {
	Minute time.Time
	// Label is the app that held the most attention that minute: its app
	// id, its category for private windows, or UnattributedLabel.
	Label     string
	Category  model.Category
	AttendedS float64 // all attention in the minute
	// CallS and VideoS are the seconds of the minute a call or visible
	// video held presence.
	CallS, VideoS float64
}

// DayMinutes returns every minute of a logical day with recorded attention,
// in order. Minutes without any (away, locked, asleep) are absent.
func (s *Store) DayMinutes(ctx context.Context, logicalDay string) ([]MinuteActivity, error) {
	minutes, err := s.minuteLabels(ctx, logicalDay)
	if err != nil {
		return nil, fmt.Errorf("sqlite: day minutes: %w", err)
	}
	if err := s.addMediaSeconds(ctx, logicalDay, minutes); err != nil {
		return nil, fmt.Errorf("sqlite: day minutes: %w", err)
	}
	out := make([]MinuteActivity, 0, len(minutes))
	for _, m := range minutes {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Minute.Before(out[j].Minute) })
	return out, nil
}

// minuteLabels sums attention per minute and label and keeps each minute's
// top label, keyed by unix minute.
func (s *Store) minuteLabels(ctx context.Context, logicalDay string) (map[int64]*MinuteActivity, error) {
	type labelKey struct {
		minute int64
		label  string
	}
	perLabel := map[labelKey]float64{}
	minutes := map[int64]*MinuteActivity{}
	cats := map[string]model.Category{}
	err := s.scanRows(ctx, `
		SELECT minute_ms, utc_offset_min, window_key, COALESCE(app_id, ''), category, attended_s
		FROM attention_minutes WHERE logical_day = ? AND attended_s > 0`,
		[]any{logicalDay}, func(r *sql.Rows) error {
			var ms int64
			var off int
			var key, app, cat string
			var att float64
			if err := r.Scan(&ms, &off, &key, &app, &cat, &att); err != nil {
				return err
			}
			label := app
			switch {
			case key == model.UnattributedWindowKey:
				label = UnattributedLabel
			case label == "":
				label = cat
			}
			cats[label] = model.Category(cat)
			minute := ms / 60_000
			m, ok := minutes[minute]
			if !ok {
				m = &MinuteActivity{Minute: time.UnixMilli(ms).In(time.FixedZone("", off*60))}
				minutes[minute] = m
			}
			m.AttendedS += att
			perLabel[labelKey{minute, label}] += att
			return nil
		})
	if err != nil {
		return nil, err
	}
	best := map[int64]float64{}
	for k, att := range perLabel {
		m := minutes[k.minute]
		if att > best[k.minute] || (att == best[k.minute] && k.label < m.Label) {
			best[k.minute], m.Label, m.Category = att, k.label, cats[k.label]
		}
	}
	return minutes, nil
}

// addMediaSeconds spreads call and video presence intervals over the
// minutes they cover.
func (s *Store) addMediaSeconds(ctx context.Context, logicalDay string, minutes map[int64]*MinuteActivity) error {
	return s.scanRows(ctx, `
		SELECT start_ms, end_ms, json_extract(attrs, '$.reason') FROM intervals
		WHERE logical_day = ? AND kind = ? AND json_extract(attrs, '$.reason') IN ('call', 'video')`,
		[]any{logicalDay, string(model.IntervalPresence)}, func(r *sql.Rows) error {
			var start, end int64
			var reason string
			if err := r.Scan(&start, &end, &reason); err != nil {
				return err
			}
			for minute := start / 60_000; minute*60_000 < end; minute++ {
				m, ok := minutes[minute]
				if !ok {
					continue
				}
				lo, hi := max(start, minute*60_000), min(end, (minute+1)*60_000)
				secs := float64(hi-lo) / 1000
				if reason == "call" {
					m.CallS += secs
				} else {
					m.VideoS += secs
				}
			}
			return nil
		})
}
