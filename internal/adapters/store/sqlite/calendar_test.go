package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/model"
)

func TestCalendarSync(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	const rollover = 4 * time.Hour
	at := func(day, hh int) time.Time { return time.Date(2026, 10, day, hh, 0, 0, 0, time.UTC) }
	block := func(day, hh int, tentative bool) BusyBlock {
		return BusyBlock{Start: at(day, hh), End: at(day, hh).Add(30 * time.Minute), Tentative: tentative}
	}
	busyCount := func() int {
		t.Helper()
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM intervals WHERE kind = 'busy'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	days := []string{"2026-10-19", "2026-10-20", "2026-10-21"}
	n, err := s.ReplaceBusy(ctx, at(19, 0), at(22, 0), []BusyBlock{block(20, 9, false), block(21, 14, true), block(25, 9, false)}, days, rollover)
	if err != nil || n != 2 {
		t.Fatalf("first sync stored %d, %v (a block outside the window is skipped)", n, err)
	}
	// A later sync replaces the window: the 14:00 meeting was cancelled.
	if n, err := s.ReplaceBusy(ctx, at(19, 0), at(22, 0), []BusyBlock{block(20, 9, false)}, days, rollover); err != nil || n != 1 || busyCount() != 1 {
		t.Fatalf("resync: %d stored, %d kept, %v", n, busyCount(), err)
	}

	d, err := s.DayInput(ctx, "2026-10-20")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Calendar || len(d.Busy) != 1 || d.Busy[0].State != "confirmed" || d.Busy[0].Duration() != 30*time.Minute {
		t.Errorf("day input: calendar %v, busy %+v", d.Calendar, d.Busy)
	}
	if d, _ := s.DayInput(ctx, "2026-10-22"); d.Calendar {
		t.Error("an unsynced day counts as covered")
	}

	// After forget, a sync can't bring back meetings from before it ran.
	if err := s.Forget(ctx, time.UnixMilli(0), "0000-01-01"); err != nil {
		t.Fatal(err)
	}
	if busyCount() != 0 {
		t.Fatal("forget left busy blocks")
	}
	future := BusyBlock{Start: time.Now().Add(time.Hour), End: time.Now().Add(2 * time.Hour)}
	n, err = s.ReplaceBusy(ctx, at(19, 0), time.Now().Add(48*time.Hour), []BusyBlock{block(20, 9, false), future},
		append(days, model.LogicalDay(time.Now().Add(time.Hour), rollover)), rollover)
	if err != nil || n != 1 || busyCount() != 1 {
		t.Fatalf("after forget: %d stored, %d kept, %v (only the meeting after the forget)", n, busyCount(), err)
	}
	if d, _ := s.DayInput(ctx, "2026-10-20"); d.Calendar {
		t.Error("a forgotten day is marked covered again")
	}

	if err := s.RemoveCalendar(ctx); err != nil || busyCount() != 0 {
		t.Fatalf("remove calendar: %v, %d left", err, busyCount())
	}
	var covered int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM calendar_days`).Scan(&covered); err != nil || covered != 0 {
		t.Errorf("calendar days left: %d %v", covered, err)
	}
}

func TestCalendarRetention(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	old := base.AddDate(0, 0, -200)
	if _, err := s.ReplaceBusy(ctx, old.Add(-time.Hour), base.Add(time.Hour),
		[]BusyBlock{{Start: old, End: old.Add(time.Hour)}, {Start: base, End: base.Add(30 * time.Minute)}},
		[]string{old.Format(time.DateOnly), base.Format(time.DateOnly)}, 4*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunRetention(ctx, base, DefaultRetention()); err != nil {
		t.Fatal(err)
	}
	var busy, days int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM intervals WHERE kind = 'busy'`).Scan(&busy)
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM calendar_days`).Scan(&days)
	if busy != 1 || days != 1 {
		t.Errorf("after retention: %d busy blocks, %d calendar days; want the recent one of each", busy, days)
	}
}
