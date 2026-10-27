package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/boundary"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
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
	for _, q := range []string{`DROP TABLE boundaries`, `DELETE FROM schema_migrations WHERE version = 4`} {
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
