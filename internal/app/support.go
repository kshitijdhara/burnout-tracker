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

// topology summarises the monitor layout seen in a snapshot. Collectors
// report monitor indexes per window, so the count of distinct indexes is
// what we have in Phase 0.
func topology(snap model.Snapshot) string {
	seen := map[int]bool{}
	for _, w := range snap.Windows {
		if w.Monitor >= 0 {
			seen[w.Monitor] = true
		}
	}
	idx := make([]string, 0, len(seen))
	for m := range seen {
		idx = append(idx, fmt.Sprint(m))
	}
	sort.Strings(idx)
	return fmt.Sprintf(`{"monitors_in_use":[%s]}`, strings.Join(idx, ","))
}

// health accumulates the local SLIs (system design §11.1) for the current
// logical day.
type health struct {
	day                                   string
	presentS, attributedS                 float64
	shellEvents, agentEvents              int
	collectorErrors, flushErrors, dropped int
}

func newHealth() *health { return &health{} }

func (h *health) observe(res attention.Result) {
	dt := res.Dt.Seconds()
	h.presentS += res.Presence * dt
	h.attributedS += (res.Presence - res.Unattributed) * dt
}

// json renders today's SLIs. Counters start over when the logical day
// changes; the first call adopts the current day without resetting.
func (h *health) json(day string) string {
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
	b, _ := json.Marshal(map[string]any{
		"coverage":         coverage,
		"present_s":        h.presentS,
		"shell_events":     h.shellEvents,
		"agent_events":     h.agentEvents,
		"collector_errors": h.collectorErrors,
		"flush_errors":     h.flushErrors,
		"dropped":          h.dropped,
	})
	return string(b)
}
