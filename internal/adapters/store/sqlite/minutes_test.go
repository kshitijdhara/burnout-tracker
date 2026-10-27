package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/model"
)

func TestDayMinutes(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	d := "2026-10-21"
	m0, m1, m2 := base, base.Add(time.Minute), base.Add(2*time.Minute)
	rollups := []model.MinuteRollup{
		// Minute 0: two browser windows together beat the editor.
		{Minute: m0, UTCOffsetMin: 330, LogicalDay: d, WindowKey: "b1", AppID: "com.google.Chrome", Category: model.CategoryBrowser, AttendedS: 20},
		{Minute: m0, UTCOffsetMin: 330, LogicalDay: d, WindowKey: "b2", AppID: "com.google.Chrome", Category: model.CategoryBrowser, AttendedS: 20},
		{Minute: m0, UTCOffsetMin: 330, LogicalDay: d, WindowKey: "e", AppID: "code", Category: model.CategoryCode, AttendedS: 30},
		// Minute 1: a private window is labelled by its category only.
		{Minute: m1, UTCOffsetMin: 330, LogicalDay: d, WindowKey: "p", Category: model.CategoryPrivate, AttendedS: 50},
		// Minute 2: unattributed presence.
		{Minute: m2, UTCOffsetMin: 330, LogicalDay: d, WindowKey: model.UnattributedWindowKey, Category: model.CategoryUncategorized, AttendedS: 40},
		{Minute: m2.Add(-24 * time.Hour), LogicalDay: "2026-10-20", WindowKey: "x", AppID: "old", Category: model.CategoryCode, AttendedS: 60},
	}
	if err := s.WriteRollups(ctx, rollups); err != nil {
		t.Fatal(err)
	}
	// A call from 30 s into minute 0 to 45 s into minute 1.
	call := model.Interval{Start: m0.Add(30 * time.Second), End: m1.Add(45 * time.Second), LogicalDay: d,
		Kind: model.IntervalPresence, Attrs: model.IntervalAttrs{Reason: "call"}}
	input := model.Interval{Start: m2, End: m2.Add(time.Minute), LogicalDay: d,
		Kind: model.IntervalPresence, Attrs: model.IntervalAttrs{Reason: "input"}}
	if err := s.WriteIntervals(ctx, []model.Interval{call, input}); err != nil {
		t.Fatal(err)
	}

	got, err := s.DayMinutes(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	want := []MinuteActivity{
		{Label: "com.google.Chrome", Category: model.CategoryBrowser, AttendedS: 70, CallS: 30},
		{Label: "private", Category: model.CategoryPrivate, AttendedS: 50, CallS: 45},
		{Label: UnattributedLabel, Category: model.CategoryUncategorized, AttendedS: 40},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d minutes: %+v", len(got), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Label != w.Label || g.Category != w.Category || g.AttendedS != w.AttendedS || g.CallS != w.CallS || g.VideoS != 0 {
			t.Errorf("minute %d = %+v, want %+v", i, g, w)
		}
	}
	if _, off := got[0].Minute.Zone(); off != 330*60 || !got[0].Minute.Equal(m0) {
		t.Errorf("minute 0 at %v (offset %d), want %v in the recorded zone", got[0].Minute, off, m0)
	}
}
