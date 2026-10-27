package app

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/annotation"
	"github.com/kshitijdhara/burntout/internal/domain/features"
	"github.com/kshitijdhara/burntout/internal/domain/model"
	"github.com/kshitijdhara/burntout/internal/domain/strain"
)

type fakeScoreStore struct {
	stored   []strain.Day
	key      string // the params key stored days were computed with
	withData []string
	keptFrom string // RawKeptFrom; "" keeps everything
	loaded   []string
	notes    []annotation.Annotation
}

func (f *fakeScoreStore) Annotations(context.Context, string, string) ([]annotation.Annotation, error) {
	return f.notes, nil
}

func (f *fakeScoreStore) DataDays(_ context.Context, from, to string) ([]string, error) {
	var out []string
	for _, d := range f.withData {
		if d >= from && d <= to {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeScoreStore) DayInput(_ context.Context, day string) (features.Day, error) {
	f.loaded = append(f.loaded, day)
	at, _ := time.Parse(time.DateOnly, day)
	at = at.Add(9 * time.Hour)
	var minutes []features.Minute
	for i := range 120 {
		minutes = append(minutes, features.Minute{At: at.Add(time.Duration(i) * time.Minute), Label: "code", Category: model.CategoryCode, AttendedS: 60})
	}
	return features.Day{Date: day, Minutes: minutes, Coverage: 1, Present: 2 * time.Hour}, nil
}

func (f *fakeScoreStore) FeatureDays(_ context.Context, from, to, key string) ([]strain.Day, error) {
	if key != f.key {
		return nil, nil
	}
	return f.inRange(from, to), nil
}

func (f *fakeScoreStore) OlderFeatureDays(_ context.Context, from, to, key string) ([]strain.Day, error) {
	if key == f.key {
		return nil, nil
	}
	return f.inRange(from, to), nil
}

// RawKeptFrom keeps every day's raw data unless keptFrom says otherwise.
func (f *fakeScoreStore) RawKeptFrom(context.Context) (string, error) {
	if f.keptFrom == "" {
		return "0001-01-01", nil
	}
	return f.keptFrom, nil
}

func (f *fakeScoreStore) inRange(from, to string) []strain.Day {
	var out []strain.Day
	for _, d := range f.stored {
		if d.Date >= from && d.Date <= to {
			out = append(out, d)
		}
	}
	return out
}

func TestScoreDays(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) // logical day 2026-10-25
	st := &fakeScoreStore{
		stored: []strain.Day{
			{Date: "2026-10-20", Coverage: 1, Present: time.Hour, Values: map[string]float64{"attended_hours": 1}},
			{Date: "2026-10-23", Coverage: 1, Present: time.Hour, Values: map[string]float64{"attended_hours": 1}}, // recomputed
		},
		withData: []string{"2026-10-20", "2026-10-21", "2026-10-23", "2026-10-24", "2026-10-25"},
		key:      features.DefaultKey,
	}
	opts := DefaultScoreOptions()
	sc, err := ScoreDays(context.Background(), st, now, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Stored and old: reused. Not stored: computed. The last two completed
	// days: always recomputed. Today: left out.
	if !slices.Equal(st.loaded, []string{"2026-10-21", "2026-10-23", "2026-10-24"}) {
		t.Errorf("computed %v", st.loaded)
	}
	var computed, scored []string
	for _, d := range sc.Computed {
		computed = append(computed, d.Date)
	}
	for _, s := range sc.Scores {
		scored = append(scored, s.Date)
	}
	if !slices.Equal(computed, st.loaded) {
		t.Errorf("Computed = %v", computed)
	}
	if !slices.Equal(scored, []string{"2026-10-20", "2026-10-21", "2026-10-23", "2026-10-24"}) {
		t.Errorf("scored %v", scored)
	}
	if v := sc.Computed[0].Values["attended_hours"]; v != 2 {
		t.Errorf("computed attended hours %v, want 2", v)
	}
	// Look-back features see the usable days before each day, stored or
	// computed: the first stored day has none, 10-21 sees 10-20, and
	// 10-24 sees three days, all light under the 4 h fallback.
	if _, ok := sc.Computed[0].Values["days_since_light_day"]; !ok {
		t.Error("10-21 has no days since a light day")
	}
	if v := sc.Computed[2].Values["days_since_light_day"]; v != 1 {
		t.Errorf("10-24 days since a light day %v, want 1", v)
	}

	// A vacation annotation excludes a day already stored, and the day is
	// reported as missing, not rest.
	st.notes = []annotation.Annotation{{From: "2026-10-20", To: "2026-10-20", Kind: annotation.Vacation}}
	sc, err = ScoreDays(context.Background(), st, now, opts)
	if err != nil {
		t.Fatal(err)
	}
	if s := sc.Scores[0]; s.Date != "2026-10-20" || s.Status != strain.StatusMissing || !strings.Contains(s.Reason, "vacation") {
		t.Errorf("vacation day scored: %+v", s)
	}
	st.notes = nil

	// New core hours: stored days were computed with the old ones, so every
	// day with raw data is computed again.
	st.loaded = nil
	changed := opts
	changed.Features.CoreStart = 7 * time.Hour
	if _, err := ScoreDays(context.Background(), st, now, changed); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(st.loaded, []string{"2026-10-20", "2026-10-21", "2026-10-23", "2026-10-24"}) {
		t.Errorf("after a core-hours change computed %v", st.loaded)
	}

	// Raw data kept only from 10-22: the older stored 10-20 keeps its
	// values, 10-21 (nothing stored) isn't scored from partial data, and
	// the recent days are still computed.
	st.loaded, st.keptFrom = nil, "2026-10-22"
	sc, err = ScoreDays(context.Background(), st, now, changed)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(st.loaded, []string{"2026-10-23", "2026-10-24"}) {
		t.Errorf("with raw data trimmed computed %v", st.loaded)
	}
	if sc.Scores[0].Date != "2026-10-20" || len(sc.Scores) != 3 {
		t.Errorf("with raw data trimmed scored %+v", sc.Scores)
	}
	st.keptFrom = ""

	st.loaded = nil
	opts.IncludeToday = true
	sc, err = ScoreDays(context.Background(), st, now, opts)
	if err != nil {
		t.Fatal(err)
	}
	if last := sc.Scores[len(sc.Scores)-1]; last.Date != "2026-10-25" || slices.ContainsFunc(sc.Computed, func(d strain.Day) bool { return d.Date == "2026-10-25" }) {
		t.Errorf("today: last score %s, computed %+v", last.Date, sc.Computed)
	}
}
