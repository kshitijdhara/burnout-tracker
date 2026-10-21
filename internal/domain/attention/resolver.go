package attention

import "github.com/kshitijdhara/burnout-tracker/internal/domain/model"

// Held is a tick result with the caller's data for that tick, released by a
// Resolver once the tick's presence is final.
type Held[T any] struct {
	Result  Result
	Payload T
}

// Resolver decides idle stretches after the fact (ADR-0008). Without a
// camera, a person reading or thinking looks exactly like a person who has
// left: no input. What tells them apart is how the stretch ends. If the
// user's own input ends it while it is still confirmable (within its cap
// by the OS idle time), with no lock, sleep or gap in between, they were
// there: every held tick is released as present (ReasonConfirmedIdle) with
// the attention it would have had. Otherwise the grace rule's presence
// stands.
//
// Ticks are held only while the stretch is confirmable, so results come out
// late by at most the cap, always in order, and memory is bounded by it.
type Resolver[T any] struct {
	pending []Held[T]
}

// Push takes the next tick and returns the ticks whose presence is now
// final, oldest first.
func (r *Resolver[T]) Push(res Result, payload T) []Held[T] {
	tick := Held[T]{Result: res, Payload: payload}
	holdable := res.Idle && res.Confirmable
	if holdable && res.Dt > 0 {
		r.hold(tick)
		return nil
	}
	// Any held stretch ends here. Only the user's own input right after a
	// contiguous stretch confirms it; a lock, a sleeping display, a call or
	// video starting on its own, a gap, or the cap passing does not.
	out := r.release(!res.Idle && res.Dt > 0 && res.UserInput)
	if holdable { // a new stretch starts after a gap
		r.hold(tick)
		return out
	}
	return append(out, tick)
}

// Flush releases every held tick with the grace rule's presence, for
// shutdown: an unfinished stretch was never confirmed.
func (r *Resolver[T]) Flush() []Held[T] {
	return r.release(false)
}

// Pending reports how many ticks are held.
func (r *Resolver[T]) Pending() int { return len(r.pending) }

// hold keeps a tick until its stretch ends. Its attention is rebuilt from
// Shares on release, so only one distribution per tick is held.
func (r *Resolver[T]) hold(tick Held[T]) {
	tick.Result.Attention = nil
	r.pending = append(r.pending, tick)
}

func (r *Resolver[T]) release(confirmed bool) []Held[T] {
	out := r.pending
	r.pending = nil
	for i := range out {
		res := &out[i].Result
		if confirmed {
			res.Presence, res.Reason = 1, ReasonConfirmedIdle
		}
		res.Attention, res.Unattributed = scaled(res.Shares, res.Presence), res.UnattributedShare*res.Presence
		res.Shares, res.UnattributedShare = nil, 0
	}
	return out
}

// scaled returns shares scaled by presence; empty when presence is 0, as
// the engine reports it.
func scaled(shares map[model.WindowID]float64, presence float64) map[model.WindowID]float64 {
	out := make(map[model.WindowID]float64, len(shares))
	if presence == 0 {
		return out
	}
	for id, s := range shares {
		out[id] = s * presence
	}
	return out
}
