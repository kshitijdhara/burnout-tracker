package model

import "time"

// UnattributedWindowKey is the rollup key for attention that no window
// clearly holds (the user is looking at paper, a phone, or nothing on screen).
const UnattributedWindowKey = "unattributed"

// MinuteRollup is the persisted unit of attention: per minute, per window.
// 1 Hz ticks are never persisted; they are summed into these.
type MinuteRollup struct {
	Minute       time.Time // start of the minute, UTC
	UTCOffsetMin int
	LogicalDay   string // YYYY-MM-DD, see LogicalDay
	WindowKey    string // HMAC of app id + OS window identity, or UnattributedWindowKey
	AppID        string // empty when Category is private or aggregate-only mode is on
	Category     Category
	Monitor      int
	AttendedS    float64
	FocusedS     float64
	VisibleS     float64
}

// IntervalKind names a timeline.
type IntervalKind string

// Interval kinds.
const (
	IntervalShellCmd   IntervalKind = "shell_cmd"
	IntervalAgentState IntervalKind = "agent_state"
	IntervalHumanState IntervalKind = "human_state"
	IntervalPresence   IntervalKind = "presence"
	IntervalBusy       IntervalKind = "busy"
)

// Interval is a timed span on one timeline.
type Interval struct {
	Start, End   time.Time
	UTCOffsetMin int
	LogicalDay   string
	Kind         IntervalKind
	Category     Category
	SessionKey   string // HMAC of the agent session id, for agent and human states
	Attrs        IntervalAttrs
}

// IntervalAttrs holds the kind-specific fields of an interval. Only fields
// listed in the data inventory (docs/privacy-and-security.md §3) may be added.
type IntervalAttrs struct {
	Argv0      string `json:"argv0,omitempty"`
	Subcommand string `json:"subcmd,omitempty"`
	ExitCode   *int   `json:"exit,omitempty"`
	ProjectKey string `json:"project,omitempty"`
	State      string `json:"state,omitempty"`  // agent or human state
	Reason     string `json:"reason,omitempty"` // presence reason: input | video | call
	Tools      int    `json:"tools,omitempty"`  // tool calls during an agent working segment
	Agent      string `json:"agent,omitempty"`
}

// LogicalDay returns the local calendar day t belongs to, where a day starts
// at rollover (for example 4h) instead of midnight. Work at 01:00 belongs to
// the previous day.
func LogicalDay(t time.Time, rollover time.Duration) string {
	return t.Add(-rollover).Format(time.DateOnly)
}

// UTCOffsetMinutes returns t's offset from UTC in minutes.
func UTCOffsetMinutes(t time.Time) int {
	_, off := t.Zone()
	return off / 60
}
