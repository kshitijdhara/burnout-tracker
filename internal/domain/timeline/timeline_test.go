package timeline

import (
	"math/rand/v2"
	"sort"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/attention"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

var t0 = time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }

func ev(sec int, sess string, k model.AgentEventKind) model.AgentEvent {
	return model.AgentEvent{Agent: "claude", Kind: k, SessionKey: sess, At: at(sec)}
}

func states(ivs []model.Interval, kind model.IntervalKind) []string {
	var out []string
	for _, iv := range ivs {
		if iv.Kind == kind {
			out = append(out, iv.Attrs.State)
		}
	}
	return out
}

func TestAgentTurnWithPermission(t *testing.T) {
	tr := New(DefaultParams())
	for _, e := range []model.AgentEvent{
		ev(0, "s1", model.AgentSessionStart),
		ev(10, "s1", model.AgentPromptSubmit),
		ev(12, "s1", model.AgentToolStart),
		ev(15, "s1", model.AgentToolEnd),
		ev(20, "s1", model.AgentPermissionAsk),
		ev(50, "s1", model.AgentToolStart), // approved after 30 s
		ev(55, "s1", model.AgentToolEnd),
		ev(60, "s1", model.AgentStop),
		ev(100, "s1", model.AgentSessionEnd),
	} {
		tr.OnAgent(e)
	}
	ivs := tr.Drain()
	got := states(ivs, model.IntervalAgentState)
	want := []string{AgentIdle, AgentWorking, AgentBlocked, AgentWorking, AgentIdle}
	if len(got) != len(want) {
		t.Fatalf("states %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("states %v, want %v", got, want)
		}
	}
	blocked := ivs[2]
	if blocked.End.Sub(blocked.Start) != 30*time.Second {
		t.Fatalf("blocked for %v, want 30s (tool runtime must not count)", blocked.End.Sub(blocked.Start))
	}
	if ivs[1].Attrs.Tools != 1 {
		t.Fatalf("tools in first working segment = %d", ivs[1].Attrs.Tools)
	}
	if len(tr.Sessions()) != 0 {
		t.Fatal("session should be closed")
	}
}

func TestNotificationFallbackOnlyWithoutPermissionEvents(t *testing.T) {
	tr := New(DefaultParams())
	tr.OnAgent(ev(0, "old", model.AgentPromptSubmit))
	tr.OnAgent(ev(5, "old", model.AgentNotification))
	if tr.sessions["old"].State != AgentBlocked {
		t.Fatal("notification mid-turn should block on agents without permission events")
	}

	tr.OnAgent(ev(0, "new", model.AgentPromptSubmit))
	tr.OnAgent(ev(1, "new", model.AgentPermissionAsk))
	tr.OnAgent(ev(2, "new", model.AgentToolStart))
	tr.OnAgent(ev(3, "new", model.AgentNotification))
	if tr.sessions["new"].State != AgentWorking {
		t.Fatal("notification must not block when the agent reports permission requests")
	}
}

func TestInterruptedTurnTimesOut(t *testing.T) {
	p := DefaultParams()
	tr := New(p)
	tr.OnAgent(ev(0, "s", model.AgentPromptSubmit))
	tr.OnAgent(ev(30, "s", model.AgentToolEnd)) // then the user hits Esc: no Stop
	tr.Advance(at(30).Add(p.WorkingTimeout))
	if got := tr.sessions["s"].State; got != AgentIdle {
		t.Fatalf("state after timeout = %s", got)
	}
	ivs := tr.Drain()
	last := ivs[len(ivs)-1]
	if last.Attrs.State != AgentWorking || !last.End.Equal(at(30)) {
		t.Fatalf("working segment should end at the last event, got %+v", last)
	}
	tr.Advance(at(30).Add(p.StaleSession))
	if len(tr.Sessions()) != 0 {
		t.Fatal("stale session should end")
	}
}

func result(sec int, presence float64, attn map[model.WindowID]float64, focused model.WindowID, typing bool) attention.Result {
	return attention.Result{At: at(sec), Dt: time.Second, Presence: presence, Reason: attention.ReasonInput,
		Attention: attn, Focused: focused, KeyboardActive: typing}
}

func TestHumanStates(t *testing.T) {
	const term, browser = model.WindowID(1), model.WindowID(2)
	windows := map[model.WindowID]string{term: "s1"}
	tests := []struct {
		name    string
		blocked bool
		res     attention.Result
		want    string
	}{
		{"reading agent output", false, result(1, 1, map[model.WindowID]float64{term: 0.9, browser: 0.1}, browser, false), HumanReading},
		{"typing the next prompt", false, result(1, 1, map[model.WindowID]float64{term: 0.95}, term, true), HumanComposing},
		{"approving a permission", true, result(1, 1, map[model.WindowID]float64{term: 0.95}, term, false), HumanApproving},
		{"in the browser", false, result(1, 1, map[model.WindowID]float64{browser: 0.95}, browser, true), HumanElsewhere},
		{"away", false, result(1, 0, nil, 0, false), HumanAway},
		{"unattributed", false, attention.Result{At: at(1), Dt: time.Second, Presence: 0.7, Unattributed: 0.7}, HumanUnattributed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := New(DefaultParams())
			tr.OnAgent(ev(0, "s1", model.AgentPromptSubmit))
			if tc.blocked {
				tr.OnAgent(ev(0, "s1", model.AgentPermissionAsk))
			}
			if got, _ := tr.humanState(tc.res, windows); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestNoHumanTimelineWithoutSessions(t *testing.T) {
	tr := New(DefaultParams())
	for i := 1; i < 10; i++ {
		tr.OnTick(result(i, 1, map[model.WindowID]float64{1: 1}, 1, true), nil)
	}
	tr.Flush()
	if got := states(tr.Drain(), model.IntervalHumanState); len(got) != 0 {
		t.Fatalf("human intervals without agent sessions: %v", got)
	}
}

// The invariant from ADR-0003: however many agent sessions run in parallel,
// the human timeline never overlaps itself, so Σ human time ≤ wall time.
func TestOneHumanInvariant(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 12))
	tr := New(DefaultParams())
	sessions := []string{"a", "b", "c", "d"}
	kinds := []model.AgentEventKind{model.AgentPromptSubmit, model.AgentToolStart, model.AgentToolEnd,
		model.AgentPermissionAsk, model.AgentStop, model.AgentNotification}
	windows := map[model.WindowID]string{1: "a", 2: "b", 3: "c", 4: "d"}
	sec := 0
	var all []model.Interval
	for range 20000 {
		sec += 1 + rng.IntN(2)
		if rng.IntN(3) == 0 {
			tr.OnAgent(ev(sec, sessions[rng.IntN(len(sessions))], kinds[rng.IntN(len(kinds))]))
		}
		attn := map[model.WindowID]float64{}
		for w := range 6 {
			if rng.IntN(2) == 0 {
				attn[model.WindowID(w)] = rng.Float64() / 6
			}
		}
		res := result(sec, rng.Float64(), attn, model.WindowID(rng.IntN(6)), rng.IntN(2) == 0)
		if rng.IntN(500) == 0 {
			res.Dt = 0 // gap
		}
		tr.OnTick(res, windows)
		tr.Advance(at(sec))
		all = append(all, tr.Drain()...)
	}
	tr.Flush()
	all = append(all, tr.Drain()...)

	human := filter(all, model.IntervalHumanState)
	assertNonOverlapping(t, human)
	total := time.Duration(0)
	for _, iv := range human {
		total += iv.End.Sub(iv.Start)
	}
	if wall := at(sec).Sub(t0); total > wall {
		t.Fatalf("human time %v exceeds wall time %v", total, wall)
	}
	for _, s := range sessions {
		var own []model.Interval
		for _, iv := range filter(all, model.IntervalAgentState) {
			if iv.SessionKey == s {
				own = append(own, iv)
			}
		}
		assertNonOverlapping(t, own)
	}
}

func filter(ivs []model.Interval, kind model.IntervalKind) []model.Interval {
	var out []model.Interval
	for _, iv := range ivs {
		if iv.Kind == kind {
			out = append(out, iv)
		}
	}
	return out
}

func assertNonOverlapping(t *testing.T, ivs []model.Interval) {
	t.Helper()
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].Start.Before(ivs[j].Start) })
	for i, iv := range ivs {
		if !iv.End.After(iv.Start) {
			t.Fatalf("empty or inverted interval %+v", iv)
		}
		if i > 0 && iv.Start.Before(ivs[i-1].End) {
			t.Fatalf("overlap: %v–%v and %v–%v", ivs[i-1].Start, ivs[i-1].End, iv.Start, iv.End)
		}
	}
}

func TestPresenceIntervals(t *testing.T) {
	tr := New(DefaultParams())
	for i := 1; i <= 5; i++ {
		tr.OnTick(result(i, 1, map[model.WindowID]float64{1: 1}, 1, true), nil)
	}
	tr.OnTick(result(6, 0.2, nil, 0, false), nil) // below PresentAt; ticks 1–5 cover [0 s, 5 s)
	ivs := filter(tr.Drain(), model.IntervalPresence)
	if len(ivs) != 1 || ivs[0].Attrs.Reason != attention.ReasonInput || ivs[0].End.Sub(ivs[0].Start) != 5*time.Second {
		t.Fatalf("presence intervals: %+v", ivs)
	}
}

func TestSplitAtRollover(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	tests := []struct {
		name       string
		start, end time.Time
		wantDays   []string
	}{
		{"within a day", time.Date(2026, 10, 21, 9, 0, 0, 0, loc), time.Date(2026, 10, 21, 17, 0, 0, 0, loc), []string{"2026-10-21"}},
		{"across 04:00", time.Date(2026, 10, 21, 23, 0, 0, 0, loc), time.Date(2026, 10, 22, 5, 0, 0, 0, loc), []string{"2026-10-21", "2026-10-22"}},
		// DST ends in Berlin on 2026-10-25 at 03:00 → 02:00.
		{"across DST change", time.Date(2026, 10, 24, 22, 0, 0, 0, loc), time.Date(2026, 10, 25, 6, 0, 0, 0, loc), []string{"2026-10-24", "2026-10-25"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parts := SplitAtRollover(model.Interval{Start: tc.start, End: tc.end, Kind: model.IntervalHumanState}, 4*time.Hour)
			if len(parts) != len(tc.wantDays) {
				t.Fatalf("got %d parts: %+v", len(parts), parts)
			}
			total := time.Duration(0)
			for i, p := range parts {
				if p.LogicalDay != tc.wantDays[i] {
					t.Fatalf("part %d day %s, want %s", i, p.LogicalDay, tc.wantDays[i])
				}
				total += p.End.Sub(p.Start)
			}
			if total != tc.end.Sub(tc.start) {
				t.Fatalf("parts cover %v, want %v", total, tc.end.Sub(tc.start))
			}
		})
	}
}

func TestShellInterval(t *testing.T) {
	ivs := ShellInterval(model.ShellCommand{Argv0: "git", Subcommand: "push", ExitCode: 1, Start: at(0), End: at(3), ProjectKey: "p"}, "vcs", 4*time.Hour)
	if len(ivs) != 1 || *ivs[0].Attrs.ExitCode != 1 || ivs[0].Category != "vcs" || ivs[0].Attrs.Argv0 != "git" {
		t.Fatalf("got %+v", ivs)
	}
	// Zero-duration commands still produce a (1 ms) interval.
	if ivs := ShellInterval(model.ShellCommand{Argv0: "cd", Start: at(0), End: at(0)}, "other", 4*time.Hour); len(ivs) != 1 {
		t.Fatal("instant command dropped")
	}
}
