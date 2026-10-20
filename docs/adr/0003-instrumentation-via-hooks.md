# ADR-0003: Instrument terminals and AI agents via hooks

**Status:** Proposed
**Date:** 2026-10-20
**Deciders:** Project maintainers

## Context

App-level usage can't see what a user does inside a terminal or how their
time splits between waiting on an AI agent and reading or reviewing its
output. We need command-level and agent-turn-level events without keylogging
and without capturing secrets.

## Decision

- **Shells:** install `preexec`/`precmd`-style hooks (zsh, bash via
  bash-preexec, fish events, PowerShell prompt/PSReadLine). They send one
  event per command: argv0, allowlisted subcommand, exit code, duration,
  hashed project root, and tty/pane identifiers.
- **Claude Code:** register `burnout hook claude` for `SessionStart`,
  `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Notification`, `Stop`,
  `SubagentStop` and `SessionEnd` in `~/.claude/settings.json`. Turn
  boundaries define agent-working segments. Human segments are split by the
  attention engine into reading / composing / approving / elsewhere / away.
- Both hook types write a single datagram to the agent socket with a 50 ms
  timeout and always exit 0.

## Options Considered

### Option A: Hooks (chosen)
**Pros:** exact boundaries, near-zero cost, no elevated privileges, fits the
user's mental model, and Claude Code supports hooks officially.
**Cons:** requires an install step and misses unhooked shells (for example
`sh -c` from IDEs).

### Option B: Process-tree sampling (`ps` every N s)
**Pros:** zero setup.
**Cons:** misses short commands, no exit codes, no agent turn boundaries,
higher CPU.

### Option C: PTY interception / terminal wrapper
**Pros:** sees everything, including output.
**Cons:** invasive, breaks terminal features, and captures output (secrets).
Rejected.

### Option D: Parse shell history files / Claude transcripts
**Pros:** zero setup.
**Cons:** history lacks durations and exit codes. Transcripts contain prompt
text, so we'd have to be careful to read timestamps only. Kept as a
**fallback** for Claude Code (metadata only).

## Trade-off Analysis

A gives the highest-fidelity events at the lowest cost and privacy risk. B
and D are kept as degraded fallbacks so the product still works before the
user runs `install-hooks`.

## Consequences

- Easier: failure-loop detection, incident proxies, agent reading vs. waiting.
- Harder: we have to maintain hook scripts per shell, and `install-hooks`
  must be idempotent and reversible (`uninstall-hooks`).
- Revisit: an `AgentAdapter` for Codex CLI / Aider / Cursor; OTLP receiver for
  Claude Code telemetry as a cross-check.

## Action Items
1. [ ] Embedded hook scripts + `install-hooks` / `uninstall-hooks` / `doctor`.
2. [ ] Benchmark: p99 prompt overhead < 5 ms on zsh and bash.
3. [ ] Redaction tests: argv beyond argv0/subcommand never reaches the store.
4. [ ] Claude Code adapter + human-segment state machine with trace tests.
