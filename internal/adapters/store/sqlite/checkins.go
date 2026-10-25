package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/checkin"
)

// SaveCheckins stores completed check-ins together: a weekly check-in is
// both CBI subscales, and either both are stored or neither.
func (s *Store) SaveCheckins(ctx context.Context, rs []checkin.Record) error {
	if err := s.writable(); err != nil {
		return err
	}
	if len(rs) == 0 {
		return nil
	}
	return s.inTx(ctx, "save check-ins", func(tx *sql.Tx) error {
		for _, r := range rs {
			b, err := json.Marshal(r.Answers)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO checkins (ts_ms, instrument, answers, score) VALUES (?, ?, ?, ?)`,
				r.At.UnixMilli(), r.Instrument, string(b), r.Score); err != nil {
				return err
			}
		}
		return nil
	})
}

// Checkins returns check-ins at or after since, oldest first, in local
// time.
func (s *Store) Checkins(ctx context.Context, since time.Time) ([]checkin.Record, error) {
	var out []checkin.Record
	err := s.scanRows(ctx, `
		SELECT ts_ms, instrument, answers, COALESCE(score, 0) FROM checkins
		WHERE ts_ms >= ? ORDER BY ts_ms, id`,
		[]any{since.UnixMilli()}, func(r *sql.Rows) error {
			var ms int64
			var rec checkin.Record
			var answers string
			if err := r.Scan(&ms, &rec.Instrument, &answers, &rec.Score); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(answers), &rec.Answers); err != nil {
				return nil // a damaged row is skipped
			}
			rec.At = time.UnixMilli(ms).Local()
			out = append(out, rec)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("sqlite: check-ins: %w", err)
	}
	return out, nil
}
