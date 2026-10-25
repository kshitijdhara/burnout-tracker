package sqlite

import (
	"context"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/features"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/strain"
)

func TestDayInput(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	zone := time.FixedZone("", 330*60)
	local := func(hh, mm int) time.Time { return time.Date(2026, 10, 21, hh, mm, 0, 0, zone) }
	d, prev := "2026-10-21", "2026-10-20"
	exit1 := 1
	if err := s.WriteRollups(ctx, []model.MinuteRollup{
		{Minute: local(9, 0).UTC(), UTCOffsetMin: 330, LogicalDay: d, WindowKey: "e", AppID: "code", Category: model.CategoryCode, AttendedS: 60},
	}); err != nil {
		t.Fatal(err)
	}
	ivs := []model.Interval{
		{Start: local(9, 0), End: local(10, 0), UTCOffsetMin: 330, LogicalDay: d, Kind: model.IntervalPresence, Attrs: model.IntervalAttrs{Reason: "input"}},
		{Start: local(9, 5), End: local(9, 6), UTCOffsetMin: 330, LogicalDay: d, Kind: model.IntervalShellCmd,
			Attrs: model.IntervalAttrs{Argv0: "go", Group: "build", ExitCode: &exit1}},
		{Start: local(9, 10), End: local(9, 40), UTCOffsetMin: 330, LogicalDay: d, Kind: model.IntervalAgentState, SessionKey: "s1",
			Attrs: model.IntervalAttrs{State: "working"}},
		{Start: local(9, 10), End: local(9, 20), UTCOffsetMin: 330, LogicalDay: d, Kind: model.IntervalHumanState,
			Attrs: model.IntervalAttrs{State: "reading"}},
		// The previous day ended at 23:30 local.
		{Start: local(22, 0).AddDate(0, 0, -1), End: local(23, 30).AddDate(0, 0, -1), UTCOffsetMin: 330, LogicalDay: prev,
			Kind: model.IntervalPresence, Attrs: model.IntervalAttrs{Reason: "input"}},
	}
	if err := s.WriteIntervals(ctx, ivs); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteHealth(ctx, d, `{"coverage":0.8}`); err != nil {
		t.Fatal(err)
	}

	in, err := s.DayInput(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if in.Date != d || in.Weekend || len(in.Minutes) != 1 || len(in.Presence) != 1 || len(in.Shell) != 1 ||
		len(in.Agent) != 1 || len(in.Human) != 1 {
		t.Fatalf("day input: %+v", in)
	}
	if !in.Minutes[0].At.Equal(local(9, 0)) || in.Minutes[0].Category != model.CategoryCode {
		t.Errorf("minute: %+v", in.Minutes[0])
	}
	if c := in.Shell[0]; c.Argv0 != "go" || c.Group != "build" || c.Exit != 1 {
		t.Errorf("shell: %+v", c)
	}
	if _, off := in.Presence[0].Start.Zone(); off != 330*60 || in.Present != time.Hour {
		t.Errorf("presence: %+v, present %v", in.Presence[0], in.Present)
	}
	if !in.PrevLastPresence.Equal(local(23, 30).AddDate(0, 0, -1)) {
		t.Errorf("previous day's last presence: %v", in.PrevLastPresence)
	}
	if in.Coverage != 0.8 {
		t.Errorf("coverage %v", in.Coverage)
	}
	if v := features.Values(in, features.DefaultParams()); math.Abs(v["overnight_gap"]-9.5) > 1e-9 {
		t.Errorf("overnight gap %v, want 9.5 h", v["overnight_gap"])
	}

	days, err := s.DataDays(ctx, "2026-10-01", "2026-10-31")
	if err != nil || !slices.Equal(days, []string{prev, d}) {
		t.Fatalf("data days: %v %v", days, err)
	}
}

func TestFeatureAndScoreRoundTrip(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	days := []strain.Day{
		{Date: "2026-10-20", Coverage: 0.9, Present: 5 * time.Hour, Values: map[string]float64{"attended_hours": 6.5}},
		{Date: "2026-10-21", Coverage: 0.4, Present: time.Hour, Values: map[string]float64{}},
	}
	if err := s.SaveFeatureDays(ctx, days); err != nil {
		t.Fatal(err)
	}
	days[0].Values["attended_hours"] = 7 // a recompute replaces the row
	if err := s.SaveFeatureDays(ctx, days[:1]); err != nil {
		t.Fatal(err)
	}
	got, err := s.FeatureDays(ctx, "2026-10-01", "2026-10-31")
	if err != nil || len(got) != 2 {
		t.Fatalf("feature days: %+v %v", got, err)
	}
	if g := got[0]; g.Date != "2026-10-20" || g.Coverage != 0.9 || g.Present != 5*time.Hour || g.Values["attended_hours"] != 7 {
		t.Errorf("day 0: %+v", g)
	}
	// Rows from another registry version are recomputed, not reused.
	if _, err := s.db.ExecContext(ctx, `UPDATE daily_features SET registry_ver = 0 WHERE logical_day = '2026-10-21'`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.FeatureDays(ctx, "2026-10-01", "2026-10-31"); len(got) != 1 {
		t.Errorf("old registry version returned: %+v", got)
	}

	scores := []strain.Score{
		{Date: "2026-10-20", Status: strain.StatusOK, Strain: 42, Composite: 2.6, UsableDays: 20,
			Drivers: []strain.Driver{{Feature: "attended_hours", Label: "hours", HigherWorse: true, Deviation: 1.2}}},
		{Date: "2026-10-21", Status: strain.StatusMissing, Reason: "device barely used", UsableDays: 20},
	}
	if err := s.SaveScores(ctx, scores); err != nil {
		t.Fatal(err)
	}
	var n int
	var nullStrain int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), SUM(strain IS NULL) FROM scores WHERE model_ver = ?`,
		strain.ModelVersion).Scan(&n, &nullStrain); err != nil || n != 2 || nullStrain != 1 {
		t.Fatalf("scores: %d rows, %d without a value, %v", n, nullStrain, err)
	}
}

func TestReadOnlyStoreRefusesFeatureWrites(t *testing.T) {
	dir := t.TempDir()
	rw, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = rw.Close()
	ro, err := OpenReadOnly(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	if err := ro.SaveFeatureDays(context.Background(), []strain.Day{{Date: "2026-10-20"}}); err == nil {
		t.Error("read-only store saved features")
	}
	if _, err := ro.DayInput(context.Background(), "2026-10-20"); err != nil {
		t.Errorf("read-only DayInput: %v", err)
	}
}
