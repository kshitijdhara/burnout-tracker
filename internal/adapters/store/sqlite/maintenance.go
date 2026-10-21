package sqlite

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
)

const (
	backupDir   = "backups"
	backupsKept = 3
	day         = 24 * time.Hour
)

// Nightly recovery copies are named by date; pre-migration copies use a
// different name so rotation never removes them.
var reNightly = regexp.MustCompile(`^burnout-\d{8}\.db$`)

// Retention sets how long each kind of data is kept.
type Retention struct {
	RawEvents     time.Duration
	Rollups       time.Duration // attention minutes, monitor snapshots, health
	Intervals     time.Duration
	DailyFeatures time.Duration // daily features and scores
	Checkins      time.Duration
}

// DefaultRetention returns the defaults from docs/privacy-and-security.md §3.
func DefaultRetention() Retention {
	return Retention{
		RawEvents:     7 * day,
		Rollups:       180 * day,
		Intervals:     180 * day,
		DailyFeatures: 730 * day,
		Checkins:      730 * day,
	}
}

// RunRetention deletes data older than the retention periods, then reclaims
// free pages and truncates the WAL so deleted bytes don't linger on disk
// (secure_delete overwrites them in place).
func (s *Store) RunRetention(ctx context.Context, now time.Time, r Retention) (int64, error) {
	ms := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	dayCut := func(d time.Duration) string { return now.Add(-d).Format(time.DateOnly) }
	deletes := []struct {
		query string
		arg   any
	}{
		{`DELETE FROM raw_events WHERE recv_ms < ?`, ms(r.RawEvents)},
		{`DELETE FROM attention_minutes WHERE minute_ms < ?`, ms(r.Rollups)},
		{`DELETE FROM monitor_snapshots WHERE ts_ms < ?`, ms(r.Rollups)},
		{`DELETE FROM health_daily WHERE logical_day < ?`, dayCut(r.Rollups)},
		{`DELETE FROM intervals WHERE end_ms < ?`, ms(r.Intervals)},
		{`DELETE FROM daily_features WHERE logical_day < ?`, dayCut(r.DailyFeatures)},
		{`DELETE FROM scores WHERE logical_day < ?`, dayCut(r.DailyFeatures)},
		{`DELETE FROM checkins WHERE ts_ms < ?`, ms(r.Checkins)},
	}
	var total int64
	for _, d := range deletes {
		res, err := s.db.ExecContext(ctx, d.query, d.arg)
		if err != nil {
			return total, fmt.Errorf("sqlite: retention: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("sqlite: retention: %w", err)
		}
		total += n
	}
	if err := s.reclaim(ctx); err != nil {
		return total, err
	}
	return total, nil
}

func (s *Store) reclaim(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA incremental_vacuum`); err != nil {
		return fmt.Errorf("sqlite: incremental vacuum: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("sqlite: wal checkpoint: %w", err)
	}
	return nil
}

// Backup writes a nightly recovery copy to <dir>/backups/burnout-YYYYMMDD.db
// (replacing today's copy if one exists) and keeps the newest three.
// Recovery copies stay inside the data directory and are for corruption
// recovery only; portable backups are a separate, passphrase-encrypted
// archive (ADR-0007).
func (s *Store) Backup(ctx context.Context, now time.Time) error {
	dest := filepath.Join(s.dir, backupDir, "burnout-"+now.UTC().Format("20060102")+".db")
	if err := os.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("sqlite: backup: %w", err)
	}
	if err := s.vacuumInto(ctx, dest); err != nil {
		return fmt.Errorf("sqlite: backup: %w", err)
	}
	return s.rotateBackups()
}

func (s *Store) vacuumInto(ctx context.Context, dest string) error {
	if err := paths.EnsurePrivateDir(filepath.Dir(dest)); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
		return err
	}
	return chmodPrivate(dest)
}

func (s *Store) rotateBackups() error {
	dir := filepath.Join(s.dir, backupDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("sqlite: rotate backups: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && reNightly.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	// Names embed the date, so lexical order is chronological.
	slices.Sort(names)
	for len(names) > backupsKept {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return fmt.Errorf("sqlite: rotate backups: %w", err)
		}
		names = names[1:]
	}
	return nil
}
