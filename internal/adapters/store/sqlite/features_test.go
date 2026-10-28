package sqlite

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/app"
	"github.com/kshitijdhara/burntout/internal/domain/annotation"
	"github.com/kshitijdhara/burntout/internal/domain/checkin"
	"github.com/kshitijdhara/burntout/internal/domain/features"
	"github.com/kshitijdhara/burntout/internal/domain/model"
	"github.com/kshitijdhara/burntout/internal/domain/strain"
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
	if err := s.SaveFeatureDays(ctx, days, features.DefaultKey); err != nil {
		t.Fatal(err)
	}
	days[0].Values["attended_hours"] = 7 // a recompute replaces the row
	if err := s.SaveFeatureDays(ctx, days[:1], features.DefaultKey); err != nil {
		t.Fatal(err)
	}
	got, err := s.FeatureDays(ctx, "2026-10-01", "2026-10-31", features.DefaultKey)
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
	if got, _ := s.FeatureDays(ctx, "2026-10-01", "2026-10-31", features.DefaultKey); len(got) != 1 {
		t.Errorf("old registry version returned: %+v", got)
	}
	// Days computed with other core hours aren't returned, so they are
	// recomputed. Days stored before keys existed count as the defaults.
	if err := s.SaveFeatureDays(ctx, []strain.Day{{Date: "2026-10-22", Coverage: 1}}, "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO daily_features (logical_day, registry_ver, coverage, features)
		VALUES ('2026-10-19', ?, 1, '{"present_s":60,"values":{}}')`, features.RegistryVersion); err != nil {
		t.Fatal(err)
	}
	var dates []string
	got, _ = s.FeatureDays(ctx, "2026-10-01", "2026-10-31", features.DefaultKey)
	for _, d := range got {
		dates = append(dates, d.Date)
	}
	if strings.Join(dates, ",") != "2026-10-19,2026-10-20" {
		t.Errorf("default-key days %v", dates)
	}
	if got, _ := s.FeatureDays(ctx, "2026-10-01", "2026-10-31", "other"); len(got) != 1 || got[0].Date != "2026-10-22" {
		t.Errorf("other-key days %+v", got)
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

// A registry bump after the minutes were trimmed shorter than the
// intervals: only days whose raw data is all kept are recomputed, and the
// older days keep their stored rows (system design §4.6).
func TestRegistryBumpKeepsDaysWithTrimmedMinutes(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) // logical day 2026-10-25
	var rollups []model.MinuteRollup
	var ivs []model.Interval
	var old []strain.Day
	for d := 1; d <= 24; d++ {
		date := time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC)
		ld := date.Format(time.DateOnly)
		for m := range 120 { // 09:00–11:00 of code
			rollups = append(rollups, model.MinuteRollup{Minute: date.Add(9*time.Hour + time.Duration(m)*time.Minute),
				LogicalDay: ld, WindowKey: "w", AppID: "code", Category: model.CategoryCode, AttendedS: 60})
		}
		ivs = append(ivs, model.Interval{Start: date.Add(9 * time.Hour), End: date.Add(11 * time.Hour), LogicalDay: ld,
			Kind: model.IntervalPresence, Attrs: model.IntervalAttrs{Reason: "input"}})
		if err := s.WriteHealth(ctx, ld, `{"coverage":1}`); err != nil {
			t.Fatal(err)
		}
		old = append(old, strain.Day{Date: ld, Coverage: 1, Present: 6 * time.Hour, Values: map[string]float64{"attended_hours": 6}})
	}
	if err := s.WriteRollups(ctx, rollups); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteIntervals(ctx, ivs); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveFeatureDays(ctx, old, features.DefaultKey); err != nil {
		t.Fatal(err)
	}
	prev := features.RegistryVersion - 1
	if _, err := s.db.ExecContext(ctx, `UPDATE daily_features SET registry_ver = ?`, prev); err != nil {
		t.Fatal(err)
	}
	// retention.minutes_days = 7: the minutes before 10-18 12:00 go, so
	// 10-19 is the oldest day with minutes. The oldest day left may have
	// lost its morning, so 10-20 is the first day known to be whole. The
	// intervals stay for 180 days.
	r := DefaultRetention()
	r.Rollups = 7 * day
	if _, err := s.RunRetention(ctx, now, r); err != nil {
		t.Fatal(err)
	}
	if from, err := s.RawKeptFrom(ctx); err != nil || from != "2026-10-20" {
		t.Fatalf("raw kept from %q, %v", from, err)
	}

	sc, err := app.ScoreDays(ctx, s, now, app.DefaultScoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	var computed []string
	for _, d := range sc.Computed {
		computed = append(computed, d.Date)
	}
	if !slices.Equal(computed, []string{"2026-10-20", "2026-10-21", "2026-10-22", "2026-10-23", "2026-10-24"}) {
		t.Errorf("recomputed %v", computed)
	}
	if len(sc.Scores) != 24 || sc.Scores[0].Date != "2026-10-01" {
		t.Errorf("scored %d days from %s, want all 24 from 2026-10-01", len(sc.Scores), sc.Scores[0].Date)
	}
	if err := s.SaveFeatureDays(ctx, sc.Computed, features.DefaultKey); err != nil {
		t.Fatal(err)
	}
	kept, err := s.OlderFeatureDays(ctx, "2026-10-01", "2026-10-31", features.DefaultKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 19 || kept[18].Date != "2026-10-19" {
		t.Fatalf("older rows kept: %d, last %+v", len(kept), kept[len(kept)-1])
	}
	for _, d := range kept {
		if d.Values["attended_hours"] != 6 || d.Present != 6*time.Hour {
			t.Errorf("%s was overwritten: %+v", d.Date, d)
		}
	}
	current, err := s.FeatureDays(ctx, "2026-10-01", "2026-10-31", features.DefaultKey)
	if err != nil || len(current) != 5 || current[0].Values["attended_hours"] != 2 {
		t.Errorf("recomputed rows: %+v %v", current, err)
	}

	// With every minute gone, no day is whole.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM attention_minutes`); err != nil {
		t.Fatal(err)
	}
	if from, err := s.RawKeptFrom(ctx); err != nil || from != "" {
		t.Errorf("raw kept from %q, %v with no minutes", from, err)
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
	if err := ro.SaveFeatureDays(context.Background(), []strain.Day{{Date: "2026-10-20"}}, features.DefaultKey); err == nil {
		t.Error("read-only store saved features")
	}
	if _, err := ro.DayInput(context.Background(), "2026-10-20"); err != nil {
		t.Errorf("read-only DayInput: %v", err)
	}
}

func TestForget(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	at := func(m int) time.Time { return base.Add(time.Duration(m) * time.Minute) }
	d := "2026-10-21"
	exit := 0
	if err := s.WriteRollups(ctx, []model.MinuteRollup{
		{Minute: at(0), LogicalDay: d, WindowKey: "a", Category: model.CategoryCode, AttendedS: 60},
		{Minute: at(9), LogicalDay: d, WindowKey: "a", Category: model.CategoryCode, AttendedS: 60}, // since falls inside it
		{Minute: at(20), LogicalDay: d, WindowKey: "a", Category: model.CategoryCode, AttendedS: 60},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteIntervals(ctx, []model.Interval{
		{Start: at(0), End: at(5), LogicalDay: d, Kind: model.IntervalShellCmd, Attrs: model.IntervalAttrs{Argv0: "a", ExitCode: &exit}},
		{Start: at(5), End: at(15), LogicalDay: d, Kind: model.IntervalPresence}, // runs into since
		{Start: at(20), End: at(21), LogicalDay: d, Kind: model.IntervalShellCmd, Attrs: model.IntervalAttrs{Argv0: "b", ExitCode: &exit}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteHealth(ctx, d, `{}`); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCheckins(ctx, []checkin.Record{
		{At: at(1), Instrument: checkin.DailyID, Answers: checkin.Answers{"x": 1}},
		{At: at(30), Instrument: checkin.DailyID, Answers: checkin.Answers{"x": 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAnnotation(ctx, annotation.Annotation{From: d, To: d, Kind: annotation.OnCall}); err != nil {
		t.Fatal(err)
	}
	// A recovery copy made before since survives; one made after goes.
	if err := s.Backup(ctx, base); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(s.dir, backupDir, "burntout-20261020.db")
	if err := os.Rename(filepath.Join(s.dir, backupDir, "burntout-20261021.db"), old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(old, at(-60), at(-60)); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, base); err != nil {
		t.Fatal(err)
	}
	newer := filepath.Join(s.dir, backupDir, "burntout-20261021.db")
	if err := os.Chtimes(newer, at(30), at(30)); err != nil {
		t.Fatal(err)
	}

	if err := s.Forget(ctx, at(9).Add(30*time.Second), d); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		var n int
		if err := s.db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT COUNT(*) FROM attention_minutes`); n != 1 {
		t.Errorf("%d attention minutes left, want only the one before since", n)
	}
	if n := count(`SELECT COUNT(*) FROM intervals`); n != 1 {
		t.Errorf("%d intervals left, want only the command that ended before since", n)
	}
	if n := count(`SELECT COUNT(*) FROM checkins`); n != 1 {
		t.Errorf("%d check-ins left", n)
	}
	if n := count(`SELECT COUNT(*) FROM health_daily`); n != 0 {
		t.Errorf("health for the forgotten day kept")
	}
	if n := count(`SELECT COUNT(*) FROM annotations`); n != 1 {
		t.Errorf("annotations removed")
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("copy made before since removed: %v", err)
	}
	if _, err := os.Stat(newer); !os.IsNotExist(err) {
		t.Errorf("copy made after since kept")
	}
}

func TestAnnotations(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	id, err := s.AddAnnotation(ctx, annotation.Annotation{From: "2026-10-20", To: "2026-10-24", Kind: annotation.Vacation, Note: "Lisbon"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAnnotation(ctx, annotation.Annotation{From: "2026-10-20", To: "2026-10-19", Kind: annotation.Sick}); err == nil {
		t.Error("stored an invalid annotation")
	}
	got, err := s.Annotations(ctx, "2026-10-24", "2026-10-30")
	if err != nil || len(got) != 1 || got[0].ID != id || got[0].Note != "Lisbon" || got[0].Kind != annotation.Vacation {
		t.Fatalf("annotations %+v %v", got, err)
	}
	if got, _ := s.Annotations(ctx, "2026-10-25", "2026-10-30"); len(got) != 0 {
		t.Errorf("non-overlapping: %+v", got)
	}
	if ok, err := s.RemoveAnnotation(ctx, id); !ok || err != nil {
		t.Fatalf("remove: %v %v", ok, err)
	}
	if ok, _ := s.RemoveAnnotation(ctx, id); ok {
		t.Error("removed twice")
	}
}
