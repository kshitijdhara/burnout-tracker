package model

import "time"

// ShellCommand is one finished interactive shell command, after the hook
// has extracted argv0 and (for allowlisted tools) a candidate subcommand.
type ShellCommand struct {
	Argv0      string // "" when the command did not resolve to a known command
	Subcommand string // validated against the per-tool allowlist by the domain
	ExitCode   int
	Start, End time.Time
	ProjectKey string // HMAC of the project root; never the path itself
	TTY        string // in memory only
	ShellPID   int    // in memory only
}

// AgentEventKind is a normalised agent lifecycle event.
type AgentEventKind string

// Agent event kinds, normalised across agents.
const (
	AgentSessionStart  AgentEventKind = "session_start"
	AgentSessionEnd    AgentEventKind = "session_end"
	AgentPromptSubmit  AgentEventKind = "prompt_submit"
	AgentToolStart     AgentEventKind = "tool_start"
	AgentToolEnd       AgentEventKind = "tool_end"
	AgentPermissionAsk AgentEventKind = "permission_request"
	AgentNotification  AgentEventKind = "notification"
	AgentStop          AgentEventKind = "stop"
	AgentStopFailure   AgentEventKind = "stop_failure"
	AgentSubagentStop  AgentEventKind = "subagent_stop"
)

// AgentEvent is one hook event from an AI coding agent.
type AgentEvent struct {
	Agent      string // "claude"
	Kind       AgentEventKind
	SessionKey string // HMAC of the agent's session id
	ToolName   string // tool name only, never inputs or outputs
	At         time.Time
	ProjectKey string
	AgentPID   int // the agent process; in memory only, used to find its terminal window
}
