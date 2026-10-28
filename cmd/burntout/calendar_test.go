package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/egress/ics"
	"github.com/kshitijdhara/burntout/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burntout/internal/domain/model"
	"github.com/kshitijdhara/burntout/internal/domain/schedule"
)

// Meetings that start in private hours are never stored, and a sync
// records its outcome without the source's secret parts.
func TestSyncSkipsPrivateHours(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	day := time.Now().Add(-48 * time.Hour)
	at := func(hh int) time.Time { return time.Date(day.Year(), day.Month(), day.Day(), hh, 0, 0, 0, time.Local) }
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\n")
	for _, hh := range []int{10, 20} {
		b.WriteString("BEGIN:VEVENT\r\nDTSTART:" + at(hh).UTC().Format("20060102T150405Z") + "\r\nDURATION:PT1H\r\nEND:VEVENT\r\n")
	}
	b.WriteString("END:VCALENDAR\r\n")
	file := filepath.Join(dir, "cal.ics")
	if err := os.WriteFile(file, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := ics.Parse(model.NewSensitive(file))
	if err != nil {
		t.Fatal(err)
	}
	evenings, _ := schedule.Parse("19:00-23:00")
	n, err := syncInto(ctx, store, dir, src, time.Now(), schedule.Schedule{evenings})
	if err != nil || n != 1 {
		t.Fatalf("stored %d meetings, %v; want only the 10:00 one", n, err)
	}
	st, ok := readCalendarStatus(dir)
	if !ok || !st.OK || st.Blocks != 1 || st.Source != "cal.ics" {
		t.Errorf("status %+v", st)
	}
}
