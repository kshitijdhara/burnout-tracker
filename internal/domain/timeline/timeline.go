// Package timeline builds the agent and human timelines (system design
// §4.3, ADR-0003): one timeline per agent session, and a single human
// timeline across all sessions, so parallel agents never multiply human
// time. It also produces presence intervals. Pure: no I/O, no clock.
package timeline

import (
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/attention"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// Agent states.
const (
	AgentWorking = "working"
	AgentBlocked = "blocked_on_human"
	AgentIdle    = "idle"
)

// Human states.
const (
	HumanReading      = "reading"
	HumanComposing    = "composing"
	HumanApproving    = "approving"
	HumanElsewhere    = "elsewhere"
	HumanAway         = "away"
	HumanUnattributed = "unattributed"
)

// Params configures the tracker.
type Params struct {
	Rollover time.Duration // logical-day rollover, e.g. 4h
	// A working segment with no events for this long is closed: the turn was
	// probably interrupted and Stop never fired.
	WorkingTimeout time.Duration
	// A blocked segment this old is closed the same way (the user dismissed
	// the prompt).
	BlockedTimeout time.Duration
	// A session with no events for this long is ended.
	StaleSession time.Duration
	// Output counts as fresh (longer presence grace) for this long after Stop.
	FreshOutput time.Duration
	// Minimum presence to count as present on the presence timeline.
	PresentAt float64
}

// DefaultParams returns the documented defaults.
func DefaultParams() Params {
	return Params{
		Rollover:       4 * time.Hour,
		WorkingTimeout: 10 * time.Minute,
		BlockedTimeout: time.Hour,
		StaleSession:   12 * time.Hour,
		FreshOutput:    3 * time.Minute,
		PresentAt:      0.5,
	}
}

// Session describes an open agent session, so the caller can find its
// terminal window from the agent process.
type Session struct {
	Key       string
	Agent     string
	PID       int
	State     string
	LastEvent time.Time
}

// Tracker consumes agent events and attention ticks, and emits intervals.
type Tracker struct {
	p        Params
	sessions map[string]*session
	human    *span
	presence *span
	out      []model.Interval
}

type session struct {
	Session
	project       string
	since         time.Time
	tools         int
	lastStop      time.Time
	sawPermission bool
}

type span struct {
	state, session, reason string
	start, end             time.Time
}

// New returns a tracker.
func New(p Params) *Tracker {
	return &Tracker{p: p, sessions: make(map[string]*session)}
}

// Sessions returns the open sessions.
func (t *Tracker) Sessions() []Session {
	out := make([]Session, 0, len(t.sessions))
	for _, s := range t.sessions {
		out = append(out, s.Session)
	}
	return out
}

// Drain returns and clears the intervals emitted so far.
func (t *Tracker) Drain() []model.Interval {
	out := t.out
	t.out = nil
	return out
}

// OnAgent applies one agent hook event.
func (t *Tracker) OnAgent(e model.AgentEvent) {
	if e.SessionKey == "" {
		return
	}
	s := t.session(e)
	if e.At.After(s.LastEvent) {
		s.LastEvent = e.At
	}
	if e.AgentPID != 0 {
		s.PID = e.AgentPID
	}
	switch e.Kind {
	case model.AgentPromptSubmit:
		t.transition(s, AgentWorking, e.At)
	case model.AgentToolStart:
		t.transition(s, AgentWorking, e.At)
		s.tools++
	case model.AgentToolEnd:
		t.transition(s, AgentWorking, e.At)
	case model.AgentPermissionAsk:
		s.sawPermission = true
		if s.State == AgentWorking {
			t.transition(s, AgentBlocked, e.At)
		}
	case model.AgentNotification:
		// Older agents have no permission event; a notification mid-turn
		// then means the agent is waiting on the user.
		if !s.sawPermission && s.State == AgentWorking {
			t.transition(s, AgentBlocked, e.At)
		}
	case model.AgentStop, model.AgentStopFailure:
		t.transition(s, AgentIdle, e.At)
		s.lastStop = e.At
	case model.AgentSessionEnd:
		t.endSession(s, e.At)
	}
}

func (t *Tracker) session(e model.AgentEvent) *session {
	s, ok := t.sessions[e.SessionKey]
	if !ok {
		s = &session{
			Session: Session{Key: e.SessionKey, Agent: e.Agent, State: AgentIdle, LastEvent: e.At},
			project: e.ProjectKey,
			since:   e.At,
		}
		t.sessions[e.SessionKey] = s
	}
	return s
}

func (t *Tracker) transition(s *session, state string, at time.Time) {
	if s.State == state {
		return
	}
	t.closeAgentSegment(s, at)
	s.State, s.since, s.tools = state, at, 0
}

func (t *Tracker) closeAgentSegment(s *session, at time.Time) {
	if !at.After(s.since) {
		return
	}
	attrs := model.IntervalAttrs{State: s.State, Agent: s.Agent, ProjectKey: s.project}
	if s.State == AgentWorking {
		attrs.Tools = s.tools
	}
	t.emit(model.Interval{Start: s.since, End: at, Kind: model.IntervalAgentState, SessionKey: s.Key, Attrs: attrs})
}

func (t *Tracker) endSession(s *session, at time.Time) {
	t.closeAgentSegment(s, at)
	delete(t.sessions, s.Key)
}

// Advance applies timeouts as of now. Call it on every tick.
func (t *Tracker) Advance(now time.Time) {
	for _, s := range t.sessions {
		idleFor := now.Sub(s.LastEvent)
		switch {
		case idleFor >= t.p.StaleSession:
			t.endSession(s, s.LastEvent)
		case s.State == AgentWorking && idleFor >= t.p.WorkingTimeout,
			s.State == AgentBlocked && idleFor >= t.p.BlockedTimeout:
			// The turn ended without a Stop (interrupted). We don't know
			// when, so close it at the last thing we heard.
			t.transition(s, AgentIdle, maxTime(s.LastEvent, s.since))
		}
	}
}

// FreshOutput reports which windows show agent output that finished within
// the fresh-output window. sessionWindows maps windows to session keys.
func (t *Tracker) FreshOutput(now time.Time, sessionWindows map[model.WindowID]string) map[model.WindowID]bool {
	out := make(map[model.WindowID]bool)
	for w, key := range sessionWindows {
		if s, ok := t.sessions[key]; ok && !s.lastStop.IsZero() && now.Sub(s.lastStop) < t.p.FreshOutput {
			out[w] = true
		}
	}
	return out
}

// OnTick extends the human and presence timelines with one attention
// result. sessionWindows maps terminal windows to the agent session running
// in them; when several sessions share a window the caller picks one.
func (t *Tracker) OnTick(res attention.Result, sessionWindows map[model.WindowID]string) {
	t.tickPresence(res)
	if len(t.sessions) == 0 {
		t.closeSpan(t.human, model.IntervalHumanState)
		t.human = nil
		return
	}
	state, sess := t.humanState(res, sessionWindows)
	t.human = t.extend(t.human, model.IntervalHumanState, res, span{state: state, session: sess})
}

func (t *Tracker) humanState(res attention.Result, sessionWindows map[model.WindowID]string) (state, sessionKey string) {
	if res.Presence == 0 {
		return HumanAway, ""
	}
	top, _ := res.Top()
	if top == 0 {
		return HumanUnattributed, ""
	}
	key, ok := sessionWindows[top]
	s := t.sessions[key]
	if !ok || s == nil {
		return HumanElsewhere, ""
	}
	switch {
	case s.State == AgentBlocked:
		return HumanApproving, key
	case res.KeyboardActive && res.Focused == top:
		return HumanComposing, key
	default:
		return HumanReading, key
	}
}

func (t *Tracker) tickPresence(res attention.Result) {
	if res.Presence < t.p.PresentAt {
		t.closeSpan(t.presence, model.IntervalPresence)
		t.presence = nil
		return
	}
	reason := res.Reason
	if reason == attention.ReasonGrace {
		reason = attention.ReasonInput // grace is the tail of input presence
	}
	t.presence = t.extend(t.presence, model.IntervalPresence, res, span{state: "present", reason: reason})
}

// extend grows cur by one tick, or closes it and starts next. A tick with
// Dt > 0 accounts for [At-Dt, At); a tick after a gap starts a new span.
func (t *Tracker) extend(cur *span, kind model.IntervalKind, res attention.Result, next span) *span {
	if res.Dt == 0 {
		t.closeSpan(cur, kind)
		next.start, next.end = res.At, res.At
		return &next
	}
	if cur != nil && cur.state == next.state && cur.session == next.session && cur.reason == next.reason && !res.At.Before(cur.end) {
		cur.end = res.At
		return cur
	}
	start := res.At.Add(-res.Dt)
	if cur != nil {
		cur.end = start
		t.closeSpan(cur, kind)
	}
	next.start, next.end = start, res.At
	return &next
}

func (t *Tracker) closeSpan(sp *span, kind model.IntervalKind) {
	if sp == nil || !sp.end.After(sp.start) {
		return
	}
	iv := model.Interval{Start: sp.start, End: sp.end, Kind: kind, SessionKey: sp.session}
	if kind == model.IntervalPresence {
		iv.Attrs.Reason = sp.reason
	} else {
		iv.Attrs.State = sp.state
	}
	t.emit(iv)
}

// Flush closes every open human and presence span at the current end
// (on shutdown). Agent sessions stay open; their segments close on events.
func (t *Tracker) Flush() {
	t.closeSpan(t.human, model.IntervalHumanState)
	t.closeSpan(t.presence, model.IntervalPresence)
	t.human, t.presence = nil, nil
}

func (t *Tracker) emit(iv model.Interval) {
	t.out = append(t.out, SplitAtRollover(iv, t.p.Rollover)...)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
