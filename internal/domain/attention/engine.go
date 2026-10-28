package attention

import (
	"math"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/model"
)

// Presence reasons, recorded on presence intervals.
const (
	ReasonInput = "input"
	ReasonGrace = "grace"
	ReasonVideo = "video"
	ReasonCall  = "call"
	// ReasonConfirmedIdle marks idle time confirmed as present after the
	// fact, typically reading or thinking: the user's own input ended the
	// idle stretch within its cap, with no lock or sleep in between (see
	// Resolver).
	ReasonConfirmedIdle = "confirmed_idle"
)

// Input is one tick's snapshot plus what the caller knows about its windows.
type Input struct {
	Snapshot model.Snapshot
	// Category per window, from classification. Missing means uncategorized.
	Categories map[model.WindowID]model.Category
	// Windows showing agent output that finished recently (a terminal whose
	// agent session just stopped). These get the longest grace window.
	FreshOutput map[model.WindowID]bool
}

// Result is the attention model's output for one tick.
type Result struct {
	At time.Time
	// Dt is the wall time this tick accounts for. It is zero for the first
	// tick and after a gap, so gaps are never attributed.
	Dt time.Duration
	// Presence in [0, 1] and why.
	Presence float64
	Reason   string
	// Attention per window; values sum with Unattributed to Presence.
	Attention    map[model.WindowID]float64
	Unattributed float64
	// Focused window, 0 if none.
	Focused model.WindowID
	// KeyboardActive is true when keyboard input went to the focused window
	// within the input window.
	KeyboardActive bool

	// UserInput is true when the user gave input within ActiveInput on an
	// unlocked, awake screen, whatever else holds presence. Only this
	// confirms an idle stretch.
	UserInput bool
	// Idle is true inside an idle stretch: no recent input, call or visible
	// video, screen unlocked and awake. Presence then holds the grace
	// rule's value, which a Resolver may later replace.
	Idle bool
	// Confirmable is true on idle ticks the user's return could still
	// confirm: by the OS idle time, the stretch is within its cap.
	Confirmable bool
	// Shares is the attention distribution if the user is present, summing
	// with UnattributedShare to 1. Set on confirmable ticks only.
	Shares            map[model.WindowID]float64
	UnattributedShare float64
}

// Top returns the window holding the most attention and its share. It
// returns (0, Unattributed) when the unattributed bucket dominates.
func (r Result) Top() (model.WindowID, float64) {
	var best model.WindowID
	share := r.Unattributed
	for id, a := range r.Attention {
		if a > share || (a == share && best != 0 && id < best) {
			best, share = id, a
		}
	}
	return best, share
}

// Engine holds the little state the model needs between ticks.
type Engine struct {
	p Params

	lastAt       time.Time
	lastTop      model.WindowID
	keys, scroll window // recent input counts
	hoverWindow  model.WindowID
	hoverSince   time.Time
	lastTouched  map[model.WindowID]time.Time

	// The current idle stretch, if any, and the window the user was
	// attending when it began. Its grace is fixed then; its cap can grow
	// if that window gets fresh agent output during the stretch.
	inStretch                bool
	stretchWindow            model.WindowID
	stretchGrace, stretchCap time.Duration
}

// New returns an engine with the given parameters.
func New(p Params) *Engine {
	return &Engine{
		p:           p,
		keys:        window{span: p.InputWindow},
		scroll:      window{span: p.InputWindow},
		lastTouched: make(map[model.WindowID]time.Time),
	}
}

// Tick consumes one snapshot and returns the attention distribution.
func (e *Engine) Tick(in Input) Result {
	s := in.Snapshot
	res := Result{At: s.At, Attention: make(map[model.WindowID]float64)}

	gap := e.lastAt.IsZero() || s.At.Before(e.lastAt) || s.At.Sub(e.lastAt) > e.p.MaxGap
	if !gap {
		res.Dt = s.At.Sub(e.lastAt)
	} else {
		e.reset()
	}
	e.lastAt = s.At

	keys, scroll := e.recordInput(s)
	focused := focusedWindow(s.Windows)
	res.Focused = focused
	res.KeyboardActive = focused != 0 && keys > 0
	e.trackHover(s)
	e.trackTouches(s, focused, keys, scroll)
	e.forget(s.Windows)

	res.Presence, res.Reason = e.presence(in)
	res.UserInput = !s.Locked && !s.DisplayAsleep && s.IdleFor <= e.p.ActiveInput
	res.Idle = e.inStretch
	res.Confirmable = res.Idle && s.IdleFor-e.p.ActiveInput <= e.stretchCap

	if res.Presence > 0 || res.Confirmable {
		shares, unattributed := e.distribution(in, focused, keys, scroll)
		if res.Confirmable {
			res.Shares, res.UnattributedShare = shares, unattributed
		}
		if res.Presence > 0 {
			res.Unattributed = unattributed * res.Presence
			for id, share := range shares {
				res.Attention[id] = share * res.Presence
			}
		}
	}
	e.lastTop = 0
	if res.Presence > 0 && res.Unattributed == 0 {
		e.lastTop, _ = res.Top()
	}
	return res
}

// distribution returns attention shares as if the user were present,
// summing with the unattributed share to 1.
func (e *Engine) distribution(in Input, focused model.WindowID, keys, scroll float64) (map[model.WindowID]float64, float64) {
	scores, best := e.scores(in, focused, keys, scroll)
	if best < e.p.UnattributedBelow {
		return map[model.WindowID]float64{}, 1
	}
	return softmax(scores, e.p.Temperature), 0
}

func (e *Engine) reset() {
	e.inStretch = false
	e.keys.clear()
	e.scroll.clear()
	e.hoverWindow = 0
	e.lastTop = 0
}

// recordInput returns key and scroll activity over the input window. When
// real counts are off it falls back to the idle timer: recent input with a
// still pointer is treated as typing, with a moving pointer as pointing.
func (e *Engine) recordInput(s model.Snapshot) (keys, scroll float64) {
	k, sc := s.Keys, s.Scrolls
	if !s.InputCountsOn {
		k, sc = 0, 0
		if s.IdleFor < time.Second {
			if s.PointerMoved {
				sc = 1
			} else {
				k = 1
			}
		}
	}
	e.keys.add(s.At, float64(k))
	e.scroll.add(s.At, float64(sc))
	return e.keys.sum(s.At), e.scroll.sum(s.At)
}

func (e *Engine) trackHover(s model.Snapshot) {
	if s.PointerWindow != e.hoverWindow {
		e.hoverWindow, e.hoverSince = s.PointerWindow, s.At
	}
}

func (e *Engine) trackTouches(s model.Snapshot, focused model.WindowID, keys, scroll float64) {
	if keys > 0 && focused != 0 {
		e.lastTouched[focused] = s.At
	}
	if (scroll > 0 || s.PointerMoved) && s.PointerWindow != 0 {
		e.lastTouched[s.PointerWindow] = s.At
	}
}

// forget drops state for windows that no longer exist, bounding memory.
func (e *Engine) forget(ws []model.Window) {
	if len(e.lastTouched) <= 2*len(ws)+16 {
		return
	}
	live := make(map[model.WindowID]bool, len(ws))
	for _, w := range ws {
		live[w.ID] = true
	}
	for id := range e.lastTouched {
		if !live[id] {
			delete(e.lastTouched, id)
		}
	}
}

func (e *Engine) presence(in Input) (float64, string) {
	if p, reason, known := e.activePresence(in.Snapshot); known {
		e.inStretch = false
		return p, reason
	}
	if !e.inStretch {
		e.inStretch, e.stretchWindow = true, e.lastTop
		e.stretchGrace = e.forWindow(in, e.stretchWindow, e.p.GraceDefault, e.p.GraceDocs, e.p.GraceAgentOutput)
		e.stretchCap = 0
	}
	// The agent may finish while the user waits: from then on its output
	// is something to read, and the cap follows.
	e.stretchCap = max(e.stretchCap,
		e.forWindow(in, e.stretchWindow, e.p.ReadingCapDefault, e.p.ReadingCapDocs, e.p.ReadingCapAgentOutput))
	over := in.Snapshot.IdleFor - e.p.ActiveInput
	if over >= e.stretchGrace {
		return 0, ""
	}
	return 1 - float64(over)/float64(e.stretchGrace), ReasonGrace
}

// activePresence reports presence that needs no inference: a locked
// screen, a call, visible video, or recent input. known is false when none
// applies and the user is idle.
func (e *Engine) activePresence(s model.Snapshot) (p float64, reason string, known bool) {
	inCall := s.CallActive
	for _, w := range s.Windows {
		inCall = inCall || w.InCall
	}
	switch {
	case s.Locked:
		return 0, "", true
	case inCall:
		// A call holds presence even when the display sleeps: listening
		// needs no screen.
		return 1, ReasonCall, true
	case s.DisplayAsleep:
		return 0, "", true
	}
	for _, w := range s.Windows {
		// Audio-only media never holds presence; only visible video does.
		if w.HasVideo && w.Visible >= e.p.VideoMinVisible {
			return 1, ReasonVideo, true
		}
	}
	if s.IdleFor <= e.p.ActiveInput {
		return 1, ReasonInput, true
	}
	return 0, "", false
}

// forWindow chooses between per-context durations by what w shows, scaled
// for the input profile: fresh agent output, documents and browsers, or
// anything else.
func (e *Engine) forWindow(in Input, w model.WindowID, def, docs, agentOutput time.Duration) time.Duration {
	d := def
	switch {
	case w != 0 && in.FreshOutput[w]:
		d = agentOutput
	case w != 0:
		switch in.Categories[w] {
		case model.CategoryDocs, model.CategoryBrowser:
			d = docs
		}
	}
	return time.Duration(float64(d) * e.p.graceScale())
}

func (e *Engine) scores(in Input, focused model.WindowID, keys, scroll float64) (map[model.WindowID]float64, float64) {
	s := in.Snapshot
	totalVisible := 0.0
	for _, w := range s.Windows {
		totalVisible += w.Visible * float64(w.Bounds.Area())
	}
	scores := make(map[model.WindowID]float64, len(s.Windows))
	best := 0.0
	for _, w := range s.Windows {
		if w.Visible <= 0 {
			continue
		}
		v := 0.0
		if totalVisible > 0 {
			v += e.p.AVisible * w.Visible * float64(w.Bounds.Area()) / totalVisible
		}
		if w.ID == focused {
			v += e.p.AKeys * sat(keys, e.p.KeysSat)
		}
		if w.ID == s.PointerWindow {
			v += e.p.AScroll * sat(scroll, e.p.ScrollSat)
			v += e.p.AHover * sat(s.At.Sub(e.hoverSince).Seconds(), e.p.HoverSat.Seconds())
		}
		if w.HasVideo {
			v += e.p.AVideo
		}
		if w.InCall {
			v += e.p.ACall
		}
		if t, ok := e.lastTouched[w.ID]; ok {
			v += e.p.ARecent * math.Exp(-s.At.Sub(t).Seconds()/e.p.RecentTau.Seconds())
		}
		scores[w.ID] = v
		best = max(best, v)
	}
	return scores, best
}

func focusedWindow(ws []model.Window) model.WindowID {
	for _, w := range ws {
		if w.Focused {
			return w.ID
		}
	}
	return 0
}

// sat maps x ≥ 0 into [0, 1): 1 - exp(-x/k).
func sat(x, k float64) float64 {
	if x <= 0 || k <= 0 {
		return 0
	}
	return 1 - math.Exp(-x/k)
}

func softmax(scores map[model.WindowID]float64, temperature float64) map[model.WindowID]float64 {
	out := make(map[model.WindowID]float64, len(scores))
	if len(scores) == 0 {
		return out
	}
	maxS := math.Inf(-1)
	for _, v := range scores {
		maxS = max(maxS, v)
	}
	sum := 0.0
	for id, v := range scores {
		x := math.Exp((v - maxS) / temperature)
		out[id] = x
		sum += x
	}
	for id := range out {
		out[id] /= sum
	}
	return out
}

// window is a fixed-span sliding sum of per-tick counts.
type window struct {
	span time.Duration
	at   []time.Time
	n    []float64
}

func (w *window) add(t time.Time, v float64) {
	w.at = append(w.at, t)
	w.n = append(w.n, v)
}

func (w *window) sum(now time.Time) float64 {
	cut := 0
	for cut < len(w.at) && now.Sub(w.at[cut]) >= w.span {
		cut++
	}
	w.at, w.n = w.at[cut:], w.n[cut:]
	total := 0.0
	for _, v := range w.n {
		total += v
	}
	return total
}

func (w *window) clear() { w.at, w.n = w.at[:0], w.n[:0] }
