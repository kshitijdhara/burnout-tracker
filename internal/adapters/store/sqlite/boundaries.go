package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/boundary"
)

// SetBoundary makes b the active rule of its kind from the day after today
// and returns its id. An active rule of the same kind ends today, so the
// day of a change is judged by neither rule. Setting the rule that is
// already active changes nothing and returns its id with changed false.
func (s *Store) SetBoundary(ctx context.Context, b boundary.Boundary, today string, now time.Time) (id int64, changed bool, err error) {
	if err := s.writable(); err != nil {
		return 0, false, err
	}
	if err := b.Validate(); err != nil {
		return 0, false, err
	}
	if !slices.Contains([]string{boundary.SourceCLI, boundary.SourceDashboard, boundary.SourceReview}, b.Source) {
		return 0, false, fmt.Errorf("%w: source %q", boundary.ErrInvalid, b.Source)
	}
	t, err := time.Parse(time.DateOnly, today)
	if err != nil {
		return 0, false, fmt.Errorf("sqlite: set boundary: %w", err)
	}
	// Immediate: the read and the write must see the same active rule, and
	// the daemon's flushes commit in between. The unique index
	// boundaries_one_active backs this up.
	err = s.inImmediateTx(ctx, "set boundary", func(conn *sql.Conn) error {
		var value string
		err := conn.QueryRowContext(ctx,
			`SELECT id, value FROM boundaries WHERE kind = ? AND until_day IS NULL`, string(b.Kind)).Scan(&id, &value)
		switch {
		case err == nil && value == b.Value:
			return nil
		case err == nil:
			if _, err := conn.ExecContext(ctx, `UPDATE boundaries SET until_day = ?, ended_ms = ? WHERE id = ?`,
				today, now.UnixMilli(), id); err != nil {
				return err
			}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		res, err := conn.ExecContext(ctx,
			`INSERT INTO boundaries (kind, value, since_day, source, created_ms) VALUES (?, ?, ?, ?, ?)`,
			string(b.Kind), b.Value, t.AddDate(0, 0, 1).Format(time.DateOnly), b.Source, now.UnixMilli())
		if err != nil {
			return err
		}
		changed = true
		id, err = res.LastInsertId()
		return err
	})
	return id, changed, err
}

// RemoveBoundary ends the active rule with id today; it reports whether
// there was one. The row stays, as history.
func (s *Store) RemoveBoundary(ctx context.Context, id int64, today string, now time.Time) (bool, error) {
	if err := s.writable(); err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE boundaries SET until_day = ?, ended_ms = ? WHERE id = ? AND until_day IS NULL`,
		today, now.UnixMilli(), id)
	if err != nil {
		return false, fmt.Errorf("sqlite: remove boundary: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Boundaries returns every rule, active and past, oldest first.
func (s *Store) Boundaries(ctx context.Context) ([]boundary.Boundary, error) {
	var out []boundary.Boundary
	err := s.scanRows(ctx, `
		SELECT id, kind, value, since_day, COALESCE(until_day, ''), source FROM boundaries ORDER BY id`,
		nil, func(r *sql.Rows) error {
			var b boundary.Boundary
			var kind string
			if err := r.Scan(&b.ID, &kind, &b.Value, &b.Since, &b.Until, &b.Source); err != nil {
				return err
			}
			b.Kind = boundary.Kind(kind)
			out = append(out, b)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("sqlite: boundaries: %w", err)
	}
	return out, nil
}

// UncategorizedApp is an app no rule or override has a category for, and
// how long it held attention.
type UncategorizedApp struct {
	App     string
	Seconds float64
}

// UncategorizedApps returns the apps whose attention in [from, to] is
// stored as uncategorized, most attended first. Private and aggregate-only
// minutes have no app and never appear. Sites can't be listed: titles,
// and the domains read from them, are never stored.
func (s *Store) UncategorizedApps(ctx context.Context, from, to string) ([]UncategorizedApp, error) {
	var out []UncategorizedApp
	err := s.scanRows(ctx, `
		SELECT app_id, SUM(attended_s) FROM attention_minutes
		WHERE logical_day BETWEEN ? AND ? AND category = 'uncategorized' AND app_id IS NOT NULL AND app_id != ''
		GROUP BY app_id ORDER BY 2 DESC, 1`,
		[]any{from, to}, func(r *sql.Rows) error {
			var a UncategorizedApp
			if err := r.Scan(&a.App, &a.Seconds); err != nil {
				return err
			}
			out = append(out, a)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("sqlite: uncategorized apps: %w", err)
	}
	return out, nil
}
