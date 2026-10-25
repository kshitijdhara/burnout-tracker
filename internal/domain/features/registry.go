package features

import (
	"slices"
	"sort"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// RegistryVersion changes whenever a feature is added, removed or computed
// differently. Stored features from another version are recomputed.
const RegistryVersion = 1

// Group is a family of correlated features; the strain score caps each
// group's contribution so they can't count the same thing twice.
type Group string

// Feature groups (system design §7.1).
const (
	GroupLoad          Group = "load"
	GroupFragmentation Group = "fragmentation"
	GroupRecovery      Group = "recovery"
	GroupFriction      Group = "friction"
	GroupAgent         Group = "agent"
)

// Role selects per-role feature weights. Role profiles arrive with the
// classification work; every user is a developer until then.
type Role string

// Roles.
const (
	RoleDeveloper Role = "developer"
)

// Feature is one declared daily feature.
type Feature struct {
	Name    string
	Version int
	Unit    string
	Group   Group
	// HigherWorse is true when a higher value means more strain.
	HigherWorse bool
	// Epsilon floors the spread used to standardise the feature, so a
	// feature that is nearly constant (or mostly zero) can't produce huge
	// deviations from tiny changes.
	Epsilon float64
	// Weights per role; a missing role uses the developer weight. Zero
	// means shown but not scored (exploratory).
	Weights map[Role]float64
	// Label is the plain-language name used in explanations.
	Label string
	Doc   string
	// Compute returns the value for a day, or ok=false when the feature
	// doesn't apply (too little data, a weekday-only feature on a weekend).
	Compute func(d Day, p Params) (v float64, ok bool)
}

// Weight returns the feature's weight for role.
func (f Feature) Weight(r Role) float64 {
	if w, ok := f.Weights[r]; ok {
		return w
	}
	return f.Weights[RoleDeveloper]
}

func weight(w float64) map[Role]float64 { return map[Role]float64{RoleDeveloper: w} }

// Registry returns every feature, in a fixed order.
func Registry() []Feature {
	return []Feature{
		{
			Name: "attended_hours", Version: 1, Unit: "h", Group: GroupLoad, HigherWorse: true,
			Epsilon: 0.5, Weights: weight(1), Label: "hours of attended work",
			Doc: "Time you were looking at work on screen, media excluded.",
			Compute: func(d Day, _ Params) (float64, bool) {
				return attendedWork(d) / 3600, true
			},
		},
		{
			Name: "after_hours_share", Version: 1, Unit: "ratio", Group: GroupLoad, HigherWorse: true,
			Epsilon: 0.05, Weights: weight(1), Label: "share of work outside core hours",
			Doc:     "Share of a weekday's attended work outside core hours (09:00–18:00). Not computed on weekends.",
			Compute: afterHoursShare,
		},
		{
			Name: "meeting_hours", Version: 1, Unit: "h", Group: GroupLoad, HigherWorse: true,
			Epsilon: 0.25, Weights: weight(0.75), Label: "hours in meetings and calls",
			Doc: "Time in meeting apps or on a call, counted once per minute.",
			Compute: func(d Day, _ Params) (float64, bool) {
				total := 0.0
				for _, m := range d.Minutes {
					s := m.CallS
					if m.Category == model.CategoryMeeting {
						s = max(s, m.AttendedS)
					}
					total += s
				}
				return total / 3600, true
			},
		},
		{
			Name: "switches_per_hour", Version: 1, Unit: "/h", Group: GroupFragmentation, HigherWorse: true,
			Epsilon: 2, Weights: weight(1), Label: "app switches per hour",
			Doc:     "How often the app holding your attention changed, per attended hour.",
			Compute: switchesPerHour,
		},
		{
			Name: "longest_focus_block", Version: 1, Unit: "min", Group: GroupFragmentation, HigherWorse: false,
			Epsilon: 10, Weights: weight(0.75), Label: "longest focus block",
			Doc:     "Longest unbroken stretch in code, terminal or docs (gaps up to 2 minutes allowed).",
			Compute: longestFocusBlock,
		},
		{
			Name: "comms_share", Version: 1, Unit: "ratio", Group: GroupFragmentation, HigherWorse: true,
			Epsilon: 0.05, Weights: weight(0.5), Label: "share of work in chat and email",
			Doc: "Share of attended work spent in chat and email apps.",
			Compute: func(d Day, p Params) (float64, bool) {
				total := attendedWork(d)
				if total < p.MinAttended.Seconds() {
					return 0, false
				}
				comms := 0.0
				for _, m := range d.Minutes {
					if m.Category == model.CategoryComms {
						comms += m.AttendedS
					}
				}
				return comms / total, true
			},
		},
		{
			Name: "breaks", Version: 1, Unit: "count", Group: GroupRecovery, HigherWorse: false,
			Epsilon: 1, Weights: weight(0.75), Label: "breaks of 5 minutes or more",
			Doc: "Times you stepped away for at least 5 minutes between the day's first and last activity.",
			Compute: func(d Day, p Params) (float64, bool) {
				n, _, ok := breaks(d, p)
				return float64(n), ok
			},
		},
		{
			Name: "longest_break", Version: 1, Unit: "min", Group: GroupRecovery, HigherWorse: false,
			Epsilon: 10, Weights: weight(0.5), Label: "longest break",
			Doc: "Longest time away between the day's first and last activity.",
			Compute: func(d Day, p Params) (float64, bool) {
				_, longest, ok := breaks(d, p)
				return longest.Minutes(), ok
			},
		},
		{
			Name: "overnight_gap", Version: 1, Unit: "h", Group: GroupRecovery, HigherWorse: false,
			Epsilon: 0.5, Weights: weight(1), Label: "overnight gap",
			Doc:     "Time from the previous day's last activity to this day's first.",
			Compute: overnightGap,
		},
		{
			Name: "shell_failure_rate", Version: 1, Unit: "ratio", Group: GroupFriction, HigherWorse: true,
			Epsilon: 0.05, Weights: weight(0.5), Label: "share of failing shell commands",
			Doc: "Share of shell commands that exited with an error, on days with at least 10.",
			Compute: func(d Day, p Params) (float64, bool) {
				if len(d.Shell) < p.MinCommands {
					return 0, false
				}
				failed := 0
				for _, c := range d.Shell {
					if c.Exit != 0 {
						failed++
					}
				}
				return float64(failed) / float64(len(d.Shell)), true
			},
		},
		{
			Name: "failure_loops", Version: 1, Unit: "count", Group: GroupFriction, HigherWorse: true,
			Epsilon: 1, Weights: weight(0.5), Label: "repeated failing commands",
			Doc:     "Runs of 3 or more failures of the same command, each within 10 minutes of the last.",
			Compute: failureLoops,
		},
		{
			Name: "late_night_infra", Version: 1, Unit: "count", Group: GroupFriction, HigherWorse: true,
			Epsilon: 1, Weights: weight(0.75), Label: "late-night infrastructure and ssh commands",
			Doc: "Infrastructure and remote-shell commands started between 22:00 and 05:00.",
			Compute: func(d Day, p Params) (float64, bool) {
				n := 0
				for _, c := range d.Shell {
					tod := timeOfDay(c.Start)
					if (c.Group == "infra" || c.Group == "remote") && (tod >= p.LateStart || tod < p.LateEnd) {
						n++
					}
				}
				return float64(n), len(d.Shell) > 0
			},
		},
		{
			Name: "agent_parallelism", Version: 1, Unit: "sessions", Group: GroupAgent, HigherWorse: true,
			Epsilon: 0.25, Weights: weight(0.5), Label: "agents working at once",
			Doc:     "Average number of AI-agent sessions working at the same time, while any was working.",
			Compute: agentParallelism,
		},
		{
			Name: "agent_blocked_minutes", Version: 1, Unit: "min", Group: GroupAgent, HigherWorse: true,
			Epsilon: 5, Weights: weight(0.5), Label: "time agents waited on you",
			Doc: "Minutes AI agents spent waiting for your approval or answer, summed across sessions.",
			Compute: func(d Day, _ Params) (float64, bool) {
				total := time.Duration(0)
				for _, s := range d.Agent {
					if s.State == "blocked_on_human" {
						total += s.Duration()
					}
				}
				return total.Minutes(), len(d.Agent) > 0
			},
		},
		{
			Name: "reading_share", Version: 1, Unit: "ratio", Group: GroupAgent, HigherWorse: true,
			Epsilon: 0.05, Weights: weight(0), Label: "reading vs. composing with agents",
			Doc: "Share of your time with an agent's terminal spent reading rather than typing. Exploratory: shown, not scored.",
			Compute: func(d Day, _ Params) (float64, bool) {
				var reading, composing time.Duration
				for _, s := range d.Human {
					switch s.State {
					case "reading":
						reading += s.Duration()
					case "composing":
						composing += s.Duration()
					}
				}
				if reading+composing < 5*time.Minute {
					return 0, false
				}
				return reading.Seconds() / (reading + composing).Seconds(), true
			},
		},
	}
}

// Values computes every feature that applies to d.
func Values(d Day, p Params) map[string]float64 {
	out := map[string]float64{}
	for _, f := range Registry() {
		if v, ok := f.Compute(d, p); ok {
			out[f.Name] = v
		}
	}
	return out
}

// ByName returns the feature called name.
func ByName(name string) (Feature, bool) {
	i := slices.IndexFunc(Registry(), func(f Feature) bool { return f.Name == name })
	if i < 0 {
		return Feature{}, false
	}
	return Registry()[i], true
}

func afterHoursShare(d Day, p Params) (float64, bool) {
	total := attendedWork(d)
	if d.Weekend || total < p.MinAttended.Seconds() {
		return 0, false
	}
	after := 0.0
	for _, m := range d.Minutes {
		if tod := timeOfDay(m.At); work(m) && (tod < p.CoreStart || tod >= p.CoreEnd) {
			after += m.AttendedS
		}
	}
	return after / total, true
}

// switchesPerHour counts changes of the app holding attention between
// minutes close enough to be one stretch. Unattributed minutes neither
// count nor break a stretch.
func switchesPerHour(d Day, p Params) (float64, bool) {
	total := attendedWork(d)
	if total < p.MinAttended.Seconds() {
		return 0, false
	}
	switches := 0
	var prev *Minute
	for i := range d.Minutes {
		m := &d.Minutes[i]
		if m.Label == "" || m.Label == model.UnattributedWindowKey {
			continue
		}
		if prev != nil && m.Label != prev.Label && m.At.Sub(prev.At) <= time.Minute+p.FocusGap {
			switches++
		}
		prev = m
	}
	return float64(switches) / (total / 3600), true
}

var focusCategories = []model.Category{model.CategoryCode, model.CategoryTerminal, model.CategoryDocs}

func longestFocusBlock(d Day, p Params) (float64, bool) {
	if attendedWork(d) < p.MinAttended.Seconds() {
		return 0, false
	}
	var start, last time.Time
	best := time.Duration(0)
	for _, m := range d.Minutes {
		if m.Label == model.UnattributedWindowKey {
			continue
		}
		if !slices.Contains(focusCategories, m.Category) {
			start = time.Time{}
			continue
		}
		if start.IsZero() || m.At.Sub(last) > time.Minute+p.FocusGap {
			start = m.At
		}
		last = m.At
		best = max(best, last.Sub(start)+time.Minute)
	}
	return best.Minutes(), true
}

// breaks returns the number of presence gaps of at least BreakMin between
// the day's first and last presence, and the longest gap.
func breaks(d Day, p Params) (int, time.Duration, bool) {
	present := time.Duration(0)
	for _, s := range d.Presence {
		present += s.Duration()
	}
	if present < p.MinAttended {
		return 0, 0, false
	}
	spans := slices.Clone(d.Presence)
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start.Before(spans[j].Start) })
	n, longest := 0, time.Duration(0)
	end := spans[0].End
	for _, s := range spans[1:] {
		if gap := s.Start.Sub(end); gap >= p.BreakMin {
			n++
			longest = max(longest, gap)
		}
		if s.End.After(end) {
			end = s.End
		}
	}
	return n, longest, true
}

func overnightGap(d Day, _ Params) (float64, bool) {
	if d.PrevLastPresence.IsZero() || len(d.Presence) == 0 {
		return 0, false
	}
	first := d.Presence[0].Start
	for _, s := range d.Presence {
		if s.Start.Before(first) {
			first = s.Start
		}
	}
	gap := first.Sub(d.PrevLastPresence)
	if gap <= 0 || gap > 48*time.Hour {
		return 0, false
	}
	return gap.Hours(), true
}

func failureLoops(d Day, p Params) (float64, bool) {
	cmds := slices.Clone(d.Shell)
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].Start.Before(cmds[j].Start) })
	loops, run := 0, 0
	var prev Span
	for _, c := range cmds {
		switch {
		case c.Exit == 0 || c.Argv0 == "":
			run = 0
		case run > 0 && c.Argv0 == prev.Argv0 && c.Start.Sub(prev.End) <= p.LoopWindow:
			run++
		default:
			run = 1
		}
		if run == 3 {
			loops++
		}
		prev = c
	}
	return float64(loops), len(cmds) > 0
}

// agentParallelism is the time-weighted mean number of working sessions
// over the time at least one was working.
func agentParallelism(d Day, _ Params) (float64, bool) {
	type edge struct {
		at    time.Time
		delta int
	}
	var edges []edge
	for _, s := range d.Agent {
		if s.State == "working" && s.End.After(s.Start) {
			edges = append(edges, edge{s.Start, 1}, edge{s.End, -1})
		}
	}
	if len(edges) == 0 {
		return 0, false
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].at.Equal(edges[j].at) {
			return edges[i].delta < edges[j].delta
		}
		return edges[i].at.Before(edges[j].at)
	})
	var busy, weighted float64
	n := 0
	for i, e := range edges {
		if i > 0 && n > 0 {
			dt := e.at.Sub(edges[i-1].at).Seconds()
			busy += dt
			weighted += dt * float64(n)
		}
		n += e.delta
	}
	if busy == 0 {
		return 0, false
	}
	return weighted / busy, true
}
