package attention

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

var t0 = time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)

func win(id model.WindowID, focused bool, visible float64) model.Window {
	return model.Window{ID: id, AppID: "app", Bounds: model.Rect{W: 1000, H: 800}, Visible: visible, Focused: focused}
}

// run feeds n one-second ticks built by mk and returns the last result.
func run(e *Engine, n int, mk func(i int, at time.Time) Input) Result {
	var res Result
	for i := range n {
		at := t0.Add(time.Duration(i) * time.Second)
		res = e.Tick(mk(i, at))
	}
	return res
}

func TestTypingInFocusedWindowWins(t *testing.T) {
	e := New(DefaultParams())
	res := run(e, 10, func(_ int, at time.Time) Input {
		return Input{Snapshot: model.Snapshot{
			At: at, Windows: []model.Window{win(1, true, 1), win(2, false, 1)},
			PointerWindow: 2, IdleFor: 0, Keys: 5, InputCountsOn: true,
		}}
	})
	if res.Presence != 1 || res.Reason != ReasonInput {
		t.Fatalf("presence = %v %q", res.Presence, res.Reason)
	}
	if res.Attention[1] < 0.9 {
		t.Fatalf("focused typing window got %v", res.Attention)
	}
	if !res.KeyboardActive {
		t.Fatal("expected keyboard activity")
	}
}

// The core multi-monitor case: focus stays on the editor, but the user
// scrolls docs on the second monitor.
func TestScrollingUnfocusedWindowOnSecondMonitorWins(t *testing.T) {
	e := New(DefaultParams())
	res := run(e, 10, func(_ int, at time.Time) Input {
		docs := win(2, false, 1)
		docs.Monitor = 1
		return Input{Snapshot: model.Snapshot{
			At: at, Windows: []model.Window{win(1, true, 1), docs},
			PointerWindow: 2, Scrolls: 3, InputCountsOn: true,
		}}
	})
	if res.Attention[2] < 0.9 {
		t.Fatalf("scrolled docs window got %v", res.Attention)
	}
}

func TestBackgroundAudioDoesNotHoldPresence(t *testing.T) {
	e := New(DefaultParams())
	music := win(2, false, 0) // audio app, not visible
	music.HasVideo = false
	res := e.Tick(Input{Snapshot: model.Snapshot{At: t0, Windows: []model.Window{win(1, true, 1), music}, IdleFor: 30 * time.Minute}})
	if res.Presence != 0 {
		t.Fatalf("presence = %v with only background audio", res.Presence)
	}
}

func TestVisibleVideoAndCallsHoldPresence(t *testing.T) {
	video := win(2, false, 0.8)
	video.HasVideo = true
	call := win(3, false, 0.1)
	call.InCall = true
	hidden := win(4, false, 0.05)
	hidden.HasVideo = true
	tests := []struct {
		name   string
		ws     []model.Window
		want   float64
		reason string
	}{
		{"visible video", []model.Window{win(1, true, 1), video}, 1, ReasonVideo},
		{"call", []model.Window{win(1, true, 1), call}, 1, ReasonCall},
		{"mostly hidden video", []model.Window{win(1, true, 1), hidden}, 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := New(DefaultParams()).Tick(Input{Snapshot: model.Snapshot{At: t0, Windows: tc.ws, IdleFor: time.Hour}})
			if res.Presence != tc.want || res.Reason != tc.reason {
				t.Fatalf("got %v %q", res.Presence, res.Reason)
			}
		})
	}
}

func TestGraceDependsOnLastAttendedWindow(t *testing.T) {
	p := DefaultParams()
	idle := p.ActiveInput + 90*time.Second // past default grace, within docs and agent grace
	tests := []struct {
		name  string
		cat   model.Category
		fresh bool
		want  bool // still present?
	}{
		{"default app", model.CategoryOther, false, false},
		{"docs", model.CategoryDocs, false, true},
		{"terminal with fresh agent output", model.CategoryTerminal, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := New(p)
			in := func(at time.Time, idleFor time.Duration) Input {
				return Input{
					Snapshot:    model.Snapshot{At: at, Windows: []model.Window{win(1, true, 1)}, IdleFor: idleFor, Keys: 1, InputCountsOn: true},
					Categories:  map[model.WindowID]model.Category{1: tc.cat},
					FreshOutput: map[model.WindowID]bool{1: tc.fresh},
				}
			}
			e.Tick(in(t0, 0))
			res := e.Tick(in(t0.Add(time.Second), idle))
			if got := res.Presence > 0; got != tc.want {
				t.Fatalf("present = %v (presence %v)", got, res.Presence)
			}
		})
	}
}

func TestAssistiveProfilesStretchGrace(t *testing.T) {
	p := DefaultParams()
	p.Profile = ProfileScreenReader
	e := New(p)
	in := func(at time.Time, idle time.Duration) Input {
		return Input{Snapshot: model.Snapshot{At: at, Windows: []model.Window{win(1, true, 1)}, IdleFor: idle, Keys: 1, InputCountsOn: true}}
	}
	e.Tick(in(t0, 0))
	if res := e.Tick(in(t0.Add(time.Second), p.ActiveInput+2*time.Minute)); res.Presence == 0 {
		t.Fatal("screen-reader profile should still be present after 2 min without input")
	}
}

func TestLockedIsAbsent(t *testing.T) {
	res := New(DefaultParams()).Tick(Input{Snapshot: model.Snapshot{At: t0, Windows: []model.Window{win(1, true, 1)}, Locked: true}})
	if res.Presence != 0 || len(res.Attention) != 0 {
		t.Fatalf("locked: %+v", res)
	}
}

func TestNoClearSignalGoesUnattributed(t *testing.T) {
	p := DefaultParams()
	e := New(p)
	// Present (within grace) but nothing on screen has been touched for a long time.
	ws := []model.Window{win(1, false, 0.1), win(2, false, 0.1)}
	res := e.Tick(Input{Snapshot: model.Snapshot{At: t0, Windows: ws, IdleFor: p.ActiveInput}})
	if res.Unattributed != res.Presence || res.Presence == 0 {
		t.Fatalf("expected all presence unattributed, got %+v", res)
	}
}

func TestGapsAreNotAttributed(t *testing.T) {
	e := New(DefaultParams())
	in := func(at time.Time) Input {
		return Input{Snapshot: model.Snapshot{At: at, Windows: []model.Window{win(1, true, 1)}, Keys: 1, InputCountsOn: true}}
	}
	if r := e.Tick(in(t0)); r.Dt != 0 {
		t.Fatalf("first tick Dt = %v", r.Dt)
	}
	if r := e.Tick(in(t0.Add(time.Second))); r.Dt != time.Second {
		t.Fatalf("Dt = %v", r.Dt)
	}
	if r := e.Tick(in(t0.Add(time.Hour))); r.Dt != 0 {
		t.Fatalf("gap Dt = %v", r.Dt)
	}
	if r := e.Tick(in(t0)); r.Dt != 0 {
		t.Fatalf("clock went backwards, Dt = %v", r.Dt)
	}
}

func TestFallbackWithoutInputCounts(t *testing.T) {
	e := New(DefaultParams())
	res := run(e, 5, func(_ int, at time.Time) Input {
		return Input{Snapshot: model.Snapshot{At: at, Windows: []model.Window{win(1, true, 1), win(2, false, 1)}, PointerWindow: 2, IdleFor: 0}}
	})
	if res.Attention[1] < 0.5 || !res.KeyboardActive {
		t.Fatalf("idle-delta fallback should attribute still-pointer input to the focused window: %+v", res)
	}
}

// Properties from ADR-0002: attention ≥ 0, Σ attention + unattributed = presence ≤ 1.
func TestAttentionInvariants(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 9))
	e := New(DefaultParams())
	at := t0
	for range 5000 {
		at = at.Add(time.Duration(rng.IntN(3000)) * time.Millisecond)
		n := rng.IntN(12)
		ws := make([]model.Window, n)
		for i := range ws {
			ws[i] = model.Window{
				ID: model.WindowID(1 + rng.IntN(20)), Bounds: model.Rect{W: 1 + rng.IntN(2000), H: 1 + rng.IntN(1200)},
				Visible: rng.Float64(), Focused: i == 0 && rng.IntN(2) == 0,
				HasVideo: rng.IntN(20) == 0, InCall: rng.IntN(50) == 0,
			}
		}
		var ptr model.WindowID
		if n > 0 {
			ptr = ws[rng.IntN(n)].ID
		}
		res := e.Tick(Input{Snapshot: model.Snapshot{
			At: at, Windows: ws, PointerWindow: ptr, PointerMoved: rng.IntN(2) == 0,
			IdleFor: time.Duration(rng.IntN(400)) * time.Second, Keys: rng.IntN(5), Scrolls: rng.IntN(5),
			InputCountsOn: rng.IntN(2) == 0, Locked: rng.IntN(30) == 0,
		}})
		if res.Presence < 0 || res.Presence > 1 {
			t.Fatalf("presence out of range: %v", res.Presence)
		}
		sum := res.Unattributed
		for _, a := range res.Attention {
			if a < 0 || math.IsNaN(a) {
				t.Fatalf("bad attention %v", a)
			}
			sum += a
		}
		if math.Abs(sum-res.Presence) > 1e-9 {
			t.Fatalf("Σ attention %v != presence %v", sum, res.Presence)
		}
	}
}

func TestRollupsSumTicks(t *testing.T) {
	e := New(DefaultParams())
	r := NewRollups(4 * time.Hour)
	ident := func(w model.Window) Identity {
		return Identity{Key: string(rune('a' + int(w.ID))), AppID: "app", Category: model.CategoryCode}
	}
	wantAttended := 0.0
	for i := range 150 { // 2.5 minutes of one-second ticks
		s := model.Snapshot{At: t0.Add(time.Duration(i) * time.Second), Windows: []model.Window{win(1, true, 1), win(2, false, 0.5)}, Keys: 2, InputCountsOn: true}
		res := e.Tick(Input{Snapshot: s})
		r.Add(res, s, ident)
		for _, a := range res.Attention {
			wantAttended += a * res.Dt.Seconds()
		}
		wantAttended += res.Unattributed * res.Dt.Seconds()
	}
	rows := r.Flush(t0.Add(2 * time.Minute)) // only the first two complete minutes
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4 (2 minutes × 2 windows)", len(rows))
	}
	rows = append(rows, r.Flush(t0.Add(time.Hour))...)
	got := 0.0
	for _, m := range rows {
		got += m.AttendedS
		if m.LogicalDay != "2026-10-21" || m.Category != model.CategoryCode {
			t.Fatalf("bad row %+v", m)
		}
	}
	if math.Abs(got-wantAttended) > 1e-6 {
		t.Fatalf("rollups %v != ticks %v", got, wantAttended)
	}
}

func BenchmarkTick20Windows(b *testing.B) {
	e := New(DefaultParams())
	ws := make([]model.Window, 20)
	for i := range ws {
		ws[i] = win(model.WindowID(i+1), i == 0, 0.5)
	}
	at := t0
	b.ReportAllocs()
	for b.Loop() {
		at = at.Add(time.Second)
		e.Tick(Input{Snapshot: model.Snapshot{At: at, Windows: ws, PointerWindow: 3, Keys: 1, InputCountsOn: true}})
	}
}

func TestRollupsSkipAbsence(t *testing.T) {
	r := NewRollups(4 * time.Hour)
	s := model.Snapshot{At: t0, Windows: []model.Window{win(1, true, 1)}, Locked: true}
	r.Add(Result{At: t0, Dt: time.Second, Presence: 0}, s, func(model.Window) Identity { return Identity{Key: "k"} })
	if rows := r.Flush(t0.Add(time.Hour)); len(rows) != 0 {
		t.Fatalf("rows written while absent: %+v", rows)
	}
}

// A sleeping display means away, unless a call holds presence: listening
// to a call needs no screen. A lock always means away.
func TestDisplayAsleep(t *testing.T) {
	for _, tc := range []struct {
		name   string
		snap   model.Snapshot
		want   float64
		reason string
	}{
		{"asleep", model.Snapshot{DisplayAsleep: true}, 0, ""},
		{"asleep in a call", model.Snapshot{DisplayAsleep: true, CallActive: true}, 1, ReasonCall},
		{"asleep with video", model.Snapshot{DisplayAsleep: true, Windows: []model.Window{{ID: 1, Visible: 1, HasVideo: true}}}, 0, ""},
		{"locked in a call", model.Snapshot{Locked: true, CallActive: true}, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.snap.At, tc.snap.IdleFor = t0, time.Hour
			res := New(DefaultParams()).Tick(Input{Snapshot: tc.snap})
			if res.Presence != tc.want || res.Reason != tc.reason || res.Idle {
				t.Fatalf("got presence %v %q idle=%v", res.Presence, res.Reason, res.Idle)
			}
		})
	}
}
