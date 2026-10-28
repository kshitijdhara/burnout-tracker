package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/model"
)

// metaForgotAt is the meta key holding when forget last ran, in Unix
// milliseconds. Calendar syncs never import blocks starting before it:
// anything that existed then was either forgotten or is already stored.
const metaForgotAt = "forgot_at_ms"

// BusyBlock is one calendar busy block to store.
type BusyBlock struct {
	Start, End time.Time // in the zone the day is reckoned in
	Tentative  bool
}

// ReplaceBusy makes the stored calendar match a sync: busy blocks starting
// in [from, to) are replaced by blocks, and days are recorded as covered
// by a calendar. Nothing starting before the last time forget ran is
// touched, so a sync never brings back what the user deleted. It returns
// how many blocks were stored.
func (s *Store) ReplaceBusy(ctx context.Context, from, to time.Time, blocks []BusyBlock, days []string, rollover time.Duration) (int, error) {
	if err := s.writable(); err != nil {
		return 0, err
	}
	floor, err := s.forgotAt(ctx)
	if err != nil {
		return 0, err
	}
	if floor.After(from) {
		from = floor
	}
	floorDay := model.LogicalDay(floor, rollover)
	stored := 0
	err = s.inTx(ctx, "replace busy blocks", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM intervals WHERE kind = ? AND start_ms >= ? AND start_ms < ?`,
			string(model.IntervalBusy), from.UnixMilli(), to.UnixMilli()); err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO intervals (start_ms, end_ms, utc_offset_min, logical_day, kind, category, session_key, attrs)
			VALUES (?, ?, ?, ?, ?, NULL, NULL, ?)`)
		if err != nil {
			return err
		}
		defer func() { _ = stmt.Close() }()
		for _, b := range blocks {
			if b.Start.Before(from) || !b.Start.Before(to) {
				continue
			}
			state := "confirmed"
			if b.Tentative {
				state = "tentative"
			}
			attrs, err := json.Marshal(model.IntervalAttrs{State: state})
			if err != nil {
				return err
			}
			if _, err := stmt.ExecContext(ctx, b.Start.UnixMilli(), b.End.UnixMilli(), model.UTCOffsetMinutes(b.Start),
				model.LogicalDay(b.Start, rollover), string(model.IntervalBusy), string(attrs)); err != nil {
				return err
			}
			stored++
		}
		for _, d := range days {
			if !floor.IsZero() && d < floorDay {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO calendar_days (logical_day) VALUES (?)`, d); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("sqlite: %w", err)
	}
	return stored, nil
}

// RemoveCalendar deletes every stored busy block and calendar day, for
// when the user removes their calendar and asks for its data to go too.
func (s *Store) RemoveCalendar(ctx context.Context) error {
	if err := s.writable(); err != nil {
		return err
	}
	err := s.inTx(ctx, "remove calendar", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM intervals WHERE kind = ?`, string(model.IntervalBusy)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM calendar_days`)
		return err
	})
	if err != nil {
		return fmt.Errorf("sqlite: %w", err)
	}
	return s.reclaim(ctx)
}

// forgotAt is when forget last ran; zero if never.
func (s *Store) forgotAt(ctx context.Context) (time.Time, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, metaForgotAt).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("sqlite: meta: %w", err)
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, nil // a damaged value doesn't block syncing
	}
	return time.UnixMilli(ms), nil
}
