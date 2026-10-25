package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/annotation"
)

// Forget deletes everything recorded at or after since (privacy & security
// §5.1 "Delete recent" and "Forget"): attention minutes overlapping it,
// intervals ending after it, check-ins, raw events and monitor snapshots
// from it, the derived rows (health, features, scores) of sinceDay and
// later, and every recovery or pre-migration copy made after it, since
// those contain the forgotten time. Annotations are the user's own notes
// and are kept. Freed pages are overwritten (secure_delete) and the WAL
// truncated, so the data doesn't linger on disk.
//
// Run it from the process that writes the database: the daemon when it is
// running, so nothing it holds in memory is written back afterwards.
func (s *Store) Forget(ctx context.Context, since time.Time, sinceDay string) error {
	if err := s.writable(); err != nil {
		return err
	}
	ms := since.UnixMilli()
	err := s.inTx(ctx, "forget", func(tx *sql.Tx) error {
		for _, q := range []struct {
			query string
			arg   any
		}{
			// A minute that started before since but overlaps it goes too.
			{`DELETE FROM attention_minutes WHERE minute_ms > ? - 60000`, ms},
			{`DELETE FROM intervals WHERE end_ms > ?`, ms},
			{`DELETE FROM raw_events WHERE recv_ms >= ?`, ms},
			{`DELETE FROM monitor_snapshots WHERE ts_ms >= ?`, ms},
			{`DELETE FROM checkins WHERE ts_ms >= ?`, ms},
			{`DELETE FROM health_daily WHERE logical_day >= ?`, sinceDay},
			{`DELETE FROM daily_features WHERE logical_day >= ?`, sinceDay},
			{`DELETE FROM scores WHERE logical_day >= ?`, sinceDay},
		} {
			if _, err := tx.ExecContext(ctx, q.query, q.arg); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := s.removeCopiesSince(since); err != nil {
		return err
	}
	return s.reclaim(ctx)
}

// removeCopiesSince deletes recovery and pre-migration copies made at or
// after since. The next nightly maintenance makes a fresh one.
func (s *Store) removeCopiesSince(since time.Time) error {
	dir := filepath.Join(s.dir, backupDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sqlite: forget copies: %w", err)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || (!reNightly.MatchString(e.Name()) && !rePremigrate.MatchString(e.Name())) {
			continue
		}
		fi, err := e.Info()
		if err != nil || fi.ModTime().Before(since) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("sqlite: forget copies: %w", err)
		}
	}
	return nil
}

// AddAnnotation stores an annotation and returns its id.
func (s *Store) AddAnnotation(ctx context.Context, a annotation.Annotation) (int64, error) {
	if err := s.writable(); err != nil {
		return 0, err
	}
	if err := a.Validate(); err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO annotations (start_day, end_day, kind, note) VALUES (?, ?, ?, ?)`,
		a.From, a.To, string(a.Kind), nullString(a.Note))
	if err != nil {
		return 0, fmt.Errorf("sqlite: add annotation: %w", err)
	}
	return res.LastInsertId()
}

// RemoveAnnotation deletes an annotation; it reports whether one existed.
func (s *Store) RemoveAnnotation(ctx context.Context, id int64) (bool, error) {
	if err := s.writable(); err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM annotations WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("sqlite: remove annotation: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Annotations returns the annotations overlapping [from, to], by start day.
func (s *Store) Annotations(ctx context.Context, from, to string) ([]annotation.Annotation, error) {
	var out []annotation.Annotation
	err := s.scanRows(ctx, `
		SELECT id, start_day, end_day, kind, COALESCE(note, '') FROM annotations
		WHERE end_day >= ? AND start_day <= ? ORDER BY start_day, id`,
		[]any{from, to}, func(r *sql.Rows) error {
			var a annotation.Annotation
			var kind string
			if err := r.Scan(&a.ID, &a.From, &a.To, &kind, &a.Note); err != nil {
				return err
			}
			a.Kind = annotation.Kind(kind)
			out = append(out, a)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("sqlite: annotations: %w", err)
	}
	return out, nil
}
