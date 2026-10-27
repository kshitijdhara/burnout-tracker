# Burnout Tracker — System Design

**Status:** Draft v0.3 (incorporates the [2026-10-20 design review](reviews/2026-10-20-design-review.md) and the maintainer decisions of 2026-10-21)
**Date:** 2026-10-20
**Related:** [Privacy & Security](privacy-and-security.md) (source of truth for what is collected) ·
ADRs [0001](adr/0001-local-first-go-agent.md) ·
[0002](adr/0002-attention-model-over-active-window.md) ·
[0003](adr/0003-instrumentation-via-hooks.md) ·
[0004](adr/0004-modelling-strategy.md) ·
[0005](adr/0005-packaging-and-distribution.md) ·
[0006](adr/0006-decision-model-for-classification.md) ·
[0007](adr/0007-data-protection-at-rest.md) ·
[0008](adr/0008-retroactive-idle-resolution.md) ·
[0009](adr/0009-defer-laya-spike.md) ·
[0010](adr/0010-gnome-wayland-bridge.md)

---

## 1. Problem statement

Build a desktop app that watches *how* a person works (not just *which apps*
they use), learns their personal baseline, and helps them act on early
warning signs:

1. a **strain score**: how far today and this week deviate from a healthy
   baseline (their own, plus absolute limits);
2. a **trajectory**: if current trends continue, roughly how many weeks until
   they reach their high-strain zone;
3. a **weekly review**: the main drivers and 1–2 concrete changes to try.

**Audience:** developers first, then students, researchers and project
managers. Developers spend a lot of time in terminals and, more and more,
alongside AI coding agents such as Claude Code. Usage time alone misses most
of what matters there. The other groups live mostly in the browser,
documents and meetings. So collectors are pluggable, and "what is this
activity?" is answered per role (§4.6).

**Project model:** fully open source and non-commercial. **No server.** Every
component runs on the user's machine, and every artifact is free to build
and download.

**Positioning:** a wellness and self-reflection tool, *not* a diagnostic or
medical device. Product copy says "spot early warning signs in your work
patterns", never "predict burnout". Burnout is an occupational phenomenon
in WHO ICD-11 (QD85), not a disease. We report behavioural risk indicators,
not diagnoses.

**Design principle:** the tool must not become another stressor. That means
no streaks, no guilt-driven copy, and no red badges at 11 p.m.

---

## 2. Requirements

### 2.1 Functional

| # | Requirement | Phase |
|---|-------------|-------|
| F1 | Track window usage by **attention** (not just focus), correct on multi-monitor setups | 1 |
| F2 | Detect presence vs. away, including passive presence (reading, video, calls) | 1 |
| F3 | Capture terminal activity: command name, allowlisted subcommand, duration, exit code, project (HMAC) | 1 |
| F4 | Capture AI-agent sessions (Claude Code first) as one human timeline plus N agent timelines (§4.3) | 1 |
| F5 | Compute daily features and a strain score from personal baselines plus absolute limits | 1 |
| F6 | Show today/week/trend in a local dashboard, a tray icon and a CLI (`burnout status`) | 1 |
| F7 | Short check-ins: weekly CBI, optional daily micro check-in (labels for the model) | 1 |
| F8 | Forecast time-to-threshold, shown only when the trend is statistically meaningful | 2 |
| F9 | Browser extension (per-tab domain + visibility) | Deferred |
| F10 | Optional, opt-in, end-to-end-encrypted multi-device sync | 4 |
| F11 | Role profiles (developer / student / researcher / PM / custom) and role-aware classification | 1 (rules), 2 (Laya) |
| F12 | Calendar free/busy blocks via ICS | 1 |
| F13 | Privacy controls: pause, private hours, exclusions, sensitive-apps denylist, delete recent, aggregate-only mode | 1 |
| F14 | Context annotations: on-call, exam week, release, vacation, sick | 1 |
| F15 | Weekly review loop: drivers, suggestions, user-set boundaries, adherence | 1 |
| F16 | Safety: supportive copy and regional resources when check-ins are in the high band | 1 |
| F17 | Portable backup/restore (`burnout backup`/`restore`, passphrase-encrypted archive), and import of daily features from another machine | 2 |
| F18 | `burnout doctor` (health, permissions, SLIs) and `doctor --bundle` (redacted diagnostics) | 1 |

### 2.2 Non-functional

| Concern | Target |
|---------|--------|
| Privacy | Local-first; exactly three outbound calls, all opt-in (§12). Titles never persisted; argv beyond argv0/subcommand never collected. See [Privacy & Security](privacy-and-security.md) |
| CPU (agent) | < 1% average on a laptop |
| Energy | ≤ 2 wakeups/s average; sampling pauses when locked/idle and slows on battery |
| Memory (agent) | < 60 MB RSS (`GOMEMLIMIT` set) |
| Memory (Laya sidecar) | ≤ 1.5 GB transient, on demand only, exits after 60 s idle (ADR-0006) |
| Shell overhead | < 5 ms added per prompt (p99), per supported shell |
| Claude Code hook overhead | < 10 ms per invocation (p99); tool hooks async where supported |
| Dashboard | 90-day view query < 100 ms |
| Disk | < 200 MB/year at default retention |
| Data quality | SLOs in §11.1 (coverage ≥ 98%, hook delivery ≥ 99.9%) |
| Robustness | Agent failure never breaks the user's shell or Claude Code; hooks fail open and silently |
| Platforms | macOS (last 3 versions, Apple Silicon + Intel); Windows 11 (10 best-effort, out of support since Oct 2025); Linux: Ubuntu LTS GNOME and Fedora KDE supported, Arch + Sway best-effort. A visible tray is required (Privacy & Security §5.2 AC-1): Ubuntu's GNOME ships one, other GNOME desktops need the AppIndicator extension, and headless machines and ssh sessions are not supported |
| Install | GitHub Releases only: `.dmg`, `.msi`, `.deb`/`.rpm`, tarballs, or `install.sh`/`install.ps1` with pinned-key verification. Free, no external requirements |
| Accessibility | Dashboard meets WCAG 2.2 AA; presence detection works for keyboard-only, voice-control and screen-reader users |

### 2.3 Constraints and assumptions

- Small team. Go is the main language: single static binaries,
  cross-platform builds, low resource use.
- No labelled burnout data exists on day one, so the model must work cold
  (§7, ADR-0004).
- On macOS and Wayland the OS permission models restrict what we can see.
  Every signal is optional and degrades gracefully. **Zero-permission mode**
  (app focus, idle, pointer, hooks) is a supported configuration, not an
  error state.
- "jev" in the brief is **Jev**, TypeSafe AI's closed, cloud-hosted
  "System One" decision model. Its open-weight counterpart is **Laya**
  (Convai Innovations, Apache-2.0). Both answer typed questions about a text
  state; neither forecasts time series. The score is a pure-Go statistical
  model (ADR-0004). Laya runs **locally and optionally** as an activity
  classifier (ADR-0006). Jev is not used, because it would send activity data
  to a third-party cloud.
- **Out of scope** (stated so nobody assumes otherwise):
  - remote shells over SSH;
  - Claude Code running remotely, in devcontainers or on the web;
  - WSL (bridge in Phase 3);
  - malware running as the same user.

---

## 3. Architecture

### 3.1 Components

The system is one main binary plus small, independently versioned
components. A **compatibility matrix** in `docs/compat.md` lists which
versions work together. The daemon accepts contract version N-1 from every
component.

| Component | Language | Role |
|-----------|----------|------|
| `burnout` | Go | Daemon (`burnout daemon`), hot-path ingest (`emit`, `hook`), CLI, dashboard server |
| Shell hook scripts | zsh/bash/fish/PowerShell | Emit command events; embedded in `burnout`, installed by `install-hooks` |
| `burnout-classify` | Go + ONNX Runtime | Optional Laya sidecar, sandboxed, on demand (ADR-0006) |
| GNOME Shell extension | JS (GPL-2.0+) | Wayland window/pointer bridge over a private owner-only socket (§12, ADR-0010) |
| KWin script | JS | Same, for KDE |

```
┌──────────────────────────── user's machine ─────────────────────────────┐
│  OS / desktop              Integrations (push)                           │
│  ┌──────────────┐   ┌──────────────────────────────┐                     │
│  │ focus events │   │ shell hooks → `burnout emit` │                     │
│  │ window geom. │   │ Claude Code hooks →          │                     │
│  │ pointer/idle │   │   `burnout hook claude`      │                     │
│  │ input counts │   └──────────────┬───────────────┘                     │
│  │ media/call   │                  │ v-tagged messages, ≤ 4 KiB          │
│  └──────┬───────┘                  │ socket (0700 dir, peer-cred) / pipe │
│  ┌──────▼──────────────────────────▼────────────────────────────────┐    │
│  │ burnout daemon                                                   │    │
│  │  adapters: collectors · ingest · store(sqlite) · keystore · api  │    │
│  │  ─────────────────────── ports ───────────────────────────────── │    │
│  │  domain:  sessionize → attention → timeline → classify(rules)    │    │
│  │           → rollups → features → score → review                  │    │
│  └──────┬───────────────┬──────────────────┬────────────────────────┘    │
│         ▼               ▼                  ▼                             │
│   SQLite (WAL,     burnout-classify   tray · dashboard (127.0.0.1,       │
│   0600, on FDE,     (optional,        cookie auth) · CLI                 │
│   ADR-0007)          sandboxed)                                          │
└──────────────────────────────────────────────────────────────────────────┘
```

### 3.2 Dependency rule (ports and adapters)

```
domain   (attention, sessionize, timeline, classify/rules, features, score, review)
   ▲     imports only the standard library and internal/domain/types
ports    (Collector, ProcessInfo, Clock; Sink, Hasher in app): interfaces owned by their consumers
   ▲
adapters (collector/*, ingest, store/sqlite, keystore/*, classify/laya, api, ui, integrations/*)
   ▲
cmd/     (wiring only: config → adapters → domain)
```

- The rule is enforced in CI (`go-arch-lint` or a `depguard` config). An
  adapter import from a domain package fails the build.
- `Clock` is injected; `time.Now()` is banned in domain packages (lint rule).
- Domain packages are **pure and single-threaded**: no goroutines, no I/O.
  Concurrency lives in adapters. That makes the replay tests (§14)
  deterministic.
- Interfaces are declared by the consumer. Packages are small, and names
  follow Go conventions (`attention.Engine`, not `AttentionEngineImpl`).

### 3.3 Contracts

Every cross-process message is versioned:
- hook messages (one per stream connection);
- the GNOME/KWin bridge protocol;
- the sidecar's stdin/stdout protocol;
- the export format (`schemas/v1/export.schema.json`, `burnout export`).

The rules:
- Every message carries `"v": <int>`, with a JSON Schema in `schemas/`.
- The daemon accepts versions N and N-1. Unknown fields are ignored.
  Malformed or oversized messages are dropped and **counted**, and the count
  shows up in `doctor`.
- Contract tests replay recorded payloads per supported version, including
  real Claude Code hook payloads per Claude Code version.

### 3.4 Configuration

- There is one TOML file at `<config dir>/burnout/config.toml`. Its schema,
  defaults and validation live in code.
- `burnout config get|set|add|remove|unset|validate` manages it, and the
  daemon reloads it within a second of a change. Unknown keys are an
  error, so a typo never silently leaves a privacy control off. An invalid
  file is never half-applied: a running daemon keeps its current settings
  and logs a warning, a starting one refuses to start, and `doctor`
  reports it.
- **Secrets never go in the config file.** The ICS URL and the HMAC key live
  in the OS keystore (ADR-0007). `burnout keys rotate` rotates the HMAC key,
  which starts new identities (Privacy & Security §4.5).
- Every privacy opt-in is a config key, and `burnout doctor` and the
  dashboard's Privacy page list them all with their current values.

### 3.5 Feature registry

Each daily feature is a declared unit with these fields:
- `name`, `version`, `unit`;
- `direction` (whether higher is worse);
- `group` (load / fragmentation / recovery / friction / agent);
- per-role weights;
- a doc string;
- a pure compute function.

The dashboard explanations, the schema version and the docs are generated
from the registry, so they can't drift apart.

---

## 4. Data collection

What each signal stores and keeps is defined once, in the
[data inventory](privacy-and-security.md). This section explains how
signals are gathered.

### 4.1 Attention engine (multi-monitor)

Focus-time breaks in three cases:
- docs on monitor 2 while typing on monitor 1;
- Claude Code output on a second screen;
- video or calls with no input at all.

So we model **attention as a distribution over visible windows** (ADR-0002).

**Signals and cadence.** Event-driven wherever the OS allows it:

| Signal | Cadence | Notes |
|--------|---------|-------|
| Focus change | event | |
| Window geometry/visibility | event (AXObserver / `EVENT_OBJECT_LOCATIONCHANGE`, filtered and debounced 250 ms) + 5 s reconcile | |
| Pointer position + window-at-point | 1 Hz | never persisted |
| Idle / lock | 5 s + lock events | |
| Input counts (keys, scroll, clicks) | per tick, counter only; **opt-in**, never persisted | macOS listen-only `CGEventTap` whose callback only increments a counter; Windows **Raw Input** (`RIDEV_INPUTSINK`) on a locked OS thread; key codes are never read. Without the opt-in, idle-time deltas stand in |
| Media / call | 3 s poll on macOS, on its own goroutine | a call: a regular app (with a Dock icon, so not a voice trigger or dictation utility) capturing audio (cameras are not queried, see ADR-0008); video: a display-sleep assertion from an app or a helper in its bundle (not command-line tools like `caffeinate`). macOS reads these without any permission (CoreAudio process objects, IOKit assertions) and maps helper processes to their app. Windows and Linux: not yet (ADR-0008) |

Everything pauses when the screen is locked or the user is idle, and runs at
half rate on battery. On macOS the sampling thread opts out of App Nap
(`NSProcessInfo.beginActivity`) so ticks don't drift.

**Model (v1, deterministic, explainable).** Each tick (1 s):

```
for each visible window w:
  s(w) = a_vis   * visible_area_share(w)
       + a_key   * sat(keys_5s)   * [w focused]          # sat(x) = 1 - exp(-x/k), in [0,1]
       + a_scroll* sat(scroll_5s) * [w under pointer]
       + a_hover * sat(hover_dwell(w))
       + a_video * [w shows video] + a_call * [call active in w]
       + a_rec   * exp(-Δt_last_interaction(w) / τ)

if max_w s(w) < θ_unattributed:  attention = {unattributed: presence}
else:                            attention(w) = softmax(s / T)(w) * presence

presence ∈ [0,1]:
  1.0 while input within the last 30 s, or a call is active, or video plays in a visible window
  then an idle stretch, decided when it ends (ADR-0008):
     ended by the user's own input within its cap, no lock or sleep → 1.0 throughout (confirmed idle)
     the cap is measured by OS idle time and grows if the window gets fresh agent output
     otherwise (lock, display sleep, sleep gap, past the cap)      → decays over a grace window
                                          grace   cap
     terminal with fresh agent output:    3 min   8 min
     documents/PDF/browser:               2 min   8 min
     default:                             60 s    4 min
  audio-only media does NOT hold presence
  0 when locked / paused, or display asleep unless a call is active
```

Idle ticks are held until their stretch ends, so rollups for those minutes
are written late (additive upserts merge them). `burnout timeline` shows a
day minute by minute for checking against a hand-written log.

- The weights `a_*`, the temperature `T`, and the constants `τ`, `θ` and `k`
  are config with documented defaults. They will be fitted from opt-in
  spot-labels in Phase 2.
- An **input profile** (default / keyboard-only / voice control / screen
  reader) changes the grace windows and signal weights, so assistive-tech
  users aren't marked away.
- **Invariants** (property-tested): Σ attention ≤ presence ≤ 1;
  attention ≥ 0.
- **Persistence:** **per-minute rollups per window**, holding `attended_s`,
  `focused_s` and `visible_s`. Ticks are never written. Classification
  (§4.6) runs *before* persistence, so nothing un-redacted reaches disk.
- **What each environment yields:**

  | Environment | What we see |
  |-------------|-------------|
  | macOS Spaces / full-screen | Only the current Space |
  | Windows virtual desktops | Cloaked windows excluded |
  | Linux X11 workspaces | Only the current workspace (`_NET_WM_DESKTOP` vs `_NET_CURRENT_DESKTOP`), sticky windows included; minimised (`_NET_WM_STATE_HIDDEN`), desktop and dock windows excluded |
  | Linux GNOME Wayland | Through the GNOME Shell bridge: windows on the active workspace (`located_on_workspace`), minimised and `other`-type windows (menus, tooltips) excluded, desktop and dock windows used for occlusion only; nothing while the screen is locked (GNOME disables extensions there) |
  | Other Wayland desktops | No desktop collector yet; hooks only |
  | Clamshell mode | External monitors only |
  | RDP/Citrix/VM | One window, classified as `remote-desktop` |

  `doctor` reports these.
- **Monitor topology** snapshots (count, resolution, primary) are kept as a
  context feature.

**Per-OS sources:**

| | macOS | Windows | Linux X11 | Linux Wayland |
|---|---|---|---|---|
| Focus | `NSWorkspace` + AX | `SetWinEventHook(EVENT_SYSTEM_FOREGROUND)` | `_NET_ACTIVE_WINDOW` | GNOME bridge: `display.focus_window` |
| Windows + bounds | `CGWindowListCopyWindowInfo` (titles need Screen Recording) | `EnumWindows` + `DWMWA_CLOAKED` + `MonitorFromWindow` | `_NET_CLIENT_LIST_STACKING` (reversed to topmost first), client origin translated to root plus `_NET_FRAME_EXTENTS`; monitors from RandR 1.5 | GNOME bridge: `sort_windows_by_stacking` (bottom first, reversed by the agent) and `get_frame_rect()` in logical coordinates; monitors from `get_monitor_geometry`. KWin script and Sway/Hyprland IPC not yet |
| App + pid | bundle id + pid | exe name + pid | `WM_CLASS` class (else `/proc/<pid>/comm`) + `_NET_WM_PID` | GNOME bridge: Mutter WM class (the Wayland `app_id`, or the X class under Xwayland), else GNOME's app id; `get_pid()` |
| Pointer | `NSEvent.mouseLocation` | `GetCursorPos` + `WindowFromPoint` | `XQueryPointer` root position, hit-tested against the stacking order | GNOME bridge: `global.get_pointer()`, hit-tested by the agent; wlroots limited |
| Idle | `CGEventSourceSecondsSinceLastEventType` | `GetLastInputInfo` | XScreenSaver | Mutter `org.gnome.Mutter.IdleMonitor.GetIdletime` (session bus); `ext-idle-notify-v1` elsewhere, not yet |
| Lock | `CGSessionCopyCurrentDictionary` | input desktop + `LockApp.exe` | logind `LockedHint`/`Active` (system bus), else `org.gnome.ScreenSaver.GetActive` | same as X11 |
| Input counts | listen-only `CGEventTap` (Input Monitoring, optional) | Raw Input | XInput2 raw (not yet) | not available → idle deltas |

The X11 collector is pure Go (`jezek/xgb`), so Linux builds stay
`CGO_ENABLED=0`. It is chosen when `DISPLAY` is set and
`XDG_SESSION_TYPE` isn't `wayland` (under Wayland, Xwayland would show
only X clients).

The Wayland bridges talk to the daemon over a **private peer-to-peer
connection** with a token handshake, never as a public session-bus service
(§12). On GNOME (ADR-0010) the extension
(`extensions/gnome-shell`) listens on the owner-only socket
`$XDG_RUNTIME_DIR/burnout/gnome-bridge.sock`. The agent checks that the
socket belongs to `gnome-shell` running as the same user. It then says
hello with the token it wrote to `gnome-bridge.token` (mode 0600) and
whether titles are wanted. The extension checks the caller's uid
(`SO_PEERCRED`) and the token, then answers one newline-delimited JSON
snapshot per tick (`schemas/v1/gnome-bridge.schema.json`). The snapshot
holds raw facts only: occlusion, workspace filtering and pointer
hit-testing happen in the agent's pure translate step, which uses the
same geometry code as X11. The agent picks the bridge when the session is Wayland
(`XDG_SESSION_TYPE` or `WAYLAND_DISPLAY`) and `XDG_CURRENT_DESKTOP`
includes GNOME. When the extension doesn't answer, there is no desktop
collector, and the error and `doctor` say how to enable it. The agent
reads extension protocol versions N and N-1, because an upgraded extension
loads only after the user logs out and back in. Other Wayland desktops
have no collector yet. They will fall back to app-level focus, and the
dashboard will say so.

### 4.2 Terminal instrumentation

Shell hooks, not process scanning or keylogging (ADR-0003).
`burnout install-hooks` adds a **marked block** (`# >>> burnout >>>` …
`# <<< burnout <<<`) to `.zshrc`, `.bashrc`, `config.fish` and the
PowerShell `$PROFILE`. The rules for that block:
- the original file is backed up first, into the app's data directory
  (`hook-backups/`, owner-only), never next to the file, so a backup can't
  land inside a dotfiles repository;
- the write is atomic and idempotent;
- `uninstall-hooks` restores the file exactly;
- other preexec users (starship, atuin, iTerm2 shell integration) keep
  working.

| Shell | Mechanism |
|-------|-----------|
| zsh | `add-zsh-hook preexec/precmd` |
| bash (3.2+) | vendored `bash-preexec` (loaded only if no other tool has; `HISTCONTROL` left as the user set it; the command comes from `$BASH_COMMAND`, not history) |
| fish | `fish_preexec` / `fish_postexec`, in a file in `conf.d`, so `config.fish` isn't edited |
| PowerShell 7 | `prompt` wrapper + PSReadLine; writes to the pipe **in-process** (`NamedPipeClientStream`), never spawns |

**Hook rules:**
1. **Interactive shells only** (`[[ $- == *i* ]]` or the equivalent).
   Scripts are never instrumented.
2. **The full command line never leaves the shell.** The hook function
   resolves aliases, extracts `argv0` and, for allowlisted tools only, the
   subcommand. It passes those to the emitter via environment variables and
   stdin. **Nothing sensitive goes in argv**, so nothing leaks through
   `ps`/`/proc` or process auditing. A CI test checks `ps` during hooked
   commands.
3. On zsh/bash/fish the emitter runs in the background (`&!` / `disown`)
   with a 50 ms socket timeout, and always exits 0. Shells without a
   millisecond clock (bash before 5, fish) send the command's duration
   instead of its start and end, and the emitter takes the end as now.
4. A command that doesn't resolve (a typo or a pasted token) is sent with
   an empty name and its exit code, so failure loops still count without
   recording anything the user typed.
5. The subcommand allowlist is owned by the domain classifier and rendered
   into the hook script, so a word not on it never leaves the shell.

**Event (after hook-side extraction):**

```json
{ "v": 1, "kind": "shell.cmd",
  "t_start_ms": 0, "t_end_ms": 0, "exit": 1,
  "argv0": "kubectl", "subcmd": "logs",
  "cwd": "<sent only over the local socket; daemon converts to HMAC(project root) and discards>",
  "tty": "/dev/ttys004", "pane": {"tmux": "%3", "wezterm": null, "term_session": "…"},
  "shell_pid": 81234, "ssh": false }
```

The daemon resolves the project root (cached `git rev-parse` per directory),
stores only `HMAC(key, project_root)`, and classifies `argv0` into a
category. Anything beyond argv0 and the allowlisted subcommand is never
collected. Opting in to more detail per tool is a Phase 3 consideration,
and would need its own privacy review.

**Features:** command categories over time, failure loops (same command
failing N times in M minutes), late-night infra/ssh activity, and forced
waiting on long builds and tests.

**Window attribution:** `shell_pid → tty → terminal emulator PID → window`,
with tmux/iTerm2/WezTerm/kitty pane IDs to disambiguate where available.

**Out of scope:** SSH remote shells and WSL (Phase 3 bridge).

### 4.3 AI-agent sessions (Claude Code first)

**The one-human / N-agents model.** One person can run several agent
sessions (plus subagents) at once, so there are two kinds of timelines:

- **One human timeline** with states `reading`, `composing`, `approving`,
  `elsewhere`, `away` and `unattributed`. A state is attributed to **at most
  one session at a time**: the session whose terminal holds the most
  attention.
- **N agent timelines**, one per session, with states `working`,
  `blocked_on_human` and `idle`. These may overlap.

Invariant (property-tested): Σ human sub-states ≤ wall time. Features use
both timelines: agent parallelism, time blocked on the human, human
context switches while agents work, and reading vs. composing.

**Hook events** registered by `burnout install-hooks --claude`. The
settings merge follows the same rules as §4.2: merge into the existing
file, preserve other hooks, back up first, write atomically, restore on
uninstall. It also handles user, project and managed settings.

| Event | Use |
|-------|-----|
| `SessionStart` / `SessionEnd` | open / close the session's agent timeline |
| `UserPromptSubmit` | agent → `working`; closes any open segment of the previous turn |
| `PreToolUse` / `PostToolUse` | tool mix; synchronous in Phase 0 (an `async` hook option isn't verified), budget < 10 ms |
| `PermissionRequest` (version-detected) | agent → `blocked_on_human`; human → `approving` if that terminal is attended |
| `Notification` | fallback for blocked-on-human on versions without `PermissionRequest` |
| `Stop` | agent → `idle`; marks "fresh output" for presence. (`SubagentStop` is not registered: subagents don't change the session's state) |
| `StopFailure` | agent → `idle` (error). Not registered in Phase 0 until the Claude Code version that introduced it is verified; the 10-minute timeout covers it |

**Hook rules (security-relevant):**
1. **The hook writes nothing to stdout, ever, and never blocks.** The
   registered command is `'<bin>' hook claude || true`, because a Go runtime
   crash exits with 2, which Claude Code treats as blocking. For
   `UserPromptSubmit` and
   `SessionStart`, stdout is injected into Claude's context, so a stray
   print would be a prompt injection in every user's sessions. Stderr is
   also silent. The hook always exits 0. A test asserts empty stdout for
   every event type.
2. **Allowlisted payload fields only:** `session_id`, `hook_event_name`, the
   tool name, timestamps. `cwd` goes through the same HMAC as in §4.2.
   Prompt and response text and `transcript_path` contents are never read.
3. **Segment closing.** The "approving" segment ends at the first of
   `PreToolUse`, `PostToolUse`, or terminal input after the permission
   request. Tool runtime is never counted as human time. Interrupted turns
   (Esc) may not fire `Stop`, so open `working` segments close on the next
   `UserPromptSubmit`, `SessionEnd` or `StopFailure`, or after 10 min with
   no events.
4. **Terminal mapping.** The hook walks its parent processes to the `claude`
   process and its controlling tty, then maps that to a window as in §4.2.

**Removed from Phase 1:** tailing transcript files and receiving Claude Code
OpenTelemetry. Both stream prompt content through our process. We'll
revisit with field-level allowlists only if hooks prove insufficient.

**Extensibility:** an `AgentAdapter` (`Name()`, `Install()`, `Uninstall()`,
`Translate(raw) ([]Event, error)`) adds Codex CLI, Aider, Cursor and others
in Phase 3 without changing the timeline logic.

### 4.4 Browser (deferred)

The browser extension is deferred. Until then:
- **default:** browser time is category `browser` (titles are off by
  default);
- **with opt-in title classification:** page titles are redacted and
  classified in memory (§4.6), and are never stored.

Notes for later: per-tab domain + `visibilityState` via native messaging.
Chrome and Edge only install from their stores; Firefox needs a
Mozilla-signed `.xpi`.

### 4.5 Check-ins (labels)

- **Weekly:** the Copenhagen Burnout Inventory (free to use;
  personal + work-related subscales). Only **validated translations** are
  offered, per language.
- **Daily (optional):** a one-tap micro check-in with energy and "how
  drained". This gives denser labels for validation (§7.5).
- **Free text (optional):** may be scored locally by Laya against fixed
  questions, and is discarded after scoring.
- Target effort is about 30 s a week. High-band results trigger the safety
  flow (§8.4).

Phase 1 implementation (`internal/domain/checkin`, `burnout checkin`):
- **Weekly:** both CBI subscales in English (6 personal and 7
  work-related items, original wording and response scales). Each is
  scored as the mean of 0–100 item values, with the leisure-energy item
  reversed.
- **Bands:** the CBI has no official cut-offs. The commonly used ones
  are: below 50 low, 50–74 moderate, 75–99 high, 100 severe.
- **Safety:** the flow (§8.4) shows for a subscale in the high band or
  above.
- **Due:** six days after the last weekly check-in, so it is due on the
  same weekday a week later. `status` reminds; nothing interrupts.
- **Daily:** energy (reversed) and "how drained" on a 5-point scale. It
  is unvalidated and optional.
- **Storage:** only complete check-ins are stored. Both subscales are
  saved in one transaction.
- **Process:** the CLI writes through `OpenExisting`, which never
  creates or migrates the database, so it is safe beside a running
  daemon.
- **Before the alpha:** the item wording must be checked against the
  published instrument, and the copy reviewed (§8.4).

### 4.6 Activity classification (role-aware)

Onboarding sets a **role profile**. Each profile defines a category taxonomy
and default feature weights. Classification is a cascade, cheapest first,
and runs **before persistence**:

```
activity (app id, domain-from-title*, redacted title*, argv0)
  ├─► 1. sensitive-apps denylist + user exclusions  → category `private` (identity dropped)
  ├─► 2. bundled rules (app ids, domains, commands)  ~80% of time, µs
  ├─► 3. user overrides
  ├─► 4. in-memory cache (HMAC key → category; never persisted)
  ├─► 5. Laya sidecar, optional (ADR-0006): Choice[taxonomy], p < 0.6 → uncategorized
  └─► 6. `uncategorized`, offered for labelling in the weekly review
```

\* Titles are used only with opt-in **title classification**, and are
redacted before any rule or Laya sees them. The redaction rules are in
[Privacy & Security §4.1](privacy-and-security.md). Titles and title hashes
are **never persisted**.

Phase 1 so far:
- **Roles:** developer, student, researcher and PM (`profile.role`; `setup` asks).
- **One taxonomy:** every role uses the same categories. The bundled rules
  also cover study, research and planning apps, such as RStudio, MATLAB,
  Zotero, Anki, OneNote and Linear. A role changes the feature weights
  (§3.5), not how activity is classified.
- **Starting weights:**
  - developer: as in §7.1;
  - PM: meetings, app switching, short focus blocks and chat weigh less;
  - student: after-hours work, meetings and switching weigh less;
  - researcher: after-hours work weighs less, short focus blocks more.

  They are starting points for Phase 4 to fit.
- **Changing role** rescores all of history at once, because weights are
  applied at scoring time.
- **User overrides** (`classify.overrides`) map an app id or a site
  (`domain:example.com`) to a category. They win over bundled rules but
  never over exclusions or the sensitive lists, so step 3 runs before
  step 2 in practice; the privacy step stays first.
- **Titles** (opt-in `privacy.title_classification`): for bundled
  browsers only, the site is read from the raw title in memory and the
  title is redacted. Sites are matched against exclusions and sensitive
  sites, then the user's site overrides, then a small bundled site list
  (`internal/domain/classify/domain_rules.go`), then the in-memory cache.
  Without a match the window keeps its browser category. A browser
  window whose category changes within a minute gets one row per
  category. There is no Laya step yet (ADR-0009).
- **Core hours** (`profile.core_hours`, default 09:00-18:00, weekdays)
  define after-hours work. Changing them changes the features themselves:
  - each stored day records the feature parameters it was computed with;
  - days stored under other parameters are recomputed while their minutes
    and intervals are kept (180 days by default), so baselines compare
    like with like;
  - older days keep their stored values;
  - with the default profile, nothing is recomputed.

### 4.7 Calendar busy blocks

Calendar data covers in-person meetings and meeting-heavy days, which
matter for PMs and researchers.

- **Phase 1:** an ICS URL or `.ics` file. The URL is a **bearer secret to
  the whole calendar**: it lives in the keystore, is fetched over HTTPS
  only, and is never logged. The parser keeps only `DTSTART`, `DTEND`,
  `TRANSP` and `STATUS` and discards everything else as it streams. The
  fetch is one of the three opt-in network calls (§12).
- **Phase 3:** the OS calendar (macOS EventKit).
- **Features:** meeting hours, back-to-back chains, free-time
  fragmentation, after-hours meetings.

Phase 1 so far (`internal/domain/calendar`, `internal/adapters/egress/ics`):
- **Setup:** `burnout calendar add URL|FILE` accepts https:// or webcal://
  feeds, or a local `.ics` file. Plain http:// is refused.
  - The URL goes into the OS keystore, after a confirmation that it is a
    key to the whole calendar.
  - The feed is test-fetched before it is kept.
- **Syncing:** the daemon fetches the feed a minute after it starts, then
  every 3 hours.
  - It skips fetching while paused, or when `calendar.ics.enabled = false`.
  - Each sync replaces the busy blocks from 60 days back to 2 days ahead.
  - It records which days it covered, so a day without meetings counts as
    0 rather than missing.
  - Meetings starting in private hours are dropped.
  - Nothing that starts before the last `forget` is ever imported again.
  - `burnout calendar sync | status | remove [--forget]` manage it.
- **Parsing:** the parser streams the feed. It expands RRULE (DAILY,
  WEEKLY, MONTHLY and YEARLY, with INTERVAL, COUNT, UNTIL, BYDAY with
  ordinals, BYMONTHDAY, BYMONTH and BYSETPOS), EXDATE and RECURRENCE-ID
  overrides.
  - Repeats keep their local wall-clock time across DST changes.
  - TZIDs resolve as IANA names, then common Windows names, then local time.
  - These are not meetings: all-day events, free (TRANSPARENT) events,
    cancelled events, and events longer than 12 hours. Tentative ones count
    and are marked tentative.
  - A kept property over 8 KB drops its event.
  - Caps: 20,000 events, and 5,000 instances per recurring event.
- **Fetching:** a 20 MB size cap and a 60 s timeout. Redirects must stay on
  https. No cookies are sent, the User-Agent is generic, and errors name
  only the host.
- Free-time fragmentation isn't built yet.

---

## 5. Time model

- **Logical day:** a configurable rollover, 04:00 local by default, so
  1 a.m. work belongs to the day it continues. Intervals and rollups are
  split at the rollover.
- **Offsets:** every interval stores its UTC offset. DST changes and travel
  produce correct local-day attribution, and "after-hours" uses the
  offset in effect at the time.
- **Clocks:** the daemon stamps receipt time with a monotonic clock anchored
  to wall time. Emitter timestamps are used for ordering within a message
  stream only, and skew over 5 min is rejected and counted. Suspend/resume
  and wall-clock jumps close open intervals.
- **Week start** follows the locale (Sunday or Monday).

---

## 6. Data model (SQLite)

The database isn't encrypted by the app. At-rest protection is OS
full-disk encryption plus owner-only permissions, and FDE status is checked
and warned about (ADR-0007). `id` columns are integer primary keys.

```sql
-- redacted events only; short retention (default 7 days); for reprocessing and debugging
CREATE TABLE raw_events (id INTEGER PRIMARY KEY, recv_ms INTEGER NOT NULL, v INTEGER NOT NULL,
  source TEXT NOT NULL, kind TEXT NOT NULL, payload BLOB NOT NULL);

-- per-minute attention rollups per window (default 180 days)
CREATE TABLE attention_minutes (
  minute_ms INTEGER NOT NULL, utc_offset_min INTEGER NOT NULL, logical_day TEXT NOT NULL,
  window_key TEXT NOT NULL,            -- HMAC(key, app id + window identity); 'category:<c>' in aggregate-only mode
  app_id TEXT,                         -- NULL when category = 'private' or aggregate-only
  category TEXT NOT NULL, monitor INTEGER,
  attended_s REAL NOT NULL, focused_s REAL NOT NULL, visible_s REAL NOT NULL,
  PRIMARY KEY (minute_ms, window_key));
CREATE INDEX attention_day ON attention_minutes(logical_day, category);

-- timeline intervals: shell commands, agent states, human states, presence, calendar (default 180 days)
CREATE TABLE intervals (id INTEGER PRIMARY KEY,
  start_ms INTEGER NOT NULL, end_ms INTEGER NOT NULL, utc_offset_min INTEGER NOT NULL,
  logical_day TEXT NOT NULL, kind TEXT NOT NULL,  -- shell_cmd | agent_state | human_state | presence | busy
  category TEXT, session_key TEXT,                -- HMAC of agent session id
  attrs BLOB /* JSON: argv0, subcmd, exit, state, presence reason (input|video|call), project HMAC… */);
CREATE INDEX intervals_kind ON intervals(kind, start_ms);
CREATE INDEX intervals_day  ON intervals(logical_day, kind);

CREATE TABLE daily_features (logical_day TEXT PRIMARY KEY, registry_ver INTEGER NOT NULL,
  coverage REAL NOT NULL, features BLOB NOT NULL);           -- default 2 years
CREATE TABLE scores (logical_day TEXT PRIMARY KEY, model_ver TEXT NOT NULL, strain REAL,
  status TEXT NOT NULL, components BLOB, forecast BLOB);  -- status: ok | missing | calibrating
CREATE TABLE checkins (id INTEGER PRIMARY KEY, ts_ms INTEGER NOT NULL, instrument TEXT NOT NULL,
  answers BLOB NOT NULL, score REAL);                       -- default 2 years
CREATE TABLE annotations (id INTEGER PRIMARY KEY, start_day TEXT NOT NULL, end_day TEXT NOT NULL,
  kind TEXT NOT NULL /* on_call|exam|release|vacation|sick|other */, note TEXT);
-- user-set rules (§8.2); never updated in place, so the history stays
CREATE TABLE boundaries (id INTEGER PRIMARY KEY, kind TEXT NOT NULL /* shutdown|no_weekend|meetings_max|focus_block */,
  value TEXT NOT NULL, since_day TEXT NOT NULL, until_day TEXT /* NULL while active */,
  source TEXT NOT NULL /* cli|dashboard|review */, created_ms INTEGER NOT NULL, ended_ms INTEGER);
CREATE TABLE monitor_snapshots (ts_ms INTEGER PRIMARY KEY, topology TEXT NOT NULL); -- count, resolutions, arrangement; 180 days
CREATE TABLE health_daily (logical_day TEXT PRIMARY KEY, slis TEXT NOT NULL);       -- coverage, drops, hook delivery; 180 days
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_ms INTEGER NOT NULL);
```

**Driver and settings:**
- `modernc.org/sqlite` (pure Go);
- the main loop writes in batched transactions every 15 s; maintenance
  (retention in 5,000-row chunks, recovery copies) runs on its own goroutine,
  and SQLite serialises the two writers (`busy_timeout` covers the overlap);
- `status`, `timeline` and `doctor` open the database read-only and never migrate;
- `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`, prepared
  statements;
- `secure_delete=ON` and `auto_vacuum=INCREMENTAL`, with an incremental
  vacuum and `wal_checkpoint(TRUNCATE)` after the nightly retention job.
  That's what makes deletion real (ADR-0007).

**Locations:**

| OS | Data directory | Note |
|----|----------------|------|
| macOS | `~/Library/Application Support/burnout` | Excluded from Time Machine (backup drives are often unencrypted) |
| Windows | `%LOCALAPPDATA%\burnout` | Never Documents or OneDrive |
| Linux | `$XDG_DATA_HOME/burnout` | |

Directories are `0700` and files `0600`.

**Volume:**
- attention rollups: ~600 active minutes × ~5 windows ≈ 3k rows/day;
- intervals: ~0.5–3k/day;
- total ≈ 1–2 MB/day, comfortably under the disk budget at default
  retention.

---

## 7. Features and scoring

### 7.1 Daily features (v1; each one declared in the registry, §3.5)

| Group | Features |
|-------|----------|
| **Load** | attended work hours; after-hours share (outside core hours); weekend/holiday work minutes; meeting hours |
| **Fragmentation** | context switches/hour; longest deep-work block; comms share and interrupt rate; back-to-back meetings; human context switches while agents work |
| **Recovery** | breaks (presence 0 for ≥ 5 min) count and length; overnight gap (last → first activity); days since a light day |
| **Friction** | shell failure loops and failure rate; late-night infra/ssh; approval latency trend (exploratory) |
| **Agent** | agent parallelism; time blocked on human; reading vs. composing ratio |

**Coverage** is recorded per day: the fraction of logged-in time the agent
observed. A day below the coverage threshold (default 60%), or a day the
device wasn't used, is **missing, not rest**. Work on another machine is
indistinguishable from rest, so we say so in the UI and rely on import
(F17) for multi-device users.

v1 implementation notes (`internal/domain/features`, registry version 3):
- Built: attended hours, after-hours share (weekdays), meeting hours,
  app switches per hour, longest focus block (code, terminal, docs; gaps
  up to 2 min), comms share, breaks and longest break, overnight gap,
  shell failure rate and failure loops, late-night infra/ssh commands,
  agent parallelism, minutes agents waited on you, and reading share
  (exploratory, weight 0).
- Weekend work is covered by the weekend baseline and the weekend
  guardrail rather than a separate feature.
- Registry version 2 adds the calendar:
  - meeting hours now count calendar meetings too, unioned per minute with
    meeting-app and call time, so in-person meetings count and nothing
    counts twice;
  - back-to-back meetings: meetings starting within 5 minutes of the last
    one ending;
  - after-hours meetings: a weekday's meeting hours outside core hours;
  - the last two apply only on days a calendar sync covered.
- Registry version 3 adds the last four features:
  - comms interrupt rate (fragmentation, weight 0.5, PM 0.25): moves of
    attention into chat or email from another category, per attended
    hour. Same stretch rule as app switches; Slack → Mail is not an
    interrupt.
  - app switches while agents worked (fragmentation, weight 0.5): app
    switches between minutes inside the union of working agent segments,
    per hour attended in that time; needs 30 min of it. The human
    timeline has no window labels, so it is counted from the minutes.
  - days since a light day (recovery, higher is worse, weight 0.5,
    student 0.25): observed days after the last light one in the trailing
    28 days, plus this day. A light day has attended hours below the
    user's own 25th percentile over those days, or below 4 h until 7 are
    observed. Only usable days (coverage and presence, §7.2) count, and
    this day is never tested, since today's partial hours would always
    look light. Days with no data are missing, not rest, so they neither
    count nor reset it: a vacation off the device doesn't reset it, and
    vacation annotations aren't read, because stored days aren't
    recomputed when one is added.
  - approval latency (friction, exploratory, weight 0): median length of
    the day's waits on the human, with pieces of one wait (split at
    rollover or a restart) joined. A wait ends when the agent moves on,
    which takes the human's answer. Unanswered waits time out at zero
    length and drop out.
- Coverage is the share of present time attributed to a window, from the
  day's health record. A day with under 30 min of presence is "not used".

### 7.2 Strain score v1 (cold start, no labels)

1. **Calibration:** 14 days. The UI explains why there's no score yet, and
   still shows the data.
2. **Baselines:** robust per-feature statistics (median, MAD) over a
   trailing 28 days, **kept separately for weekdays and weekends**.
   Annotated vacation and sick days are excluded from baselines. Planned
   crunch (on-call, exam, release) is included but tagged.
3. **Deviation:** `z_f = (x_f − median_f) / max(1.4826·MAD_f, ε_f)`. The
   floor `ε_f` is a per-feature constant from the registry, which avoids
   divide-by-zero on zero-inflated features. Features that are mostly zero
   use rates. z is signed so "worse" is positive, and clipped to ±4.
4. **Absolute guardrails**, evaluated next to the relative score:

   | Guardrail | Default |
   |-----------|---------|
   | Attended work per week | ≥ 55 h (WHO/ILO evidence on long working hours) |
   | Overnight gap | < 7 h |
   | Weekend work | Both weekend days with > 2 h |

   Guardrails also seed the long-term anchor, so a user who starts during
   a crunch doesn't get a burnt-out "normal".
5. **Composite:** `c = Σ_groups min(cap_g, Σ_{f∈g} w_f(role) · EWMA_7d(z_f)) + Σ guardrail penalties`.
   The group caps stop correlated features (hours, after-hours,
   last-activity) from triple-counting.
6. **Mapping:** `strain = 100 / (1 + exp(−(c − c0)/s))`, with `c0` and `s`
   documented in the registry.
7. **Anchor:** the rolling baseline is blended with a long-term anchor. The
   anchor is the 28-day window with the lowest composite in the last
   6 months, seeded from guardrails until enough history exists. This
   counters baselines drifting upward during long overwork.
8. **Explainability:** the top 3 drivers in plain language ("after-hours
   work +2.1σ vs. your usual weekdays").

v1 implementation choices (`internal/domain/strain`, `strain-v1`):
- **Annotations** (`burnout annotate`): vacation and sick days are
  excluded at scoring time, so an annotation added later also applies to
  days already stored. Planned crunch is scored and listed in the weekly
  review.
- **Smoothing:** EWMA with a 7-day half-life, so one long day moves the
  score a little and a sustained week moves it a lot.
- **Baselines:** need at least 5 weekdays or 2 weekend days in the
  window. A feature with fewer has no deviation yet.
- **Group caps:** ±2 per group, in both directions.
- **Mapping:** `c0 = 3`, `s = 1.25`. An ordinary day is about 10, a
  moderately heavier fortnight about 60, and a crunch week above 90.
- **Guardrail penalties:** weekly hours +1.5, short night +1, weekend
  work +1.
- **Anchor:** the 28-day window with the lowest mean composite (at least
  14 scored days), ending before the rolling window and starting within
  180 days. Its medians and MADs are averaged with the rolling ones.
  Until it exists, the guardrails alone counter drift.
- **Drivers:** shown at a contribution of 0.5 or more. The text says
  "than usual lately", because the smoothed deviation spans both
  weekdays and weekends.
- **Determinism:** each day sees only earlier days, so past scores never
  change. Same input, same score.
- **Storage:** the daemon stores completed days' features and scores
  during maintenance, recomputing the last two days. `burnout status`
  computes missing days in memory, so it is current even before
  maintenance runs.

### 7.3 Trajectory (Phase 2)

- A local linear trend or Kalman filter (level + slope) on daily strain,
  skipping missing days.
- The forecast is shown **only when the slope's 80% interval excludes 0**,
  and only as a range: *"If the last 3 weeks' trend continues, you may reach
  your high-strain zone in ~3–6 weeks."* It always comes with 1–2 suggestions
  (§8.2).
- Threshold: default strain 70, personalised once check-ins show where the
  user's CBI reaches the high band.

### 7.4 Learned model (Phase 2+)

- **Per-user model:** after ≥ 8 weekly check-ins, an ordinal or Bayesian
  logistic regression of the CBI band on lagged weekly features, with
  strong priors.
- **Population priors (Phase 4):** trained offline on voluntarily exported
  data, but only after the research-export gate in
  [Privacy & Security](privacy-and-security.md) is met (coarsening,
  k-anonymity, preview, consent, ethics review).
- **Stack:** research in Python (NumPy / scikit-learn / PyMC). Coefficients
  ship as JSON. **Inference is pure Go** (gonum), with `leaves` as the GBDT
  escape hatch (ADR-0004).
- **Jev/Laya** classify inputs and never produce the score (ADR-0006).

### 7.5 Validation

- **Alpha:** ≥ 8 weeks, mixed roles, with weekly CBI plus daily micro
  check-ins.
- **Primary analysis:** repeated-measures correlation (rmcorr) between
  weekly strain and CBI work-related burnout across users. Target r ≥ 0.4.
- The analysis plan is pre-registered in `research/` before the alpha
  starts.
- Secondary checks: guardrail hit rates, coverage distribution, and
  false-alarm rate (high strain with low CBI).

---

## 8. Product experience

### 8.1 Onboarding

1. Explain what is and isn't collected, linking the in-app Privacy page
   generated from the data inventory.
2. Pick a role and core hours.
3. Start in **zero-permission mode** (app focus, idle, pointer, hooks). Then
   ask for each OS permission **just-in-time**, with a one-line "what this
   adds" and a "not now" option.
4. Offer the optional integrations: shell hooks, Claude Code hooks, ICS.
5. Explain the 14-day calibration and the data-quality badge.

Built (CLI): `burnout setup` covers all five steps, interactively only.
- **Steps 1–2:** the data summary, the disk-encryption check and a typed
  "yes"; then a role and core hours (Enter keeps the defaults), saved to the
  config file, where `burnout config set profile.role|profile.core_hours`
  changes them later.
- **Step 3:** it says it starts in zero-permission mode, then offers title
  classification with what it adds, defaulting to "not now". On macOS a yes
  opens System Settings at Screen Recording. Input counts aren't offered.
  A daemon reads the setting only when it starts, so if one is running and
  the service step doesn't restart it, setup says how to.
- **Step 4:** y/N questions for the shell and Claude Code hooks (the same
  code as `install-hooks`), an ICS feed (read from the prompt, with
  `calendar add`'s warning that the URL is a secret) and the background
  service (as `service install`). Each no, and each failure, prints the
  command to run later.
- **Step 5:** the calibration days and what makes a day count, from the
  strain model's parameters. There is no separate data-quality badge yet:
  `burnout status` shows "calibrating, N of 14" and marks a day missing
  with how much of it was observed.
- **Consent versioning:** the consent file records the version of the data
  summary consented to (`consentVersion` in `cmd/burnout/consent.go`). When
  the summary changes, the version is bumped (a test fails otherwise), and
  the daemon won't start until `setup` is run again; under the service it
  logs why and exits 0, as with no consent. A consent file from before
  versioning counts as out of date.

The first run must be completed **interactively by the logged-in user**
(anti-coercion; see Privacy & Security).

### 8.2 Core loop

**Weekly review** (about 2 min):
- this week vs. your baseline, with the top drivers;
- labelling uncategorized items;
- annotating the week (on-call, exam, release, vacation);
- **1–2 suggestions** tied to the top drivers, for example "you worked past
  22:00 on 4 days; want a shutdown reminder at 21:30?";
- the user sets or edits **boundaries** (shutdown time, no-weekend rule,
  deep-work blocks), and adherence is tracked gently.

Phase 1 so far (`burnout review`, `internal/domain/review`):
- **Built:**
  - the last 7 completed days against the 7 before (average and peak
    strain);
  - drivers by the number of days they appeared;
  - limits crossed;
  - the latest weekly check-in against the previous one;
  - up to two suggestions, guardrails first, from a fixed table tied to
    features, each offering the matching boundary when none is set;
  - boundaries and adherence (below);
  - in a terminal, `burnout review` then asks (Enter skips, q stops):
    the offered boundaries, any other boundary change, marks for the
    week (the `burnout annotate` syntax), and a category for up to three
    apps whose time this week was uncategorized, saved to
    `classify.overrides`. Apps that already have an override aren't
    asked again. Sites can't be listed, since titles and the domains read
    from them are never stored: add `domain:` overrides by hand. Piped
    (`burnout review | less`), it asks nothing.
- **Boundaries** (`internal/domain/boundary`, `burnout boundary
  add|list|remove`, the dashboard's week view). Workdays are Monday to
  Friday:
  - `shutdown HH:MM`: no work after that time on workdays. Times before
    04:00 mean after midnight, and work after midnight belongs to the
    workday before (the logical day).
  - `no-weekend`: no work on Saturdays and Sundays.
  - `meetings-max H`: at most H hours of meetings and calls on workdays
    (the `meeting_hours` feature).
  - `focus-block M`: a focus block of at least M minutes on workdays
    (the `longest_focus_block` feature).
  - One rule per kind. Adding a kind that is set changes it; changing or
    removing a rule ends it rather than deleting it, so the history
    stays (`boundary list --history`).
  - A rule applies from the day after it is set, and the day of a
    change is judged by neither rule.
- **Adherence**, per active boundary over the reviewed week, each day
  judged by the rule that applied that day:
  - Work is everything but media, as for the features. Shutdown and
    no-weekend allow 15 minutes of work past the line, so a glance at a
    message doesn't count.
  - Shutdown and no-weekend are about work that didn't happen, so any
    day with recorded data is judged. A day with no data is missing,
    never kept.
  - The meetings cap and the focus block need a day observed well
    enough to score (coverage and presence, §7.2); a barely used day
    would otherwise keep the cap and break the block.
  - Vacation and sick days are left out. The review shows "kept on N of
    M days", naming up to three days missed, and the days left out.

### 8.3 Nudges

- At most 1/day, never during Do Not Disturb/Focus, a screen share or a
  presentation, and never outside the user's waking hours.
- Copy is supportive and never shaming, and every nudge can be snoozed or
  turned off.
- The dashboard has a "blur numbers" option.

Phase 1 so far (`internal/domain/nudge`, the tray):
- **Built:**
  - one nudge kind per logical day, recorded in the `nudges` control file
    so a restart can't show a second one;
  - kinds, in priority order: shutdown time passed on a workday, working
    on a weekend under the no-weekend rule (boundaries as they apply on the
    day, reread every 5 minutes), then the latest scored day's guardrails (a long
    week, a short night, a busy weekend). Guardrails come from the last
    maintenance, so they describe yesterday, and are ignored once they are
    more than two days old;
  - copy from a fixed table, with no numbers (a test enforces it);
  - shown as a tray menu item. On macOS a short title sits beside the
    icon; the AC-4 reminder takes that place first. The nudge never shows
    in the first line or the tooltip;
  - suppression, rechecked every refresh: paused, private hours, outside
    `nudges.waking_hours`, no recent snapshot (safe mode included), a call
    or video;
  - "Snooze until tomorrow" and "Don't show this kind again" in the
    item's submenu; `nudges.enabled` and one switch per kind in the
    config. Every unknown state, such as an unreadable control file,
    shows nothing.
- **Not yet:**
  - Focus/Do Not Disturb and screen-share detection. macOS exposes
    neither without a new permission (Focus authorization, Full Disk
    Access), so calls and video stand in. Windows and Linux can't see
    calls either. Each collector lists the gap in `Limitations`, and
    `burnout doctor` shows it;
  - the dashboard's "blur numbers" option.

### 8.4 Safety

When CBI is in the high band, or free text suggests distress, the app shows
supportive copy and **regional mental-health resources**. It doesn't send
alarming forecasts. A mental-health professional reviews all
score, forecast and safety copy before alpha.

Phase 1 implementation:
- **Resources:** a built-in table of free national lines, picked by the
  locale's territory (`LC_ALL`, `LC_MESSAGES`, `LANG`), plus
  findahelpline.com for every region. There is no network call.
- **Covered so far:** US, CA, GB, IE, AU, NZ, IN and DE.
- **Not yet:** free-text distress detection, which needs the Laya
  classifier.

### 8.5 Accessibility and i18n

- The dashboard meets WCAG 2.2 AA.
- Copy is localised.
- CBI is offered only in validated translations.
- Input profiles (§4.1) cover assistive-technology users.

### 8.6 Success metrics (no telemetry)

Collected via opt-in in-app surveys and alpha interviews:
- calibration completion;
- weekly check-in completion;
- 4- and 8-week retention;
- % of users who set or changed a boundary after a review (recorded
  locally: a boundary set from the review's questions or the week
  view's offer has source `review`; `boundary list --history` shows
  it);
- self-reported usefulness.

These, plus §7.5, are the alpha exit criteria.

---

## 9. Local API and UI

- **Transport:**
  - **Unix socket** in a `0700` directory (`$XDG_RUNTIME_DIR/burnout`,
    `~/Library/Application Support/burnout/run`), with a **peer-credential
    check** (`SO_PEERCRED` / `LOCAL_PEERCRED`) for the same UID.
  - **Windows named pipe** with an explicit DACL for the user's SID and
    `PIPE_REJECT_REMOTE_CLIENTS`.
- **Ingest:** one message per stream connection on the socket or pipe,
  `≤ 4 KiB`, rate-limited per source (peer PID) and globally, strictly
  parsed (schema, §3.3), fuzzed in CI.
  Malformed messages are dropped and counted. Stream sockets rather than
  datagrams, because peer-credential checks need a connection. On Windows
  the pipe name includes the user's SID, and hooks verify the pipe server's
  owner before sending.
- **Query API (HTTP+JSON over the same socket/pipe):**
  - `GET /v1/status`
  - `GET /v1/days?from&to`
  - `GET /v1/timeline?day=`
  - `GET /v1/agents?day=`
  - `GET /v1/privacy` (current opt-ins and inventory)
  - `POST /v1/checkins`
  - `POST /v1/annotations`
  - `POST /v1/pause`
  - `DELETE /v1/data?range=`
- **Dashboard:** served on `127.0.0.1:<random port>`.
  - The tray opens it with a **one-time launch token**, which the server
    exchanges for an `HttpOnly; SameSite=Strict` session cookie, then
    redirects. Tokens never stay in URLs or history.
  - A **`Host` header allowlist** (`127.0.0.1:<port>`) defeats DNS
    rebinding.
  - No CORS, a strict CSP, `X-Frame-Options: DENY`, and CSRF tokens on
    state-changing requests.
- **Phase 1 so far:** `burnout dashboard` serves the page from the CLI
  while it runs (`internal/adapters/dashboard`), hardened as in Privacy &
  Security §8.2. The tray's "Open dashboard" serves the same page from the
  daemon, with a new launch token per click.
  - **Writes:** the dashboard has the routes above as `POST /api/pause`,
    `POST /api/resume`, `DELETE /api/data?range=hour|today`,
    `POST /api/annotations`, `DELETE /api/annotations/{id}`,
    `POST /api/boundaries`, `DELETE /api/boundaries/{id}` and
    `POST /api/checkins`, each behind a per-session CSRF token and a
    same-origin check (Privacy & Security §8.2). They go through the CLI's
    control files and its short-lived read-write connection, so both
    dashboards can change data. The socket's `/v1` query API isn't built.
  - **Overview:** 90-day strain with drivers, limits and calibration.
  - **Today:** categories, apps, a 10-minute timeline with calendar
    meetings, terminal and agents.
  - **This week:** the weekly review with boundary adherence, a button
    to set the boundary a suggestion offers, boundaries and marked days
    (add and remove), and the weekly check-in form, with the support
    flow (§8.4).
  - **Privacy:** pause, resume and delete the last hour or today (with a
    confirm step), then the same rows as `burnout privacy`.
  - **Accessibility:** WCAG work covers keyboard and screen-reader
    navigation, tables behind every chart, colour never the only cue,
    dark mode, reduced motion and "blur numbers".
  - **Preview:** `BURNOUT_DASHBOARD_PREVIEW=60s go test -run TestPreview
    ./internal/adapters/dashboard/` serves it with made-up data for UI
    work.
- **UI:** a tray icon (always visible while collecting) and an embedded SPA
  via `go:embed`. A native webview (Wails) is a Phase 2 option, which would
  remove the TCP listener entirely.
- **Phase 1 so far:** the tray (`internal/adapters/tray`, on
  `fyne.io/systray`) runs inside the daemon:
  - **Threads:** on macOS it owns the main thread, as AppKit requires,
    and the daemon's work runs beside it. On Windows and Linux it runs on
    a goroutine of its own.
  - **Ordering:** the icon shows first, and the daemon collects nothing,
    hooks included, unless it does; without an icon it exits (Privacy &
    Security §5.2 AC-1).
  - **Menu:** pause and resume, delete the last hour or today, and open
    the dashboard. It reuses the CLI's control files, so the daemon's loop
    handles a click exactly like `burnout pause` or `forget`.
  - **Icons:** template images on macOS, colour icons on Windows (.ico)
    and Linux (StatusNotifierItem over D-Bus), built into the binary from
    `assets/icon/tray`.

---

## 10. Performance and scale

"Scale" for a local agent means **long-lived installs, heavy users and a
growing contributor base**, not QPS.

| Dimension | Design target | How we verify it |
|-----------|---------------|------------------|
| Heavy desktop | 50+ windows, 6 monitors, 30 tmux panes | stress trace replay |
| Heavy shell | 10k commands/day | hook benchmark + replay |
| Parallel agents | 10 sessions with subagents | timeline property tests + replay |
| History | 2 years of data | soak DB; dashboard 90-day query < 100 ms |
| Steady state | zero allocations per attention tick | `go test -bench` with `-benchmem` |
| Long run | 7-day trace at 100× speed | soak test: RSS < 60 MB, DB size within budget, no drops |

The other choices behind these targets:
- the feature pipeline is **incremental per logical day**;
- `GOMEMLIMIT` is set;
- cgo callbacks run on locked OS threads and only touch atomic counters;
- the Laya sidecar has its own budget and runs only on AC power, capped at
  500 items/day (ADR-0006).

---

## 11. Reliability and operations

### 11.1 SLIs and SLOs (local, shown in `doctor` and the data-quality badge)

| SLI | SLO |
|-----|-----|
| Coverage: present time attributed to a window/state | ≥ 98% |
| Hook delivery: hook invocations ingested | ≥ 99.9% |
| Dropped/malformed messages per day | < 10 |
| Daemon availability during logged-in time | ≥ 99.5% |
| Ingest latency | p99 < 1 ms |

Days below the coverage threshold aren't scored (§7.1).

### 11.2 Supervision and failure modes

| Failure | Behaviour |
|---------|-----------|
| Crash | launchd `KeepAlive` (on failure only) + `ThrottleInterval` 30 s; systemd `Restart=on-failure` + `RestartSec=30`, no start limit. Windows: a per-user logon task (`InteractiveToken`, so only in the user's desktop session) with `RestartOnFailure` every minute (Task Scheduler's minimum), 999 times, no time limit; not yet verified that it fires on a non-zero exit rather than only on a failed launch. Conditions a restart can't fix (no consent, a newer schema, an unusable data folder) exit 0 and stay down |
| Crash loop (3 in 10 min) | The service records its start times in an owner-only `starts` file in the data folder; a run that returns, cleanly or with an error, is dropped again, so only crashes (a panic, a native fault, a watchdog kill) count. On the 3rd start within 10 minutes it starts in **safe mode**: no native desktop collector, hooks still recorded. `doctor` and the tray's first line report it. After an hour without a crash it clears the file and exits for launchd or systemd to restart it normally. Daemons started in a terminal don't take part |
| Hang | systemd: `Type=notify` + `WatchdogSec=60`; the daemon sends `READY=1` once collecting (or once it starts waiting for a terminal daemon) and `WATCHDOG=1` from its loop every flush (15 s), paused or not, so a stuck loop is killed and restarted. launchd has no watchdog. Not yet: an internal heartbeat per collector restarts a stuck collector |
| Disk full | Stop `raw_events`, keep rollups in memory and flush when space returns, alert once |
| DB corruption | `PRAGMA quick_check` at start; restore from the latest nightly `VACUUM INTO` recovery copy (3 generations); quarantine the bad file |
| Migration failure | Back up before migrating; each migration runs in a transaction; refuse to start on a schema newer than the binary knows |
| Agent down | Hooks fail open (50 ms timeout, exit 0); the gap shows up as reduced coverage |

### 11.3 Supportability without telemetry

- **Logging:** structured `log/slog` with size-capped rotation (10 MB × 3).
  Sensitive values are wrapped in `Sensitive[T]`, whose `String()` and
  `LogValue()` return `[redacted]`, so a careless log line can't leak.
- **Diagnostics:** `burnout doctor` shows versions, the component
  compatibility check, collector and permission states, opt-ins, SLIs,
  safe-mode status and FDE status. `doctor --bundle` writes a **redacted**
  archive that the user previews before attaching it to an issue.
- **Debugging:** pprof and expvar are available only over the local socket,
  and only when debug is enabled in config.

### 11.4 Release and upgrade

- Releases use semver with conventional-commit release notes, plus a beta
  channel (GitHub pre-releases).
- Rollback is `burnout update --to vX`, and the previous binary is kept.
- **Data compatibility:** version N reads data written by N-2, and
  migrations are forward-only, with a backup first.
- Install-type-aware updates and signature checks are covered in ADR-0005.

---

## 12. Security and privacy (summary)

The full threat model, the **data inventory**, redaction rules, user
controls and verification live in
**[Privacy & Security](privacy-and-security.md)**, which wins over this
document on what is collected. The essentials:

- **Local-first, with exactly three outbound calls,** each opt-in and listed
  in `doctor`: ICS fetch, update check, model pull. No telemetry.
- **Minimise at source:**
  - no key values, screen capture or prompt/response text;
  - no argv beyond argv0 and the allowlisted subcommand;
  - no persisted titles or title hashes;
  - pointer positions are never persisted.
- **Keyed identities:** every identity hash is
  `HMAC-SHA256(per-install key, …)`, with the key in the OS keystore.
- **At rest:** OS full-disk encryption, checked with a persistent warning
  when off; owner-only permissions; the data dir kept out of synced folders
  and Time Machine; secure deletion and default retention (ADR-0007).
  Portable backups are passphrase-encrypted.
- **User controls:** pause, private hours, exclusions, sensitive-apps
  denylist → `private`, delete recent, aggregate-only mode, export, forget.
- **Anti-coercion requirements:**
  - the tray icon is always visible while collecting;
  - there is no silent or headless install;
  - first run is interactive;
  - a monthly "what's being collected" reminder;
  - no remote access.
- **Local attack surface:**
  - IPC ACLs and peer credentials;
  - a dashboard hardened against DNS rebinding and CSRF;
  - Wayland bridges over private connections only;
  - a sandboxed Laya sidecar;
  - input counting via counter-only callbacks and Raw Input.
- **Supply chain:**
  - pinned ECDSA release key plus optional cosign;
  - Actions pinned by SHA;
  - `govulncheck`, reproducible builds, SBOM, OpenSSF Scorecard;
  - `SECURITY.md`.
- **Honest scope:** malware running as the same user can read what we can.
  We don't claim otherwise.

---

## 13. Packaging and distribution (summary; ADR-0005)

- GoReleaser in GitHub Actions publishes to **GitHub Releases** only:
  - a `.dmg` (ad-hoc-signed universal `.app`);
  - `.deb`, `.rpm` and tarballs;
  - `.msi` and `.zip`;
  - the `burnout-classify` sidecar;
  - a `models-*` release carrying the Laya weights.
- **Integrity:** `checksums.txt` is signed with a **pinned ECDSA P-256
  release key**, whose public key is embedded in `install.sh`,
  `install.ps1` and the binary. The scripts verify it with
  `openssl`/.NET, so users don't need extra tools. Cosign keyless
  signatures and build-provenance attestations are additional checks.
- **Updates:** `burnout update` rejects downgrades and keeps the previous
  binary.
  - Package installs (`.deb`/`.rpm`/`.msi`) download and verify the new
    package and ask the user to install it.
  - Script and tarball installs live in a user prefix and swap atomically.
  - On macOS, automatic updates only notify.
- **macOS without a paid account:**
  - the installer uses `curl`, which avoids quarantine;
  - a self-signed local signing key, restricted to `codesign` with a prompt
    per use, keeps TCC permissions stable across updates;
  - re-signing happens only during user-initiated installs and updates.
- **Windows:** unsigned at first, with SignPath Foundation's free OSS
  signing as the upgrade path.

---

## 14. Testing strategy

| Layer | What |
|-------|------|
| Domain unit | Table tests per package; `Clock` injected; no OS |
| Property | Σ attention ≤ presence ≤ 1; human timeline non-overlapping; Σ human sub-states ≤ wall time; z finite for all inputs |
| Replay / golden | Recorded, annotated traces (2-monitor day, parallel agents, crunch week) → expected rollups, timelines, features, scores |
| Contract | Recorded hook payloads per Claude Code version; message/bridge/sidecar schemas N and N-1 (`schemas/v1` is checked against the Go validation) |
| Fuzz | Ingest parser, ICS parser, config loader, schema decoding |
| **Privacy canary** | Inject secret-looking strings into titles, argv, cwd, Claude payloads, ICS and check-in text → replay → grep the DB file bytes, WAL, logs, exports, backups and the doctor bundle; fail on any hit |
| Leak | `ps`/`/proc` inspection during hooked commands; hook stdout empty for every event |
| Platform integration | macOS, Windows, Linux (Xvfb X11; headless Mutter/Weston Wayland) runners |
| Shells | bash 3.2, bash 5, zsh, fish 3, pwsh 7, with starship/atuin present |
| Migration | Upgrade from every released schema; refuse-newer test |
| Performance | Per-tick allocation benchmarks; hook latency per shell; soak (§10) |
| Lint / arch | `golangci-lint`, `go-arch-lint` dependency rule, `time.Now` ban in domain, `govulncheck` |

---

## 15. Repository layout

```
burnout-tracker/
├── cmd/
│   ├── burnout/                 # wiring only: daemon, emit, hook, status, doctor, config, service, update…
│   └── burnout-classify/        # optional Laya sidecar
├── internal/
│   ├── domain/                  # pure, no I/O, no goroutines
│   │   ├── types/               # Event, Interval, Window, Category, Sensitive[T]…
│   │   ├── sessionize/  attention/  timeline/  classify/  features/  score/  review/
│   │   └── ports/               # Collector, ProcessInfo, Clock
│   ├── adapters/
│   │   ├── collector/{darwin,windows,x11,wayland,desktopbus}/
│   │   ├── ingest/              # socket/pipe server, schema validation, rate limits
│   │   ├── store/sqlite/        # migrations, retention, secure deletion
│   │   ├── keystore/{darwin,windows,secretservice,file}/
│   │   ├── classify/laya/       # sidecar client
│   │   ├── calendar/ics/
│   │   ├── integrations/{shell,claude}/
│   │   ├── api/                 # query API + dashboard server
│   │   └── ui/                  # tray + embedded SPA
│   └── config/                  # TOML schema, defaults, validation
├── schemas/                     # JSON Schemas for every cross-process contract
├── extensions/{gnome-shell,kwin}/
├── research/                    # Python notebooks, synthetic data, pre-registered analyses, coefficient export
├── packaging/                   # goreleaser, nfpm, wix, launchd/systemd units
├── install.sh  install.ps1
├── SECURITY.md                  # disclosure via GitHub private vulnerability reporting
└── docs/                        # design, privacy & security, ADRs, reviews, compat matrix
```

---

## 16. Roadmap

| Phase | Scope | Exit criteria |
|-------|-------|---------------|
| **0: Spike (2–3 wks)** | Domain skeleton with ports and arch lint; macOS + Windows collectors (focus, geometry, pointer, idle, Raw Input / counter tap); zsh hook with in-shell extraction; Claude Code hooks; SQLite (0600, FDE check); `status` and `doctor`; canary test. | One developer's 2-monitor day within 10% of a hand-annotated timeline; canary and ps-leak tests green (Laya spike deferred to Phase 2, ADR-0009) |
| **1: MVP (6–8 wks)** | Attention v1 + input profiles; one-human/N-agent timelines; all shells; Linux X11 + GNOME bridge; role profiles + rules; window-title opt-in + redactor; opt-in classification labels (input to the Laya spike, ADR-0009); privacy controls; annotations; ICS; features + strain v1 with guardrails and coverage; dashboard (hardened); weekly review + check-ins + safety flow; onboarding; release pipeline, installers, `update`; SLIs | ≥ 8-week alpha, 10–20 mixed-role users; SLOs met; rmcorr analysis run; < 1% CPU; no shell regressions; professional copy review done |
| **2: Personalised** | Trend forecast; per-user model; Laya spike on the alpha's labelled samples, then the optional Laya classifier if it beats rules/kNN (ADR-0009); KDE / wlroots; backup/restore/import; native webview; process isolation for native collectors if crash data warrants it | rmcorr r ≥ 0.4; false-alarm rate documented |
| **3: Integrations** | More agent adapters; WSL bridge; OS calendars; editor plugins | — |
| **4: Research** | Research export behind the privacy gate; population priors; survival model; optional peer-to-peer E2E sync | Ethics review and dataset policy in place before any data is published |

**Status (2026-10-27).**
- **Phase 0** is built. Its exit check, one developer's 2-monitor day
  within 10% of a hand-annotated timeline, hasn't been recorded yet.
- **Phase 1** scope is built and on `main`, and `v0.1.0-alpha.1` starts the
  alpha. Its exit criteria are the alpha itself, so they are open.
- **Known gaps** going into the alpha:
  - the GNOME Shell extension has only been tested in a container, not in
    a real GNOME Shell;
  - the Windows logon task has been tested in CI, not at a real logon;
  - call and video detection is macOS-only (ADR-0008);
  - KDE and wlroots Wayland desktops are Phase 2, so on them only hooks
    record.

---

## 17. Trade-offs and what to revisit

| Decision | Trade-off | Revisit when |
|----------|-----------|--------------|
| Heuristic attention model | Explainable and deterministic, but hand-tuned weights | Opt-in spot-labels exist to fit weights |
| Hooks over process scanning | Accurate and private, but needs install and misses unhooked shells | Users report gaps; add a process-tree sampler fallback |
| Transcript/OTLP fallbacks removed | Simpler and more private, but no data until hooks are installed | Hooks prove insufficient; reintroduce with field allowlists |
| OS full-disk encryption only (no app-level DB encryption) | Simplest and no lost-key failure mode, but users without FDE are unprotected against device theft (warned) | Users without FDE turn out to be common, or a security review requires app-level encryption (ADR-0007 Option B) |
| Pure-Go inference | Simple shipping, but limited model families | A heavier model clearly beats the baseline |
| Local-only | Maximum trust, but no cross-device view | Users ask for multi-device; E2E sync only |
| In-process native collectors | Simple, but a native crash takes the daemon down (mitigated by safe mode) | Crash data from alpha |
| Weekly CBI + daily micro check-in | Validated and light, but labels are still sparse | Validation underpowered |

---

## 18. Glossary

| Term | Meaning |
|------|---------|
| **Presence** | Probability the user is at the device and engaged, in [0,1] |
| **Attention** | Presence distributed across visible windows (or `unattributed`) |
| **Rollup** | Per-minute, per-window sums of attended/focused/visible seconds |
| **Interval** | A timed span on a timeline: shell command, agent state, human state, presence, busy block |
| **Human timeline** | The single sequence of the user's states across all agent sessions |
| **Agent timeline** | Per-session sequence of `working` / `blocked_on_human` / `idle` |
| **Logical day** | Local day with a configurable rollover (default 04:00) |
| **Coverage** | Fraction of logged-in time observed; low-coverage days are missing |
| **Strain** | 0–100 score from grouped, capped deviations plus guardrail penalties |
| **Guardrail** | An absolute limit evaluated regardless of personal baseline |

---

## 19. Decisions log

**Resolved:**
1. **"jev":** TypeSafe's Jev (closed, cloud). Laya is used optionally and
   locally, for classification only (ADR-0006).
2. **Audience:** developers first, then students, researchers and PMs →
   role profiles and a classification cascade.
3. **Business model:** none. Open source, no server, free distribution via
   GitHub Releases (ADR-0005).
4. **Calendar:** free/busy only, via ICS in Phase 1 (§4.7).
5. **Browser extension:** deferred (§4.4).
6. **Review findings (2026-10-20):** resolved in this version. See the
   resolution table in the review.
7. **Encryption at rest:** rely on OS full-disk encryption, with no
   app-level database encryption (ADR-0007, accepted).
8. **Window titles:** off by default; opt-in title classification, never
   persisted (ADR-0006).
9. **Anti-coercion requirements** AC-1…AC-6 adopted (Privacy & Security
   §5.2).
10. **Licence:** Apache-2.0 for the project (`LICENSE`). The GNOME Shell
    extension is GPL-2.0-or-later, as GNOME requires, and carries its own
    licence file.
