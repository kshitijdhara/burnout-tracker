package attention

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// sim drives an Engine and a Resolver second by second, like the daemon.
type sim struct {
	e         *Engine
	r         Resolver[int]
	at        time.Time
	lastInput time.Time
	tick      int
	cat       model.Category
	out       []Held[int]
}

func newSim(cat model.Category) *sim {
	return &sim{e: New(DefaultParams()), at: t0, lastInput: t0, cat: cat}
}

// step advances one second. input marks user input this second; mod can
// change the snapshot (lock it, start a call).
func (s *sim) step(input bool, mod func(*model.Snapshot)) {
	s.at = s.at.Add(time.Second)
	if input {
		s.lastInput = s.at
	}
	snap := model.Snapshot{At: s.at, Windows: []model.Window{win(1, true, 1)}, IdleFor: s.at.Sub(s.lastInput)}
	if mod != nil {
		mod(&snap)
	}
	res := s.e.Tick(Input{Snapshot: snap, Categories: map[model.WindowID]model.Category{1: s.cat}})
	s.tick++
	s.out = append(s.out, s.r.Push(res, s.tick)...)
}

func (s *sim) run(n int, input bool, mod func(*model.Snapshot)) {
	for range n {
		s.step(input, mod)
	}
}

// presentSeconds sums presence over released ticks, and the seconds
// released with ReasonReading.
func (s *sim) presentSeconds() (total, reading float64) {
	for _, h := range s.out {
		total += h.Result.Presence * h.Result.Dt.Seconds()
		if h.Result.Reason == ReasonReading {
			reading += h.Result.Dt.Seconds()
		}
	}
	return total, reading
}

func locked(s *model.Snapshot) { s.Locked = true }

func TestReadingConfirmedWhenUserReturns(t *testing.T) {
	s := newSim(model.CategoryBrowser)
	s.run(10, true, nil)
	s.run(5*60, false, nil) // reads for five minutes without touching anything
	if s.r.Pending() == 0 {
		t.Fatal("idle stretch should be held until it ends")
	}
	s.step(true, nil) // scrolls again
	if s.r.Pending() != 0 {
		t.Fatalf("%d ticks still held after input", s.r.Pending())
	}
	total, reading := s.presentSeconds()
	if total < 5*60+10-1 {
		t.Fatalf("present %.0fs, want the whole reading time", total)
	}
	// The first 30 s of idle are plain input presence; the rest is reading.
	if want := float64(5*60 - 30); reading < want-1 || reading > want+1 {
		t.Fatalf("reading %.0fs, want about %.0fs", reading, want)
	}
	for _, h := range s.out {
		if h.Result.Reason == ReasonReading && h.Result.Attention[1] != 1 {
			t.Fatalf("confirmed tick attention = %v", h.Result.Attention)
		}
	}
}

func TestIdleStretchEndingInLockKeepsGrace(t *testing.T) {
	s := newSim(model.CategoryBrowser)
	s.run(10, true, nil)
	s.run(4*60, false, nil)
	s.run(5, false, locked)
	total, reading := s.presentSeconds()
	if reading != 0 {
		t.Fatalf("%.0fs confirmed as reading despite the lock", reading)
	}
	// 10 s input + 30 s active input + half of the 2-minute docs grace.
	if want := 10 + 30 + 60.0; total < want-2 || total > want+2 {
		t.Fatalf("present %.0fs, want about %.0fs (grace rule)", total, want)
	}
}

func TestIdleStretchPastCapKeepsGrace(t *testing.T) {
	s := newSim(model.CategoryBrowser)
	s.run(10, true, nil)
	s.run(12*60, false, nil) // longer than the 8-minute docs cap
	if s.r.Pending() != 0 {
		t.Fatalf("%d ticks held past the cap", s.r.Pending())
	}
	s.step(true, nil)
	if _, reading := s.presentSeconds(); reading != 0 {
		t.Fatalf("%.0fs confirmed after the cap was exceeded", reading)
	}
}

func TestCapDependsOnContext(t *testing.T) {
	// Five idle minutes: within the 8-minute docs cap, past the 4-minute
	// default cap (editors, terminals).
	for _, tc := range []struct {
		cat  model.Category
		want bool
	}{{model.CategoryDocs, true}, {model.CategoryCode, false}} {
		s := newSim(tc.cat)
		s.run(10, true, nil)
		s.run(5*60, false, nil)
		s.step(true, nil)
		if _, reading := s.presentSeconds(); (reading > 0) != tc.want {
			t.Errorf("%s: reading %.0fs, want confirmed=%v", tc.cat, reading, tc.want)
		}
	}
}

func TestStretchEndedBySleepOrCallIsNotConfirmed(t *testing.T) {
	cases := map[string]func(*sim){
		"sleep gap": func(s *sim) {
			s.at = s.at.Add(time.Hour) // the machine slept; the next tick follows a gap
			s.lastInput = s.at
			s.step(true, nil)
		},
		"call starts on its own": func(s *sim) {
			s.step(false, func(sn *model.Snapshot) { sn.CallActive = true })
		},
	}
	for name, end := range cases {
		t.Run(name, func(t *testing.T) {
			s := newSim(model.CategoryBrowser)
			s.run(10, true, nil)
			s.run(3*60, false, nil)
			end(s)
			if _, reading := s.presentSeconds(); reading != 0 {
				t.Fatalf("%.0fs confirmed", reading)
			}
		})
	}
}

func TestFlushReleasesWithGrace(t *testing.T) {
	s := newSim(model.CategoryBrowser)
	s.run(10, true, nil)
	s.run(3*60, false, nil)
	s.out = append(s.out, s.r.Flush()...)
	if s.r.Pending() != 0 {
		t.Fatal("flush left ticks held")
	}
	if _, reading := s.presentSeconds(); reading != 0 {
		t.Fatalf("unfinished stretch confirmed: %.0fs", reading)
	}
}

func TestCallHoldsPresenceWithHiddenWindow(t *testing.T) {
	s := newSim(model.CategoryBrowser)
	s.run(10, true, nil)
	s.run(30*60, false, func(sn *model.Snapshot) { sn.CallActive = true })
	if total, _ := s.presentSeconds(); total < 30*60+10-1 {
		t.Fatalf("present %.0fs through a 30-minute call", total)
	}
}

// Every tick comes out exactly once, in order, whatever the sequence.
func TestResolverReleasesEveryTickOnceInOrder(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	for range 50 {
		s := newSim(model.CategoryBrowser)
		for range 2000 {
			switch rng.IntN(100) {
			case 0:
				s.step(false, locked)
			case 1:
				s.at = s.at.Add(time.Minute) // gap
				s.step(false, nil)
			default:
				s.step(rng.IntN(120) == 0, nil)
			}
		}
		s.out = append(s.out, s.r.Flush()...)
		if len(s.out) != s.tick {
			t.Fatalf("released %d of %d ticks", len(s.out), s.tick)
		}
		for i, h := range s.out {
			if h.Payload != i+1 {
				t.Fatalf("tick %d released at position %d", h.Payload, i+1)
			}
			r := h.Result
			sum := r.Unattributed
			for _, a := range r.Attention {
				sum += a
			}
			if r.Presence < 0 || r.Presence > 1 || sum > r.Presence+1e-9 {
				t.Fatalf("tick %d: presence %v, attention sum %v", h.Payload, r.Presence, sum)
			}
		}
	}
}
