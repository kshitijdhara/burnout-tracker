package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/boundary"
	"github.com/kshitijdhara/burntout/internal/domain/model"
)

func mustParse(t *testing.T, kind, value, source string) boundary.Boundary {
	t.Helper()
	b, err := boundary.Parse(kind, value)
	if err != nil {
		t.Fatal(err)
	}
	b.Source = source
	return b
}

func TestBoundaryHistory(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	id1, changed, err := s.SetBoundary(ctx, mustParse(t, "shutdown", "21:30", boundary.SourceCLI), "2026-10-20", base)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	// The same rule again changes nothing.
	if id, changed, err := s.SetBoundary(ctx, mustParse(t, "shutdown", "21:30", boundary.SourceReview), "2026-10-21", base); err != nil || changed || id != id1 {
		t.Fatalf("same rule: %d %v %v", id, changed, err)
	}
	// A new time ends the old rule on the day of the change.
	id2, changed, err := s.SetBoundary(ctx, mustParse(t, "shutdown", "20:00", boundary.SourceReview), "2026-10-23", base)
	if err != nil || !changed || id2 == id1 {
		t.Fatal(id2, changed, err)
	}
	if _, _, err := s.SetBoundary(ctx, mustParse(t, "no-weekend", "", boundary.SourceDashboard), "2026-10-23", base); err != nil {
		t.Fatal(err)
	}
	all, err := s.Boundaries(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("%+v %v", all, err)
	}
	if all[0].Since != "2026-10-21" || all[0].Until != "2026-10-23" || all[1].Since != "2026-10-24" || all[1].Until != "" ||
		all[1].Source != boundary.SourceReview || all[1].Value != "20:00" {
		t.Errorf("history: %+v", all)
	}
	if cur := boundary.Current(all); len(cur) != 2 || cur[0].ID != id2 || cur[1].Kind != boundary.NoWeekend {
		t.Errorf("current: %+v", cur)
	}

	if ok, err := s.RemoveBoundary(ctx, id1, "2026-10-25", base); err != nil || ok {
		t.Errorf("removing an ended rule: %v %v", ok, err)
	}
	if ok, err := s.RemoveBoundary(ctx, id2, "2026-10-25", base); err != nil || !ok {
		t.Errorf("remove: %v %v", ok, err)
	}
	all, _ = s.Boundaries(ctx)
	if len(all) != 3 || all[1].Until != "2026-10-25" || len(boundary.Current(all)) != 1 {
		t.Errorf("after remove: %+v", all)
	}

	bad := boundary.Boundary{Kind: boundary.Shutdown, Value: "9:30", Source: boundary.SourceCLI}
	if _, _, err := s.SetBoundary(ctx, bad, "2026-10-25", base); !errors.Is(err, boundary.ErrInvalid) {
		t.Errorf("unnormalised value: %v", err)
	}
	bad = mustParse(t, "no-weekend", "", "telemetry")
	if _, _, err := s.SetBoundary(ctx, bad, "2026-10-25", base); !errors.Is(err, boundary.ErrInvalid) {
		t.Errorf("unknown source: %v", err)
	}
}

// A database from before boundaries gets the table, keeps its data, and
// read-write opens from the CLI then work.
func TestBoundariesMigration(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteHealth(ctx, "2026-10-21", `{}`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE boundaries`, `DELETE FROM schema_migrations WHERE version >= 4`} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.Close()
	if _, err := OpenExisting(ctx, dir); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("a v3 database opened read-write without migrating: %v", err)
	}
	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if h, err := s.Health(ctx, "2026-10-21"); err != nil || h != `{}` {
		t.Errorf("data lost: %q %v", h, err)
	}
	_ = s.Close()
	rw, err := OpenExisting(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rw.Close() }()
	if _, _, err := rw.SetBoundary(ctx, mustParse(t, "focus-block", "90", boundary.SourceCLI), "2026-10-21", base); err != nil {
		t.Error(err)
	}
	ro, err := OpenReadOnly(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	if _, _, err := ro.SetBoundary(ctx, mustParse(t, "no-weekend", "", boundary.SourceCLI), "2026-10-21", base); !errors.Is(err, ErrReadOnly) {
		t.Errorf("read-only write: %v", err)
	}
	if bs, err := ro.Boundaries(ctx); err != nil || len(bs) != 1 {
		t.Errorf("read back: %+v %v", bs, err)
	}
}

func TestUncategorizedApps(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	minute := func(day string, at time.Time, key, app string, cat model.Category, att float64) model.MinuteRollup {
		return model.MinuteRollup{Minute: at, LogicalDay: day, WindowKey: key, AppID: app, Category: cat,
			Monitor: 1, AttendedS: att, FocusedS: att, VisibleS: 60}
	}
	rows := []model.MinuteRollup{
		minute("2026-10-20", base, "k1", "com.new.tool", model.CategoryUncategorized, 30),
		minute("2026-10-21", base.Add(time.Minute), "k2", "com.new.tool", model.CategoryUncategorized, 40),
		minute("2026-10-21", base.Add(2*time.Minute), "k3", "com.other", model.CategoryUncategorized, 50),
		minute("2026-10-21", base.Add(3*time.Minute), "k4", "com.editor", model.CategoryCode, 60),
		minute("2026-10-21", base.Add(4*time.Minute), model.UnattributedWindowKey, "", model.CategoryUncategorized, 60),
		minute("2026-10-10", base.Add(5*time.Minute), "k5", "com.old", model.CategoryUncategorized, 60),
	}
	if err := s.WriteRollups(ctx, rows); err != nil {
		t.Fatal(err)
	}
	got, err := s.UncategorizedApps(ctx, "2026-10-19", "2026-10-25")
	if err != nil || len(got) != 2 || got[0].App != "com.new.tool" || got[0].Seconds != 70 || got[1].App != "com.other" {
		t.Errorf("%+v %v", got, err)
	}
}

// A database where a race left two active rules of a kind keeps the newest
// one active, ends the older one, and then refuses a second active rule.
func TestOneActiveBoundaryMigration(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP INDEX boundaries_one_active`,
		`CREATE INDEX boundaries_active ON boundaries(kind) WHERE until_day IS NULL`,
		`INSERT INTO boundaries (kind, value, since_day, source, created_ms) VALUES
			('focus_block', '60', '2026-10-21', 'cli', 1), ('focus_block', '90', '2026-10-24', 'dashboard', 2),
			('focus_block', '120', '2026-10-24', 'cli', 3), ('shutdown', '21:30', '2026-10-21', 'cli', 4)`,
		`DELETE FROM schema_migrations WHERE version >= 5`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.Close()
	if s, err = Open(ctx, dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	bs, err := s.Boundaries(ctx)
	if err != nil || len(bs) != 4 {
		t.Fatalf("%+v %v", bs, err)
	}
	if bs[0].Until != "2026-10-23" || bs[1].Until != "2026-10-24" || !bs[2].Active() || !bs[3].Active() {
		t.Errorf("after the migration: %+v", bs)
	}
	if cur := boundary.Current(bs); len(cur) != 2 || cur[1].Value != "120" {
		t.Errorf("current: %+v", cur)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO boundaries (kind, value, since_day, source, created_ms)
		VALUES ('shutdown', '22:00', '2026-10-25', 'cli', 5)`); err == nil {
		t.Error("a second active shutdown was stored")
	}
}

// SetBoundary reads the active rule, then writes. Run from the CLI or the
// dashboard while the daemon flushes, and from several places at once, it
// must neither fail with SQLITE_BUSY_SNAPSHOT nor leave two active rules
// of a kind.
func TestSetBoundaryConcurrentWithFlush(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	daemon, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = daemon.Close() }()
	cli, err := OpenExisting(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()

	stop := make(chan struct{})
	flushed := make(chan error, 1)
	go func() { // the daemon's flush, as often as it can
		for i := 0; ; i++ {
			select {
			case <-stop:
				flushed <- nil
				return
			default:
			}
			if err := daemon.WriteRollups(ctx, []model.MinuteRollup{{Minute: base.Add(time.Duration(i) * time.Minute),
				LogicalDay: "2026-10-21", WindowKey: "w", AppID: "code", Category: model.CategoryCode, AttendedS: 60}}); err != nil {
				flushed <- err
				return
			}
		}
	}()

	const writers, each = 4, 25
	errs := make(chan error, writers*each)
	done := make(chan struct{})
	for w := range writers {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := range each {
				b := boundary.Boundary{Kind: boundary.FocusBlock, Value: strconv.Itoa(15 + w*each + i), Source: boundary.SourceCLI}
				if _, _, err := cli.SetBoundary(ctx, b, "2026-10-21", base); err != nil {
					errs <- err
				}
			}
		}()
	}
	for range writers {
		<-done
	}
	close(stop)
	if err := <-flushed; err != nil {
		t.Fatalf("flush: %v", err)
	}
	close(errs)
	for err := range errs {
		t.Errorf("set boundary: %v", err)
	}
	bs, err := cli.Boundaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, b := range bs {
		if b.Active() {
			active++
		}
	}
	if active != 1 || len(bs) != writers*each {
		t.Errorf("%d rules, %d active, want %d and 1", len(bs), active, writers*each)
	}
}
