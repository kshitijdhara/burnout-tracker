package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/attention"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/timeline"
)

// sessionWindows maps agent sessions to the terminal windows they run in, by
// matching a window's owning process against the agent's ancestors (the
// terminal emulator is an ancestor of the agent process).
type sessionWindows struct {
	procs     ports.ProcessInfo
	ancestors map[int]ancestry // by agent pid
}

type ancestry struct {
	pids    map[int]bool
	fetched time.Time
}

const ancestryTTL = time.Minute

func newSessionWindows(p ports.ProcessInfo) *sessionWindows {
	return &sessionWindows{procs: p, ancestors: make(map[int]ancestry)}
}

// resolve returns window → session key. When several sessions share a
// window (tabs, tmux panes), the most recently active one wins.
func (s *sessionWindows) resolve(snap model.Snapshot, sessions []timeline.Session) map[model.WindowID]string {
	out := make(map[model.WindowID]string)
	if s.procs == nil || len(sessions) == 0 {
		return out
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].LastEvent.After(sessions[j].LastEvent) })
	live := make(map[int]bool, len(sessions))
	for _, sess := range sessions {
		if sess.PID == 0 {
			continue
		}
		live[sess.PID] = true
		anc := s.ancestry(sess.PID, snap.At)
		for _, w := range snap.Windows {
			if _, taken := out[w.ID]; !taken && anc[w.PID] {
				out[w.ID] = sess.Key
			}
		}
	}
	for pid := range s.ancestors {
		if !live[pid] {
			delete(s.ancestors, pid)
		}
	}
	return out
}

func (s *sessionWindows) ancestry(pid int, now time.Time) map[int]bool {
	if a, ok := s.ancestors[pid]; ok && now.Sub(a.fetched) < ancestryTTL {
		return a.pids
	}
	pids := map[int]bool{pid: true}
	if anc, err := s.procs.Ancestors(pid, 32); err == nil {
		for _, p := range anc {
			pids[p] = true
		}
	}
	s.ancestors[pid] = ancestry{pids: pids, fetched: now}
	return pids
}

// topology summarises the active displays: their count and rectangles,
// sorted so the same layout always yields the same string.
func topology(monitors []model.Rect) string {
	rects := make([]string, 0, len(monitors))
	for _, m := range monitors {
		rects = append(rects, fmt.Sprintf("[%d,%d,%d,%d]", m.X, m.Y, m.W, m.H))
	}
	sort.Strings(rects)
	return fmt.Sprintf(`{"count":%d,"monitors":[%s]}`, len(monitors), strings.Join(rects, ","))
}

// health accumulates the local SLIs (system design §11.1) for the current
// logical day. Loop-owned.
type health struct {
	day                                   string
	presentS, attributedS                 float64
	callS, videoS, readingS               float64 // presence held by each signal
	shellEvents, agentEvents              int
	collectorErrors, flushErrors, dropped int
}

// healthJSON is the stored shape of a day's SLIs.
type healthJSON struct {
	Coverage        float64           `json:"coverage"`
	PresentS        float64           `json:"present_s"`
	AttributedS     float64           `json:"attributed_s"`
	CallS           float64           `json:"call_s"`
	VideoS          float64           `json:"video_s"`
	ReadingS        float64           `json:"reading_s"`
	ShellEvents     int               `json:"shell_events"`
	AgentEvents     int               `json:"agent_events"`
	CollectorErrors int               `json:"collector_errors"`
	FlushErrors     int               `json:"flush_errors"`
	Dropped         int               `json:"dropped"`
	Hooks           map[string]uint64 `json:"hooks,omitempty"`  // this process: accepted, rejected, queue_full
	Ingest          map[string]uint64 `json:"ingest,omitempty"` // this process: transport counters
}

func newHealth() *health { return &health{} }

func (h *health) observe(res attention.Result) {
	dt := res.Dt.Seconds()
	h.presentS += res.Presence * dt
	h.attributedS += (res.Presence - res.Unattributed) * dt
	switch res.Reason {
	case attention.ReasonCall:
		h.callS += dt
	case attention.ReasonVideo:
		h.videoS += dt
	case attention.ReasonReading:
		h.readingS += dt
	}
}

// seed continues from a stored row for day. Malformed rows are ignored.
func (h *health) seed(day, stored string) {
	var j healthJSON
	if stored == "" || json.Unmarshal([]byte(stored), &j) != nil {
		return
	}
	*h = health{
		day: day, presentS: j.PresentS, attributedS: j.AttributedS,
		callS: j.CallS, videoS: j.VideoS, readingS: j.ReadingS,
		shellEvents: j.ShellEvents, agentEvents: j.AgentEvents,
		collectorErrors: j.CollectorErrors, flushErrors: j.FlushErrors, dropped: j.Dropped,
	}
}

// json renders day's SLIs. Counters start over when the logical day
// changes; the first call adopts the current day without resetting.
func (h *health) json(day string, hooks, ingest map[string]uint64) string {
	switch h.day {
	case "":
		h.day = day
	case day:
	default:
		*h = health{day: day}
	}
	coverage := 1.0
	if h.presentS > 0 {
		coverage = h.attributedS / h.presentS
	}
	b, _ := json.Marshal(healthJSON{
		Coverage: coverage, PresentS: h.presentS, AttributedS: h.attributedS,
		CallS: h.callS, VideoS: h.videoS, ReadingS: h.readingS,
		ShellEvents: h.shellEvents, AgentEvents: h.agentEvents,
		CollectorErrors: h.collectorErrors, FlushErrors: h.flushErrors, Dropped: h.dropped,
		Hooks: hooks, Ingest: ingest,
	})
	return string(b)
}
