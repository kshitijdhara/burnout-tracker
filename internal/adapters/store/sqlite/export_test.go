package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/export"
	"github.com/kshitijdhara/burntout/internal/domain/annotation"
	"github.com/kshitijdhara/burntout/internal/domain/boundary"
	"github.com/kshitijdhara/burntout/internal/domain/checkin"
	"github.com/kshitijdhara/burntout/internal/domain/model"
	"github.com/kshitijdhara/burntout/internal/domain/strain"
)

func TestExport(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	zone := time.FixedZone("", 2*3600)
	at := base.In(zone) // 2026-10-21 12:00 +02:00
	exit := 2
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.WriteRollups(ctx, []model.MinuteRollup{
		{Minute: at, UTCOffsetMin: 120, LogicalDay: "2026-10-21", WindowKey: "k", AppID: "com.apple.Terminal", Category: model.CategoryTerminal, AttendedS: 50, FocusedS: 60, VisibleS: 60},
		{Minute: at, UTCOffsetMin: 120, LogicalDay: "2026-10-21", WindowKey: model.UnattributedWindowKey, Category: model.CategoryUncategorized, AttendedS: 10},
		{Minute: at.AddDate(0, 0, -30), UTCOffsetMin: 120, LogicalDay: "2026-09-21", WindowKey: "k", AppID: "old", Category: model.CategoryCode, AttendedS: 1},
	}))
	must(s.WriteIntervals(ctx, []model.Interval{{Start: at, End: at.Add(3 * time.Second), UTCOffsetMin: 120, LogicalDay: "2026-10-21",
		Kind: model.IntervalShellCmd, Attrs: model.IntervalAttrs{Argv0: "git", Subcommand: "push", ExitCode: &exit, ProjectKey: "pk", Group: "vcs"}}}))
	must(s.SaveFeatureDays(ctx, []strain.Day{{Date: "2026-10-20", Coverage: 0.9, Present: time.Hour, Values: map[string]float64{"breaks": 3}}}, "k"))
	must(s.SaveScores(ctx, []strain.Score{
		{Date: "2026-10-20", Status: strain.StatusOK, Strain: 42, Drivers: []strain.Driver{{Feature: "breaks"}}},
		{Date: "2026-10-19", Status: strain.StatusCalibrating},
	}))
	must(s.SaveCheckins(ctx, []checkin.Record{
		{At: at, Instrument: checkin.CBIWorkID, Score: 50, Answers: checkin.Answers{"w1": 50}},
		{At: at.AddDate(0, 0, -30), Instrument: checkin.CBIWorkID, Score: 25, Answers: checkin.Answers{"w1": 25}},
	}))
	_, err := s.AddAnnotation(ctx, annotation.Annotation{From: "2026-10-24", To: "2026-10-27", Kind: annotation.Vacation})
	must(err)
	// Ended before the range, ended inside it, never applied (set and
	// removed the same day), and still active.
	set := func(kind, value, today, source string) int64 {
		t.Helper()
		b, err := boundary.Parse(kind, value)
		must(err)
		b.Source = source
		id, _, err := s.SetBoundary(ctx, b, today, at)
		must(err)
		return id
	}
	old := set("shutdown", "22:00", "2026-09-01", boundary.SourceCLI)
	_, err = s.RemoveBoundary(ctx, old, "2026-09-15", at)
	must(err)
	set("shutdown", "21:30", "2026-09-20", boundary.SourceCLI)
	set("shutdown", "20:00", "2026-10-10", boundary.SourceReview) // ends the 21:30 rule on 10-10
	never := set("focus-block", "90", "2026-10-12", boundary.SourceDashboard)
	_, err = s.RemoveBoundary(ctx, never, "2026-10-12", at)
	must(err)

	r := export.Range{From: "2026-10-01", To: "2026-10-25",
		Start: time.Date(2026, 10, 1, 4, 0, 0, 0, zone), End: time.Date(2026, 10, 26, 4, 0, 0, 0, zone)}
	var b bytes.Buffer
	var n export.Counts
	must(s.Export(ctx, func(src export.Source) error {
		n, err = export.WriteJSON(ctx, &b, src, export.Meta{ExportedAt: at, AppVersion: "test", Range: r})
		return err
	}))
	if n != (export.Counts{Minutes: 2, Intervals: 1, Days: 2, Checkins: 1, Annotations: 1, Boundaries: 2}) {
		t.Errorf("counts %+v (the September rows are outside the range)", n)
	}
	var doc struct {
		Minutes     []export.Minute     `json:"minutes"`
		Intervals   []export.Interval   `json:"intervals"`
		Days        []export.Day        `json:"days"`
		Checkins    []export.Checkin    `json:"checkins"`
		Annotations []export.Annotation `json:"annotations"`
		Boundaries  []export.Boundary   `json:"boundaries"`
	}
	must(json.Unmarshal(b.Bytes(), &doc))
	if m := doc.Minutes[0]; m.Minute != "2026-10-21T12:00:00+02:00" || m.App != "com.apple.Terminal" || m.Unattributed {
		t.Errorf("minute %+v", m)
	}
	if m := doc.Minutes[1]; !m.Unattributed || m.App != "" {
		t.Errorf("unattributed minute %+v", m)
	}
	if iv := doc.Intervals[0]; iv.Command != "git" || iv.Subcommand != "push" || iv.Exit == nil || *iv.Exit != 2 ||
		iv.Project != "pk" || iv.End != "2026-10-21T12:00:03+02:00" {
		t.Errorf("interval %+v", iv)
	}
	// A day with only a score, and one with features and a score.
	if d := doc.Days[0]; d.Day != "2026-10-19" || d.Status != "calibrating" || d.Strain != nil || d.Features != nil {
		t.Errorf("day %+v", d)
	}
	if d := doc.Days[1]; d.Day != "2026-10-20" || d.Strain == nil || *d.Strain != 42 || d.Features["breaks"] != 3 ||
		d.Coverage == nil || *d.PresentS != 3600 || len(d.Drivers) != 1 || d.RegistryVersion == 0 {
		t.Errorf("day %+v", d)
	}
	if c := doc.Checkins[0]; c.Score != 50 || c.Answers["w1"] != 50 || c.At != "2026-10-21T12:00:00+02:00" {
		t.Errorf("check-in %+v", c)
	}
	if a := doc.Annotations[0]; a.Kind != "vacation" || a.To != "2026-10-27" {
		t.Errorf("annotation %+v", a)
	}
	want := []export.Boundary{
		{Kind: "shutdown", Value: "21:30", Since: "2026-09-21", Until: "2026-10-10", Source: "cli"},
		{Kind: "shutdown", Value: "20:00", Since: "2026-10-11", Source: "review"},
	}
	if !slices.Equal(doc.Boundaries, want) {
		t.Errorf("boundaries %+v", doc.Boundaries)
	}
}
