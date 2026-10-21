package attention

import "maps"

// Held is a tick result with the caller's data for that tick, released by a
// Resolver once the tick's presence is final.
type Held[T any] struct {
	Result  Result
	Payload T
}

// Resolver decides idle stretches after the fact. Without a camera, a
// person reading or thinking looks exactly like a person who has left: no
// input. What tells them apart is how the stretch ends. If the user's own
// input ends it within the stretch's cap, with no lock, sleep or gap in
// between, they were there: every tick is released as present
// (ReasonReading) with the attention it would have had. If a lock, sleep or
// gap ends it, or it outlasts the cap, the grace rule's presence stands.
//
// Ticks inside a stretch are held until it ends, so results come out late
// by up to the cap, but always in order. Memory is bounded by the cap.
type Resolver[T any] struct {
	pending []Held[T]
	// spent is set once the current stretch outlasted its cap; its later
	// ticks pass straight through with the grace rule's presence.
	spent bool
}

// Push takes the next tick and returns the ticks whose presence is now
// final, oldest first.
func (r *Resolver[T]) Push(res Result, payload T) []Held[T] {
	tick := Held[T]{Result: res, Payload: payload}
	if !res.Idle || res.Dt == 0 {
		// The stretch, if any, ended here. Only the user's own input after
		// a contiguous stretch confirms it; a lock, a call or video
		// starting on its own, or a gap does not.
		confirmed := !res.Idle && res.Dt > 0 && res.Reason == ReasonInput
		out := r.release(confirmed)
		r.spent = false
		if !res.Idle {
			return append(out, tick)
		}
		return append(out, r.hold(tick)...)
	}
	return r.hold(tick)
}

// Flush releases every held tick with the grace rule's presence, for
// shutdown: an unfinished stretch was never confirmed.
func (r *Resolver[T]) Flush() []Held[T] {
	r.spent = false
	return r.release(false)
}

// Pending reports how many ticks are held.
func (r *Resolver[T]) Pending() int { return len(r.pending) }

func (r *Resolver[T]) hold(tick Held[T]) []Held[T] {
	if r.spent {
		return []Held[T]{tick}
	}
	r.pending = append(r.pending, tick)
	first := r.pending[0].Result
	if tick.Result.At.Sub(first.At)+first.Dt > tick.Result.Cap {
		r.spent = true
		return r.release(false)
	}
	return nil
}

func (r *Resolver[T]) release(confirmed bool) []Held[T] {
	out := r.pending
	r.pending = nil
	if confirmed {
		for i := range out {
			out[i].Result = present(out[i].Result)
		}
	}
	return out
}

// present turns an idle tick into a fully present one, with the attention
// the engine computed for that case.
func present(res Result) Result {
	res.Presence, res.Reason = 1, ReasonReading
	res.Attention = maps.Clone(res.Shares)
	res.Unattributed = res.UnattributedShare
	return res
}
