package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

func TestAggregate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	const canary = "com.CANARYZZ9.app"
	minute := func(day string, at time.Time, key, app string, cat model.Category, att float64) model.MinuteRollup {
		return model.MinuteRollup{Minute: at, LogicalDay: day, WindowKey: key, AppID: app, Category: cat,
			Monitor: 1, AttendedS: att, FocusedS: att, VisibleS: 60}
	}
	yesterday := base.Add(-day)
	rows := []model.MinuteRollup{
		// Yesterday: two code windows and a browser in one minute, an
		// unattributed row, and a row already per category (written after
		// the mode was turned on) in the same minute.
		minute("2026-10-20", yesterday, "k1", canary, model.CategoryCode, 20),
		minute("2026-10-20", yesterday, "k2", "com.other", model.CategoryCode, 10),
		minute("2026-10-20", yesterday, "k3", "com.browser", model.CategoryBrowser, 15),
		minute("2026-10-20", yesterday, model.UnattributedWindowKey, "", model.CategoryUncategorized, 5),
		minute("2026-10-20", yesterday, model.CategoryWindowKey(model.CategoryCode), "", model.CategoryCode, 4),
		minute("2026-10-20", yesterday.Add(time.Minute), "k1", canary, model.CategoryCode, 60),
		// Today is left alone until it closes.
		minute("2026-10-21", base, "k1", "com.today", model.CategoryCode, 30),
	}
	if err := s.WriteRollups(ctx, rows); err != nil {
		t.Fatal(err)
	}
	removed, err := s.Aggregate(ctx, "2026-10-21")
	if err != nil {
		t.Fatal(err)
	}
	// Four app rows became two category rows in the first minute (one new,
	// one merged into the existing code row); one became one in the second.
	if removed != 2 {
		t.Errorf("removed %d rows, want 2", removed)
	}
	type got struct {
		key, cat string
		app      *string
		att      float64
	}
	var all []got
	err = s.scanRows(ctx, `SELECT window_key, category, app_id, attended_s FROM attention_minutes
		WHERE logical_day = '2026-10-20' ORDER BY minute_ms, window_key`, nil, func(r *sql.Rows) error {
		var g got
		if err := r.Scan(&g.key, &g.cat, &g.app, &g.att); err != nil {
			return err
		}
		all = append(all, g)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []got{
		{"category:browser", "browser", nil, 15},
		{"category:code", "code", nil, 34},
		{model.UnattributedWindowKey, "uncategorized", nil, 5},
		{"category:code", "code", nil, 60},
	}
	if len(all) != len(want) {
		t.Fatalf("rows %+v", all)
	}
	for i := range want {
		if all[i].key != want[i].key || all[i].cat != want[i].cat || all[i].app != nil || all[i].att != want[i].att {
			t.Errorf("row %d = %+v, want %+v", i, all[i], want[i])
		}
	}
	// The day's minutes still say the same thing, by category.
	ms, err := s.DayMinutes(ctx, "2026-10-20")
	if err != nil || len(ms) != 2 || ms[0].AttendedS != 54 || ms[0].Label != "code" {
		t.Errorf("day minutes %+v, %v", ms, err)
	}
	if n, err := s.Aggregate(ctx, "2026-10-21"); err != nil || n != 0 {
		t.Errorf("second run removed %d, %v", n, err)
	}
	var today int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM attention_minutes WHERE app_id = 'com.today'`).Scan(&today); err != nil || today != 1 {
		t.Errorf("today's row: %d, %v", today, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		b, err := os.ReadFile(filepath.Join(dir, FileName+suffix))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("CANARYZZ9")) {
			t.Errorf("app id found in %s%s after aggregation", FileName, suffix)
		}
	}
}
