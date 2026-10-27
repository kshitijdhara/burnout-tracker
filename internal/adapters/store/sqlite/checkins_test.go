package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/checkin"
)

func TestCheckinsRoundTrip(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	week := []checkin.Record{
		{At: base, Instrument: checkin.CBIPersonalID, Answers: checkin.Answers{"p1": 50}, Score: 50},
		{At: base, Instrument: checkin.CBIWorkID, Answers: checkin.Answers{"w1": 75}, Score: 75},
	}
	old := checkin.Record{At: base.Add(-30 * 24 * time.Hour), Instrument: checkin.DailyID, Answers: checkin.Answers{"drained": 25}, Score: 25}
	if err := s.SaveCheckins(ctx, append(week, old)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Checkins(ctx, base.Add(-time.Hour))
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got[0].Instrument != checkin.CBIPersonalID || got[1].Score != 75 || got[1].Answers["w1"] != 75 || !got[0].At.Equal(base) {
		t.Errorf("round trip: %+v", got)
	}
	all, _ := s.Checkins(ctx, time.Time{})
	if len(all) != 3 || all[0].Instrument != checkin.DailyID {
		t.Errorf("all: %+v", all)
	}
}

func TestOpenExistingWritesWithoutMigrating(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := OpenExisting(ctx, dir); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("no database: %v", err)
	}
	daemon, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = daemon.Close() }()
	cli, err := OpenExisting(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := checkin.Record{At: base, Instrument: checkin.DailyID, Answers: checkin.Answers{"drained": 50}, Score: 50}
	if err := cli.SaveCheckins(ctx, []checkin.Record{rec}); err != nil {
		t.Fatal(err)
	}
	_ = cli.Close()
	if got, _ := daemon.Checkins(ctx, time.Time{}); len(got) != 1 {
		t.Errorf("daemon sees %d check-ins", len(got))
	}
	// A schema this build doesn't know is refused, not migrated.
	if _, err := daemon.db.ExecContext(ctx, `INSERT INTO schema_migrations VALUES (999, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExisting(ctx, dir); !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("newer schema: %v", err)
	}
}
