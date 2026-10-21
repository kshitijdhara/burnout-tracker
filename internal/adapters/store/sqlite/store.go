// Package sqlite is the SQLite storage adapter. It owns the on-disk schema,
// migrations, retention and recovery copies.
//
// Concurrency: one writer goroutine owns a Store and makes every write call.
// Reads (DaySummary, QuickCheck) are safe to call concurrently from other
// goroutines; WAL mode lets them run alongside the writer.
//
// Nothing is encrypted by the app. At-rest protection is OS full-disk
// encryption plus owner-only file permissions (ADR-0007).
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// FileName is the database file inside the data directory.
const FileName = "burnout.db"

// ErrSchemaTooNew means the database was written by a newer version of the
// app. Opening it could lose data, so the store refuses.
var ErrSchemaTooNew = errors.New("sqlite: database schema is newer than this build")

//go:embed migrations/*.sql
var migrationFS embed.FS

// Order matters: auto_vacuum only takes effect before the first table is
// created, so it comes first and is harmless on existing databases.
var pragmas = []string{
	"auto_vacuum(INCREMENTAL)",
	"busy_timeout(5000)",
	"journal_mode(WAL)",
	"synchronous(NORMAL)",
	"foreign_keys(ON)",
	"secure_delete(ON)",
}

// Store is the SQLite-backed store.
type Store struct {
	db   *sql.DB
	dir  string
	path string
}

// Open opens (creating if needed) the database in dir and applies pending
// migrations.
func Open(ctx context.Context, dir string) (*Store, error) {
	if err := paths.EnsurePrivateDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, FileName)
	if err := createPrivateFile(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	s := &Store{db: db, dir: dir, path: path}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.restrictSidecars(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// QuickCheck runs PRAGMA quick_check and returns an error unless it reports "ok".
func (s *Store) QuickCheck(ctx context.Context) error {
	var res string
	if err := s.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&res); err != nil {
		return fmt.Errorf("sqlite: quick_check: %w", err)
	}
	if res != "ok" {
		return fmt.Errorf("sqlite: quick_check: %s", res)
	}
	return nil
}

func dsn(path string) string {
	q := make([]string, len(pragmas))
	for i, p := range pragmas {
		q[i] = "_pragma=" + p
	}
	return path + "?" + strings.Join(q, "&")
}

func createPrivateFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // path is the app's own data directory
	if err != nil {
		return fmt.Errorf("sqlite: create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("sqlite: create %s: %w", path, err)
	}
	return chmodPrivate(path)
}

// restrictSidecars forces the WAL and shared-memory files to 0600; SQLite
// creates them with the process umask.
func (s *Store) restrictSidecars() error {
	for _, suffix := range []string{"-wal", "-shm"} {
		err := chmodPrivate(s.path + suffix)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func chmodPrivate(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("sqlite: chmod %s: %w", path, err)
	}
	return nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("sqlite: read migrations: %w", err)
	}
	var ms []migration
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		if !ok || !strings.HasSuffix(e.Name(), ".sql") {
			return nil, fmt.Errorf("sqlite: bad migration name %q", e.Name())
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("sqlite: bad migration name %q: %w", e.Name(), err)
		}
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("sqlite: read %s: %w", e.Name(), err)
		}
		ms = append(ms, migration{version: v, name: e.Name(), sql: string(b)})
	}
	slices.SortFunc(ms, func(a, b migration) int { return a.version - b.version })
	return ms, nil
}

func (s *Store) migrate(ctx context.Context) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_ms INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("sqlite: migrations table: %w", err)
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("sqlite: schema version: %w", err)
	}
	latest := ms[len(ms)-1].version
	if current > latest {
		return fmt.Errorf("%w: database v%d, build knows v%d", ErrSchemaTooNew, current, latest)
	}
	if current == latest {
		return nil
	}

	hasData, err := s.hasUserTables(ctx)
	if err != nil {
		return err
	}
	if hasData {
		backup := filepath.Join(s.dir, backupDir, fmt.Sprintf("burnout-premigrate-v%d-%s.db", current, time.Now().UTC().Format("20060102T150405")))
		if err := s.vacuumInto(ctx, backup); err != nil {
			return fmt.Errorf("sqlite: pre-migration backup: %w", err)
		}
	}

	for _, m := range ms {
		if m.version <= current {
			continue
		}
		if err := s.apply(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) apply(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: migration %s: %w", m.name, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("sqlite: migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_ms) VALUES (?, ?)`,
		m.version, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("sqlite: migration %s: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: migration %s: %w", m.name, err)
	}
	return nil
}

func (s *Store) hasUserTables(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name != 'schema_migrations'`).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("sqlite: list tables: %w", err)
	}
	return n > 0, nil
}
