package sqlite

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/model"
)

var base = time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)

func openTemp(t *testing.T) *Store {
	t.Helper()
	// A space in the path matches macOS "Application Support".
	dir := filepath.Join(t.TempDir(), "data dir")
	s, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func intp(v int) *int { return &v }

func TestOpenPermissionsAndPragmas(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if err := s.WriteHealth(ctx, "2026-10-21", `{}`); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{s.dir: 0o700, s.path: 0o600} {
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != want {
				t.Errorf("%s mode %v, want %v", path, fi.Mode().Perm(), want)
			}
		}
	}
	for pragma, want := range map[string]string{
		"auto_vacuum": "2", "journal_mode": "wal", "secure_delete": "1", "foreign_keys": "1",
	} {
		var got string
		if err := s.db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %s, want %s", pragma, got, want)
		}
	}
	if err := s.QuickCheck(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationsIdempotentAndTooNew(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	for range 2 {
		s, err := Open(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		ms, err := loadMigrations()
		if err != nil {
			t.Fatal(err)
		}
		if n != len(ms) {
			t.Fatalf("schema_migrations rows = %d, want %d", n, len(ms))
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}

	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations VALUES (999, 0)`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := Open(ctx, dir); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("got %v, want ErrSchemaTooNew", err)
	}
}

func TestRollupUpsertSums(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	r := model.MinuteRollup{
		Minute: base, LogicalDay: "2026-10-21", WindowKey: "w1", AppID: "com.apple.Terminal",
		Category: model.CategoryTerminal, AttendedS: 20, FocusedS: 30, VisibleS: 30,
	}
	for range 3 {
		if err := s.WriteRollups(ctx, []model.MinuteRollup{r}); err != nil {
			t.Fatal(err)
		}
	}
	var att, foc float64
	if err := s.db.QueryRowContext(ctx, `SELECT attended_s, focused_s FROM attention_minutes`).Scan(&att, &foc); err != nil {
		t.Fatal(err)
	}
	if att != 60 || foc != 90 {
		t.Fatalf("attended %v focused %v, want 60 90", att, foc)
	}
}

func TestDaySummary(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	d := "2026-10-21"
	rollups := []model.MinuteRollup{
		{Minute: base, LogicalDay: d, WindowKey: "a", AppID: "code", Category: model.CategoryCode, AttendedS: 50},
		{Minute: base.Add(time.Minute), LogicalDay: d, WindowKey: "a", AppID: "code", Category: model.CategoryCode, AttendedS: 40},
		{Minute: base, LogicalDay: d, WindowKey: "b", AppID: "slack", Category: model.CategoryComms, AttendedS: 10},
		{Minute: base, LogicalDay: d, WindowKey: "c", Category: model.CategoryPrivate, AttendedS: 5},
		{Minute: base, LogicalDay: d, WindowKey: model.UnattributedWindowKey, Category: model.CategoryUncategorized, AttendedS: 7},
		{Minute: base.Add(-day), LogicalDay: "2026-10-20", WindowKey: "a", AppID: "code", Category: model.CategoryCode, AttendedS: 999},
	}
	if err := s.WriteRollups(ctx, rollups); err != nil {
		t.Fatal(err)
	}
	iv := func(kind model.IntervalKind, secs int, session string, a model.IntervalAttrs) model.Interval {
		return model.Interval{Start: base, End: base.Add(time.Duration(secs) * time.Second), LogicalDay: d, Kind: kind, SessionKey: session, Attrs: a}
	}
	intervals := []model.Interval{
		iv(model.IntervalShellCmd, 1, "", model.IntervalAttrs{Argv0: "git", ExitCode: intp(0)}),
		iv(model.IntervalShellCmd, 1, "", model.IntervalAttrs{Argv0: "git", ExitCode: intp(1)}),
		iv(model.IntervalShellCmd, 1, "", model.IntervalAttrs{Argv0: "go", ExitCode: intp(2)}),
		iv(model.IntervalAgentState, 60, "s1", model.IntervalAttrs{State: "working"}),
		iv(model.IntervalAgentState, 30, "s2", model.IntervalAttrs{State: "working"}),
		iv(model.IntervalAgentState, 10, "s2", model.IntervalAttrs{State: "blocked_on_human"}),
		iv(model.IntervalHumanState, 45, "s1", model.IntervalAttrs{State: "reading"}),
	}
	if err := s.WriteIntervals(ctx, intervals); err != nil {
		t.Fatal(err)
	}

	sum, err := s.DaySummary(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if sum.CategorySeconds[model.CategoryCode] != 90 || sum.CategorySeconds[model.CategoryComms] != 10 || sum.CategorySeconds[model.CategoryPrivate] != 5 {
		t.Errorf("categories = %v", sum.CategorySeconds)
	}
	if sum.UnattributedSeconds != 7 {
		t.Errorf("unattributed = %v", sum.UnattributedSeconds)
	}
	if len(sum.TopApps) != 2 || sum.TopApps[0] != (NamedSeconds{"code", 90}) {
		t.Errorf("top apps = %v", sum.TopApps)
	}
	if sum.ShellCommands != 3 || sum.ShellFailures != 2 {
		t.Errorf("shell = %d/%d", sum.ShellCommands, sum.ShellFailures)
	}
	if len(sum.TopCommands) != 2 || sum.TopCommands[0] != (NamedCount{"git", 2}) {
		t.Errorf("top commands = %v", sum.TopCommands)
	}
	if sum.AgentStateSeconds["working"] != 90 || sum.AgentStateSeconds["blocked_on_human"] != 10 {
		t.Errorf("agent states = %v", sum.AgentStateSeconds)
	}
	if sum.HumanStateSeconds["reading"] != 45 {
		t.Errorf("human states = %v", sum.HumanStateSeconds)
	}
	if sum.AgentSessions != 2 {
		t.Errorf("sessions = %d", sum.AgentSessions)
	}
}

// Deleted data must not survive anywhere in the database files.
func TestRetentionSecureDeleteCanary(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	const canary = "CANARYZZ9"
	old := base.Add(-200 * day)
	var batch []model.Interval
	for i := range 50 {
		batch = append(batch, model.Interval{
			Start: old.Add(time.Duration(i) * time.Second), End: old.Add(time.Duration(i+1) * time.Second),
			LogicalDay: "2026-04-04", Kind: model.IntervalShellCmd,
			Attrs: model.IntervalAttrs{Argv0: canary},
		})
	}
	keep := model.Interval{Start: base, End: base.Add(time.Second), LogicalDay: "2026-10-21", Kind: model.IntervalShellCmd,
		Attrs: model.IntervalAttrs{Argv0: "keepme"}}
	if err := s.WriteIntervals(ctx, append(batch, keep)); err != nil {
		t.Fatal(err)
	}
	n, err := s.RunRetention(ctx, base, DefaultRetention())
	if err != nil {
		t.Fatal(err)
	}
	if n != 50 {
		t.Fatalf("deleted %d rows, want 50", n)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, suffix := range []string{"", "-wal", "-shm"} {
		b, err := os.ReadFile(filepath.Join(dir, FileName+suffix))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(canary)) {
			t.Errorf("canary found in %s%s after deletion", FileName, suffix)
		}
		if suffix == "" && bytes.Contains(b, []byte("keepme")) {
			found = true
		}
	}
	if !found {
		t.Error("retained row missing from the database file")
	}
}

func TestBackupRotationKeepsThree(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	for i := range 5 {
		if err := s.Backup(ctx, base.Add(time.Duration(i)*day)); err != nil {
			t.Fatal(err)
		}
	}
	// Same day again is a no-op: restarts never re-copy the database.
	today := filepath.Join(s.dir, backupDir, "burntout-20261025.db")
	before, err := os.Stat(today)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, base.Add(4*day)); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(today)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("same-day backup rewrote today's copy")
	}
	entries, err := os.ReadDir(filepath.Join(s.dir, backupDir))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"burntout-20261023.db", "burntout-20261024.db", "burntout-20261025.db"}
	if len(names) != len(want) {
		t.Fatalf("backups = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("backups = %v, want %v", names, want)
		}
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(s.dir, backupDir, want[0]))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("backup mode %v", fi.Mode().Perm())
		}
	}
}

func TestOpenReadOnly(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := OpenReadOnly(ctx, dir); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("missing db: got %v, want ErrNoDatabase", err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("OpenReadOnly created a database file")
	}

	w, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteHealth(ctx, "2026-10-21", `{"coverage":1}`); err != nil {
		t.Fatal(err)
	}

	// Reads work while the writer is still open (the daemon is running).
	r, err := OpenReadOnly(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if h, err := r.Health(ctx, "2026-10-21"); err != nil || h == "" {
		t.Fatalf("Health = %q, %v", h, err)
	}
	if _, err := r.DaySummary(ctx, "2026-10-21"); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"WriteHealth":  r.WriteHealth(ctx, "2026-10-22", "{}"),
		"WriteRollups": r.WriteRollups(ctx, nil),
		"Backup":       r.Backup(ctx, base),
	} {
		if !errors.Is(err, ErrReadOnly) {
			t.Errorf("%s on read-only store: got %v, want ErrReadOnly", name, err)
		}
	}
	if _, err := r.RunRetention(ctx, base, DefaultRetention()); !errors.Is(err, ErrReadOnly) {
		t.Errorf("RunRetention on read-only store: got %v", err)
	}
	_ = r.Close()

	// A schema other than this build's is refused, never migrated.
	if _, err := w.db.ExecContext(ctx, `INSERT INTO schema_migrations VALUES (999, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(ctx, dir); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("got %v, want ErrSchemaMismatch", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPremigrateCopiesExpire(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	dir := filepath.Join(s.dir, backupDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := "burntout-premigrate-v1-20261001T120000.db"
	recent := "burntout-premigrate-v1-20261020T120000.db"
	for _, n := range []string{old, recent} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	if _, err := s.RunRetention(ctx, now, DefaultRetention()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, old)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("20-day-old pre-migration copy kept")
	}
	if _, err := os.Stat(filepath.Join(dir, recent)); err != nil {
		t.Errorf("recent pre-migration copy removed: %v", err)
	}
}

func TestRetentionDeletesInChunks(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ivs := make([]model.Interval, deleteChunk*2+7)
	for i := range ivs {
		st := old.Add(time.Duration(i) * time.Second)
		ivs[i] = model.Interval{Start: st, End: st.Add(time.Second), Kind: model.IntervalShellCmd, LogicalDay: "2025-01-01"}
	}
	if err := s.WriteIntervals(ctx, ivs); err != nil {
		t.Fatal(err)
	}
	n, err := s.RunRetention(ctx, time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), DefaultRetention())
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(ivs)) {
		t.Fatalf("deleted %d, want %d", n, len(ivs))
	}
}
