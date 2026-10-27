package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/model"
)

// WriteRollups stores per-minute attention rollups. A rollup for a minute and
// window that already exists is added to, so partial minutes flushed in
// separate batches sum correctly.
func (s *Store) WriteRollups(ctx context.Context, rs []model.MinuteRollup) error {
	if err := s.writable(); err != nil {
		return err
	}
	if len(rs) == 0 {
		return nil
	}
	return s.inTx(ctx, "write rollups", func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO attention_minutes
				(minute_ms, utc_offset_min, logical_day, window_key, app_id, category, monitor, attended_s, focused_s, visible_s)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (minute_ms, window_key) DO UPDATE SET
				attended_s = attended_s + excluded.attended_s,
				focused_s  = focused_s  + excluded.focused_s,
				visible_s  = visible_s  + excluded.visible_s`)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for _, r := range rs {
			if _, err := stmt.ExecContext(ctx,
				r.Minute.UnixMilli(), r.UTCOffsetMin, r.LogicalDay, r.WindowKey, nullString(r.AppID),
				string(r.Category), r.Monitor, r.AttendedS, r.FocusedS, r.VisibleS); err != nil {
				return err
			}
		}
		return nil
	})
}

// WriteIntervals stores timeline intervals.
func (s *Store) WriteIntervals(ctx context.Context, is []model.Interval) error {
	if err := s.writable(); err != nil {
		return err
	}
	if len(is) == 0 {
		return nil
	}
	return s.inTx(ctx, "write intervals", func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO intervals
				(start_ms, end_ms, utc_offset_min, logical_day, kind, category, session_key, attrs)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for _, iv := range is {
			attrs, err := json.Marshal(iv.Attrs)
			if err != nil {
				return err
			}
			if _, err := stmt.ExecContext(ctx,
				iv.Start.UnixMilli(), iv.End.UnixMilli(), iv.UTCOffsetMin, iv.LogicalDay, string(iv.Kind),
				nullString(string(iv.Category)), nullString(iv.SessionKey), string(attrs)); err != nil {
				return err
			}
		}
		return nil
	})
}

// WriteMonitorSnapshot records the monitor topology (count, resolutions,
// arrangement) as of at.
func (s *Store) WriteMonitorSnapshot(ctx context.Context, at time.Time, topology string) error {
	if err := s.writable(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO monitor_snapshots (ts_ms, topology) VALUES (?, ?)
		 ON CONFLICT (ts_ms) DO UPDATE SET topology = excluded.topology`, at.UnixMilli(), topology)
	if err != nil {
		return fmt.Errorf("sqlite: write monitor snapshot: %w", err)
	}
	return nil
}

// WriteHealth stores the day's health SLIs as JSON, replacing any earlier value.
func (s *Store) WriteHealth(ctx context.Context, logicalDay, slisJSON string) error {
	if err := s.writable(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO health_daily (logical_day, slis) VALUES (?, ?)
		 ON CONFLICT (logical_day) DO UPDATE SET slis = excluded.slis`, logicalDay, slisJSON)
	if err != nil {
		return fmt.Errorf("sqlite: write health: %w", err)
	}
	return nil
}

func (s *Store) inTx(ctx context.Context, what string, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: %s: %w", what, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return fmt.Errorf("sqlite: %s: %w", what, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: %s: %w", what, err)
	}
	return nil
}

// inImmediateTx runs fn in a BEGIN IMMEDIATE transaction, for a write
// that reads first. It takes the write lock at the start, waiting up to
// busy_timeout for it, so another connection committing between the read
// and the write can't fail it with SQLITE_BUSY_SNAPSHOT, as a deferred
// transaction can.
func (s *Store) inImmediateTx(ctx context.Context, what string, fn func(*sql.Conn) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("sqlite: %s: %w", what, err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("sqlite: %s: %w", what, err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// Rolled back even if ctx is done; a connection still in a
		// transaction is closed rather than returned to the pool.
		if _, rbErr := conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`); rbErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if err := fn(conn); err != nil {
		return fmt.Errorf("sqlite: %s: %w", what, err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("sqlite: %s: %w", what, err)
	}
	committed = true
	return nil
}

func nullString(v string) sql.NullString {
	return sql.NullString{String: v, Valid: v != ""}
}
