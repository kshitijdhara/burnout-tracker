# ADR-0003: Instrument terminals and AI agents via hooks

**Status:** Proposed
**Date:** 2026-10-20
**Deciders:** Project maintainers

## Context

App-level usage can't see what a user does inside a terminal, or how their
time splits between waiting on an AI agent and reading or reviewing its
output. We need command-level and agent-turn-level events without
keylogging, without capturing secrets, and without ever slowing down or
changing the behaviour of the user's shell or agent.

Developers often run several agent sessions (and subagents) at once, so
"agent time" and "human time" can't be one alternating timeline.

## Decision

### Shells

`preexec`/`precmd`-style hooks for zsh, bash (via bash-preexec; bash 3.2,
the macOS default, is supported), fish events and the PowerShell
prompt/PSReadLine.

- **Interactive shells only.** The hook returns immediately unless the
  shell is interactive (`[[ $- == *i* ]]` or the equivalent), so scripts and
  CI-like loops don't produce events.
- **Secrets never leave the shell function.** `argv0` and the allowlisted
  subcommand are extracted *inside* the hook function. Only those values,
  plus exit code, duration and tty/pane ids, go to the emitter, through
  environment variables or stdin. **Never as argv**: argv is visible to every
  local user via `ps` and `/proc` and gets logged by process auditing. The
  project root is HMAC'd by the daemon (keyed per install).
- **PowerShell never spawns a process.** It writes the message in-process
  with `System.IO.Pipes.NamedPipeClientStream` and a short timeout.
- zsh/bash/fish run the emitter in the background (`&!` / `disown`).
- **Coexistence:** the hooks chain with starship, atuin and iTerm2 shell
  integration rather than replacing their hooks.
- **Out of scope for Phase 1:**
  - WSL shells, which can't reach the Windows named pipe; a bridge comes in
    Phase 3.
  - Commands run on remote SSH hosts, which aren't captured by design.

### Claude Code

`burnout hook claude` is registered for:
- `SessionStart`, `SessionEnd`
- `UserPromptSubmit`
- `PreToolUse`, `PostToolUse`
- `PermissionRequest` (when available)
- `Notification`
- `Stop` (`StopFailure` is not registered until verified; `SubagentStop`
  is not registered because subagents don't change the session's state)

The hook process follows these rules:
- **It writes nothing to stdout, ever.** For `UserPromptSubmit` and
  `SessionStart`, plain stdout is added to Claude's context, so any output
  would inject text into the user's sessions. Diagnostics go to our log, not
  to stdout or stderr.
- **It always exits 0**, within a 50 ms socket-write timeout.
  The registered command ends in `|| true`, so even a Go runtime crash
  (exit code 2, which Claude Code treats as blocking) can't block the user.
- `PreToolUse`/`PostToolUse` are registered as **async** hooks where Claude
  Code supports it.
- **Budget:** < 10 ms p99 per invocation.
- It forwards a minimal event: event name, session id, timestamps, tool
  *name*. It never copies prompt, response or tool input text.
- **Terminal mapping:** the payload carries no tty, so the hook walks its
  parent processes to the `claude` process and reads its controlling
  terminal.
- **Version detection:** the hook schema is detected per Claude Code version.
  `PermissionRequest` is used when present; older versions fall back to
  `Notification`.
- **Remote sessions** (SSH, devcontainers, web) are out of scope. With no
  daemon listening, the hook drops the event silently.

**Segment rules:**
- *Approving* starts at `PermissionRequest`. It ends at whichever comes
  first afterwards: `PreToolUse`, `PostToolUse`, or input in the session's
  terminal. That keeps tool runtime from being counted as human time.
- *Interrupts:* an open agent-working segment is closed by the next
  `UserPromptSubmit`, by `SessionEnd`, by `StopFailure`, or after 10 minutes
  of inactivity. A cancelled turn may never fire `Stop`.

### Timeline model

- **One global human-state timeline**: reading, composing, approving,
  elsewhere, away, unattributed. At any moment it is attributed to at most
  one agent session: the one whose terminal holds attention (ADR-0002).
- **N agent-state timelines**, one per session: working, blocked-on-human,
  idle. These may overlap.
- **Invariant:** Σ human sub-states ≤ wall time. Parallel sessions add to
  "agent parallelism" and "time blocked on me", never to human time.

### Editing users' config files

`install-hooks` edits `.zshrc`, `.bashrc`, `config.fish`, the PowerShell
`$PROFILE` and `~/.claude/settings.json`.
- **Dotfiles:** our lines go inside a marked block (`# >>> burnout >>>` …
  `# <<< burnout <<<`). Re-running is idempotent.
- **Claude settings:** we *merge* into the existing JSON and preserve
  existing hooks. We validate before writing and respect managed and
  project-level settings, without overriding them.
- **Every edit:** take a backup into the app's data directory first, then write atomically
  (temp file + rename).
- `uninstall-hooks` removes exactly what we added and nothing else.

## Options Considered

### Option A: Hooks (chosen)
**Pros:** exact boundaries, near-zero cost, no elevated privileges, fits the
user's mental model, and Claude Code supports hooks officially.
**Cons:** requires an install step; misses unhooked shells (for example
`sh -c` from IDEs); hook schemas change across agent versions.

### Option B: Process-tree sampling (`ps` every N s)
**Pros:** zero setup.
**Cons:** misses short commands, no exit codes, no agent turn boundaries,
higher CPU. It also reads other processes' argv, which is the leak we're
avoiding.

### Option C: PTY interception / terminal wrapper
**Pros:** sees everything, including output.
**Cons:** invasive, breaks terminal features, and captures output (secrets).
Rejected.

### Option D: Parse shell history / Claude transcripts / Claude OTLP telemetry (deferred)
**Pros:** zero setup.
**Cons:** history lacks durations and exit codes. Transcripts contain full
prompts and responses. OTLP events can carry prompt text when
`OTEL_LOG_USER_PROMPTS` is set. Every one of these streams content through
our process. **Not in Phase 1.** We'll revisit only with field-level
allowlists that decode timestamps and event types and drop everything else
without buffering.

## Trade-off Analysis

A gives the highest-fidelity events at the lowest cost and privacy risk. The
price is careful engineering at the edges. Hook output must be silent,
secrets must stay inside the shell function, and parallel sessions need
their own timelines. The fallbacks in D would make setup easier, but they
bring content into the process, which a privacy-first design can't justify
in Phase 1.

## Consequences

- Easier: failure-loop detection, incident proxies, agent reading vs.
  waiting, and correct accounting with many parallel agents.
- Harder:
  - maintaining hook scripts per shell and version;
  - keeping recorded payloads for each Claude Code version as contract tests;
  - making install/uninstall exactly reversible.
- Revisit:
  - an `AgentAdapter` for Codex CLI, Aider and Cursor;
  - a WSL bridge in Phase 3;
  - allowlisted transcript/OTLP readers.

## Action Items
1. [ ] Embedded hook scripts with interactive guard and in-function argv0/subcommand extraction; `install-hooks` / `uninstall-hooks` with marked blocks, backups, atomic writes.
2. [x] **ps-leak test:** run a hooked command containing a canary secret and assert the canary never appears in `ps`/`/proc` output, the message, the DB or logs. Process listings: `TestPSLeak` (Linux, macOS); the message: `TestZshHookNeverLeaksCommandLine`; the DB and logs: `TestEndToEnd`.
3. [ ] Benchmarks in CI: p99 prompt overhead < 5 ms (zsh, bash 3.2, bash 5, fish); PowerShell in-process pipe write; Claude hook < 10 ms p99.
4. [ ] Claude Code adapter: no-stdout test (any byte on stdout fails CI), version detection, `PermissionRequest`/`StopFailure`/timeout segment closing, parent-walk tty mapping.
5. [ ] `internal/timeline`: one human timeline + N agent timelines, with property tests (non-overlapping human states; Σ human sub-states ≤ wall time).
6. [ ] Recorded payloads per Claude Code version as contract tests.
