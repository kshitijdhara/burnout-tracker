package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/kshitijdhara/burntout/internal/domain/model"
)

// Aggregate reduces the attention minutes of every logical day before
// beforeDay to one row per category per minute, without app ids or window
// keys (aggregate-only mode, privacy & security §5.1). The daemon already
// writes new minutes that way while the mode is on; this reduces what was
// recorded before it was turned on. Unattributed minutes stay as they
// are. Each day is reduced in its own transaction; it returns the number of
// rows removed.
func (s *Store) Aggregate(ctx context.Context, beforeDay string) (int64, error) {
	if err := s.writable(); err != nil {
		return 0, err
	}
	var days []string
	err := s.scanRows(ctx, `
		SELECT DISTINCT logical_day FROM attention_minutes
		WHERE logical_day < ? AND window_key != ? AND window_key NOT LIKE ? ORDER BY 1`,
		[]any{beforeDay, model.UnattributedWindowKey, model.CategoryWindowKeyPrefix + "%"},
		func(r *sql.Rows) error {
			var d string
			if err := r.Scan(&d); err != nil {
				return err
			}
			days = append(days, d)
			return nil
		})
	if err != nil {
		return 0, fmt.Errorf("sqlite: aggregate: %w", err)
	}
	var removed int64
	for _, day := range days {
		n, err := s.aggregateDay(ctx, day)
		removed += n
		if err != nil {
			return removed, fmt.Errorf("sqlite: aggregate %s: %w", day, err)
		}
	}
	if len(days) > 0 {
		if err := s.reclaim(ctx); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func (s *Store) aggregateDay(ctx context.Context, day string) (int64, error) {
	var removed int64
	err := s.inTx(ctx, "aggregate", func(tx *sql.Tx) error {
		type row struct {
			minute, offset, monitor int64
			cat                     string
			att, foc, vis           float64
		}
		// The rows to reduce: everything but unattributed time and rows that
		// are already per category.
		const which = `logical_day = ? AND window_key != ? AND window_key NOT LIKE ?`
		args := []any{day, model.UnattributedWindowKey, model.CategoryWindowKeyPrefix + "%"}
		rows, err := tx.QueryContext(ctx, `
			SELECT minute_ms, MIN(utc_offset_min), MIN(monitor), category,
				SUM(attended_s), SUM(focused_s), SUM(visible_s)
			FROM attention_minutes WHERE `+which+` GROUP BY minute_ms, category`, args...)
		if err != nil {
			return err
		}
		var grouped []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.minute, &r.offset, &r.monitor, &r.cat, &r.att, &r.foc, &r.vis); err != nil {
				_ = rows.Close()
				return err
			}
			grouped = append(grouped, r)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		count := func() (n int64, err error) {
			err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attention_minutes WHERE logical_day = ?`, day).Scan(&n)
			return n, err
		}
		before, err := count()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM attention_minutes WHERE `+which, args...); err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO attention_minutes
				(minute_ms, utc_offset_min, logical_day, window_key, app_id, category, monitor, attended_s, focused_s, visible_s)
			VALUES (?, ?, ?, ?, NULL, ?, ?, ?, ?, ?)
			ON CONFLICT (minute_ms, window_key) DO UPDATE SET
				attended_s = attended_s + excluded.attended_s,
				focused_s  = focused_s  + excluded.focused_s,
				visible_s  = visible_s  + excluded.visible_s`)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for _, r := range grouped {
			key := model.CategoryWindowKey(model.Category(r.cat))
			if _, err := stmt.ExecContext(ctx, r.minute, r.offset, day, key, r.cat, r.monitor, r.att, r.foc, r.vis); err != nil {
				return err
			}
		}
		after, err := count()
		removed = before - after
		return err
	})
	return removed, err
}
