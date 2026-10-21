package attention

import (
	"sort"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// Identity is what persistence knows about a window: a keyed hash, the app
// id (empty for private windows) and the category. The caller computes it,
// because hashing needs a key the domain doesn't hold.
type Identity struct {
	Key      string
	AppID    string
	Category model.Category
}

// Rollups accumulates per-minute, per-window attention. Ticks themselves are
// never persisted (privacy & security §3.1).
type Rollups struct {
	rollover time.Duration
	open     map[rollupKey]*model.MinuteRollup
}

type rollupKey struct {
	minute int64 // unix minute
	window string
}

// NewRollups returns an accumulator using rollover for logical days.
func NewRollups(rollover time.Duration) *Rollups {
	return &Rollups{rollover: rollover, open: make(map[rollupKey]*model.MinuteRollup)}
}

// Add records one tick. identify must return the identity of every window
// in the snapshot; windows it reports with an empty key are skipped.
func (r *Rollups) Add(res Result, s model.Snapshot, identify func(model.Window) Identity) {
	dt := res.Dt.Seconds()
	if dt <= 0 {
		return
	}
	minute := res.At.Truncate(time.Minute)
	for _, w := range s.Windows {
		id := identify(w)
		if id.Key == "" {
			continue
		}
		att := res.Attention[w.ID] * dt
		foc := 0.0
		if w.Focused {
			foc = dt
		}
		vis := w.Visible * dt
		if att == 0 && foc == 0 && vis == 0 {
			continue
		}
		m := r.row(minute, id, w.Monitor)
		m.AttendedS += att
		m.FocusedS += foc
		m.VisibleS += vis
	}
	if res.Unattributed > 0 {
		m := r.row(minute, Identity{Key: model.UnattributedWindowKey, Category: model.CategoryUncategorized}, -1)
		m.AttendedS += res.Unattributed * dt
	}
}

func (r *Rollups) row(minute time.Time, id Identity, monitor int) *model.MinuteRollup {
	k := rollupKey{minute: minute.Unix() / 60, window: id.Key}
	m, ok := r.open[k]
	if !ok {
		m = &model.MinuteRollup{
			Minute:       minute.UTC(),
			UTCOffsetMin: model.UTCOffsetMinutes(minute),
			LogicalDay:   model.LogicalDay(minute, r.rollover),
			WindowKey:    id.Key,
			AppID:        id.AppID,
			Category:     id.Category,
			Monitor:      monitor,
		}
		r.open[k] = m
	}
	return m
}

// Flush returns and forgets every minute that ended at or before now. Pass a
// time far in the future to flush everything (for example on shutdown).
func (r *Rollups) Flush(now time.Time) []model.MinuteRollup {
	cut := now.Truncate(time.Minute).Unix() / 60
	var out []model.MinuteRollup
	for k, m := range r.open {
		if k.minute < cut {
			out = append(out, *m)
			delete(r.open, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Minute.Equal(out[j].Minute) {
			return out[i].Minute.Before(out[j].Minute)
		}
		return out[i].WindowKey < out[j].WindowKey
	})
	return out
}
