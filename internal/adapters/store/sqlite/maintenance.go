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
	"strings"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/paths"
)

const (
	backupDir   = "backups"
	backupsKept = 3
	day         = 24 * time.Hour

	// premigrateKept is how long pre-migration copies survive. They exist to
	// undo a bad upgrade, which shows up within days; keeping them longer
	// would hold data past its retention period (privacy & security §7.2).
	premigrateKept = 7 * day

	// deleteChunk bounds rows per retention DELETE, so the write lock is
	// held only briefly while the daemon's loop is also writing.
	deleteChunk = 5000
)

var (
	// Nightly recovery copies are named by date and rotated to three.
	reNightly = regexp.MustCompile(`^burntout-\d{8}\.db$`)
	// Pre-migration copies are kept for premigrateKept, then deleted.
	rePremigrate = regexp.MustCompile(`^burntout-premigrate-v\d+-\d{8}T\d{6}\.db$`)
)

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
// (secure_delete overwrites them in place). Rows are deleted in chunks of
// deleteChunk so the daemon's regular writes are never blocked for long.
// Pre-migration copies older than premigrateKept are deleted too.
func (s *Store) RunRetention(ctx context.Context, now time.Time, r Retention) (int64, error) {
	if err := s.writable(); err != nil {
		return 0, err
	}
	ms := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	dayCut := func(d time.Duration) string { return now.Add(-d).Format(time.DateOnly) }
	// Each query deletes at most deleteChunk (5000) rows; deleteChunked
	// repeats it until fewer come back.
	deletes := []struct {
		query string
		arg   any
	}{
		{`DELETE FROM raw_events WHERE rowid IN (SELECT rowid FROM raw_events WHERE recv_ms < ? LIMIT 5000)`, ms(r.RawEvents)},
		{`DELETE FROM attention_minutes WHERE rowid IN (SELECT rowid FROM attention_minutes WHERE minute_ms < ? LIMIT 5000)`, ms(r.Rollups)},
		{`DELETE FROM monitor_snapshots WHERE rowid IN (SELECT rowid FROM monitor_snapshots WHERE ts_ms < ? LIMIT 5000)`, ms(r.Rollups)},
		{`DELETE FROM health_daily WHERE rowid IN (SELECT rowid FROM health_daily WHERE logical_day < ? LIMIT 5000)`, dayCut(r.Rollups)},
		{`DELETE FROM intervals WHERE rowid IN (SELECT rowid FROM intervals WHERE end_ms < ? LIMIT 5000)`, ms(r.Intervals)},
		{`DELETE FROM calendar_days WHERE rowid IN (SELECT rowid FROM calendar_days WHERE logical_day < ? LIMIT 5000)`, dayCut(r.Intervals)},
		{`DELETE FROM daily_features WHERE rowid IN (SELECT rowid FROM daily_features WHERE logical_day < ? LIMIT 5000)`, dayCut(r.DailyFeatures)},
		{`DELETE FROM scores WHERE rowid IN (SELECT rowid FROM scores WHERE logical_day < ? LIMIT 5000)`, dayCut(r.DailyFeatures)},
		{`DELETE FROM checkins WHERE rowid IN (SELECT rowid FROM checkins WHERE ts_ms < ? LIMIT 5000)`, ms(r.Checkins)},
	}
	var total int64
	for _, d := range deletes {
		n, err := s.deleteChunked(ctx, d.query, d.arg)
		total += n
		if err != nil {
			return total, fmt.Errorf("sqlite: retention: %w", err)
		}
	}
	if err := s.reclaim(ctx); err != nil {
		return total, err
	}
	if err := s.expirePremigrate(now); err != nil {
		return total, err
	}
	return total, nil
}

// deleteChunked runs a chunked DELETE (at most deleteChunk rows per run)
// until a run deletes fewer than deleteChunk rows.
func (s *Store) deleteChunked(ctx context.Context, query string, arg any) (int64, error) {
	var total int64
	for {
		res, err := s.db.ExecContext(ctx, query, arg)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < deleteChunk {
			return total, nil
		}
	}
}

// expirePremigrate deletes pre-migration copies older than premigrateKept,
// judged by the timestamp in their name.
func (s *Store) expirePremigrate(now time.Time) error {
	dir := filepath.Join(s.dir, backupDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sqlite: expire pre-migration copies: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !rePremigrate.MatchString(name) {
			continue
		}
		stamp := name[strings.LastIndex(name, "-")+1 : len(name)-len(".db")]
		made, err := time.Parse("20060102T150405", stamp)
		if err != nil || now.Sub(made) <= premigrateKept {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("sqlite: expire pre-migration copies: %w", err)
		}
	}
	return nil
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

// Backup writes a nightly recovery copy to <dir>/backups/burntout-YYYYMMDD.db
// and keeps the newest three. If today's copy already exists it does
// nothing, so daemon restarts (or a crash loop) never re-copy the whole
// database. Recovery copies stay inside the data directory and are for
// corruption recovery only; portable backups are a separate,
// passphrase-encrypted archive (ADR-0007).
func (s *Store) Backup(ctx context.Context, now time.Time) error {
	if err := s.writable(); err != nil {
		return err
	}
	dest := filepath.Join(s.dir, backupDir, "burntout-"+now.UTC().Format("20060102")+".db")
	if _, err := os.Stat(dest); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
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
