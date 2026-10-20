# Burnout Tracker — System Design

**Status:** Draft v0.1
**Date:** 2026-10-20
**Related ADRs:** [0001](adr/0001-local-first-go-agent.md) ·
[0002](adr/0002-attention-model-over-active-window.md) ·
[0003](adr/0003-instrumentation-via-hooks.md) ·
[0004](adr/0004-modelling-strategy.md) ·
[0005](adr/0005-packaging-and-distribution.md)

---

## 1. Problem statement

Build a desktop app that watches *how* a person works (not just *which apps*
they use), learns their personal baseline, and produces:

1. a **strain score**: how far today and this week deviate from their healthy baseline, and
2. a **trajectory**: if current trends continue, roughly how many weeks until
   they cross their personal burnout-risk threshold.

The first target user is a software developer. Developers spend a lot of time
in terminals and, more and more, working alongside AI coding agents such as
Claude Code. Time spent on app usage alone misses most of what matters there.

> **This is not a diagnostic tool.** It gives behavioural risk signals and must say
> so in the product. Burnout is a clinical/occupational construct (WHO ICD-11
> QD85). We estimate *risk indicators*, not diagnoses.

---

## 2. Requirements

### 2.1 Functional

| # | Requirement | Phase |
|---|-------------|-------|
| F1 | Track app/window usage with **attention** (not just focus), and handle multi-monitor setups correctly | 1 |
| F2 | Detect idle vs. present, including passive presence (reading, watching video) | 1 |
| F3 | Capture terminal activity: which command, how long it ran, exit code, project (hashed) | 1 |
| F4 | Capture AI-agent sessions (Claude Code first): agent-working time vs. human time, split into reading / composing / approving / elsewhere / away | 1 |
| F5 | Compute daily features and a strain score against a personal baseline | 1 |
| F6 | Show today/week/trend in a local dashboard, a tray icon and a CLI (`burnout status`) | 1 |
| F7 | Short periodic self-check-ins (labels for the model) | 1 |
| F8 | Forecast time-to-threshold | 2 |
| F9 | Browser extension: per-tab domain + visibility, via native messaging to the agent | 3 |
| F10 | Optional, opt-in, end-to-end-encrypted multi-device sync | 4 |

### 2.2 Non-functional

| Concern | Target |
|---------|--------|
| Privacy | Local-first. No network calls by default. Command args and window titles are **redacted by default** |
| CPU | < 1% average on a laptop; event-driven where the OS allows it, 1 Hz sampling otherwise |
| Memory | < 60 MB RSS for the agent |
| Shell overhead | < 5 ms added per prompt (p99) |
| Disk | < 200 MB/year (raw events expire, aggregates are kept) |
| Robustness | Agent crash must never break the user's shell or Claude Code. Hooks fail open and silently |
| Platforms | macOS (Apple Silicon + Intel), Windows 10/11, Linux (X11, GNOME/KDE/wlroots Wayland) |
| Install | `brew install`, `winget`/`scoop`, `.deb`/`.rpm`/AUR. Single binary plus platform glue |

### 2.3 Constraints and assumptions

- Small team. Go is the main language (fits single static binaries, cross-platform
  builds, low resource use).
- No labelled burnout data exists on day one. The model must work **cold**
  (see §6 and ADR-0004).
- On macOS and Wayland, OS permission models restrict what we can see. The
  design treats every signal as optional and degrades gracefully.
- "jev" from the original brief is assumed to mean **JAX** (or a Python ML
  stack in general). See ADR-0004: whatever is used for research, **inference
  ships in pure Go**.

---

## 3. High-level architecture

```
┌──────────────────────────── user's machine ─────────────────────────────┐
│                                                                          │
│  OS / desktop                Integrations (push)          Browser (P3)   │
│  ┌──────────────┐   ┌──────────────────────────────┐   ┌──────────────┐  │
│  │ focus events │   │ shell hooks (zsh/bash/fish/  │   │ extension    │  │
│  │ window list  │   │   pwsh) → `burnout emit`     │   │ tab domain + │  │
│  │ pointer pos  │   │ Claude Code hooks →          │   │ visibility   │  │
│  │ idle/input   │   │   `burnout hook claude`      │   └──────┬───────┘  │
│  │ media/screen │   │ (later: Codex, Aider, IDEs)  │          │ native   │
│  └──────┬───────┘   └──────────────┬───────────────┘          │ messaging│
│         │ pull/subscribe            │ unix socket / named pipe │          │
│  ┌──────▼───────────────────────────▼──────────────────────────▼───────┐  │
│  │                         burnoutd  (Go, user-level)                  │  │
│  │                                                                     │  │
│  │  collectors ──► event bus ──► sessionizer ──► attention engine      │  │
│  │  (per-OS)       (channels)    (intervals)     (who has my eyes?)    │  │
│  │                                   │                  │              │  │
│  │                                   ▼                  ▼              │  │
│  │                         ┌──────────────────────────────┐            │  │
│  │                         │  store: SQLite (WAL)         │            │  │
│  │                         │  raw_events (TTL 30d)        │            │  │
│  │                         │  intervals  (TTL 180d)       │            │  │
│  │                         │  daily_features (forever)    │            │  │
│  │                         │  checkins, scores            │            │  │
│  │                         └──────────────┬───────────────┘            │  │
│  │                                        ▼                            │  │
│  │                 feature pipeline (hourly + end-of-day)              │  │
│  │                                        ▼                            │  │
│  │                 scorer (pure Go: baseline z-scores → model)         │  │
│  │                                        ▼                            │  │
│  │                 local API (HTTP+JSON on unix socket / named pipe)   │  │
│  └───────────────┬───────────────────────┬─────────────────────┬───────┘  │
│                  ▼                       ▼                     ▼          │
│            tray / menubar        dashboard (localhost,     `burnout` CLI  │
│            (status + nudges)     embedded web UI)          status/export  │
└──────────────────────────────────────────────────────────────────────────┘
```

**One binary, several modes:** `burnout daemon`, `burnout emit …`,
`burnout hook claude`, `burnout status`, `burnout install-hooks`, and so on. The
hot-path subcommands (`emit`, `hook`) never open the database. They write one
datagram to the agent's socket and exit. If the agent is down, they drop the
event silently.

---

## 4. Data collection in depth

### 4.1 The attention engine (multi-monitor)

The usual approach ("time = time the window had keyboard focus") breaks in
three common cases:

- **Docs on monitor 2, editor on monitor 1.** The user reads monitor 2 but
  types on monitor 1. Focus-time gives the docs 0%.
- **Claude Code output in a terminal on monitor 2** while the user's last click
  was in the browser.
- **Video or call on one screen**, no input at all. Naive idle detection says
  "away".

So we model **attention as a probability distribution over visible windows**,
updated every tick (1 s). See ADR-0002.

**Signals per tick, by strength:**

| Signal | What it tells us | Source |
|--------|------------------|--------|
| Keyboard input → focused window | Strong engagement with focused window | OS input-activity counters (counts only, **never key values**) |
| Scroll / click → window under pointer | Strong engagement with *that* window, even if unfocused (scroll goes to the hovered window on macOS/Windows) | pointer position + window-at-point |
| Pointer resting over a window | Medium signal (people park the cursor near what they read) | pointer position |
| Window visible and unoccluded on an active monitor | Weak prior | window list + bounds + z-order + cloaked/minimized state |
| Media playing / in a call | Presence without input | macOS NowPlaying, Windows SMTC, MPRIS on Linux; mic/camera in use |
| Screen locked / display asleep | Hard zero | session lock events |

**Model (v1, deterministic, explainable):**

```
for each visible window w at tick t:
  score(w) = prior_visible(w)                      # area-weighted, small
           + K_key   * keystrokes_last_5s  * [w is focused]
           + K_scroll* scroll_last_5s      * [w under pointer]
           + K_hover * hover_dwell(w)                 # decays
           + K_media * [w is playing media/call]
           + K_recent* exp(-Δt_since_last_interaction(w) / τ)

attention(w) = softmax(score) * presence(t)

presence(t) ∈ [0,1]:
  1.0 while input within last 30 s
  then decays over an *app-aware* grace window:
     terminal with fresh agent output: 3 min   (reading)
     document/PDF/browser:             2 min
     media/call active:                stays 1.0
     default:                          60 s
  0 when locked / asleep
```

For each window we store three numbers: `focused_s`, `visible_s`, and
`attended_s` (the sum of attention). Reports use `attended_s`. The other two
are kept for debugging and for tuning the K weights later from check-in data.

**Monitor-aware extras:** we keep a monitor topology snapshot (count,
resolution, primary) so we can tell "a second monitor appeared" (desk setup)
from "laptop only" (couch, travel). That is itself a useful context feature.

**Per-OS sources:**

| | macOS | Windows | Linux X11 | Linux Wayland |
|---|---|---|---|---|
| Focus change | `NSWorkspace` notifications + AX focused window | `SetWinEventHook(EVENT_SYSTEM_FOREGROUND)` | `_NET_ACTIVE_WINDOW` PropertyNotify | compositor-specific (below) |
| Visible windows + bounds | `CGWindowListCopyWindowInfo` (titles need Screen Recording permission) | `EnumWindows` + `DwmGetWindowAttribute(DWMWA_CLOAKED)` + `MonitorFromWindow` | `_NET_CLIENT_LIST_STACKING` + geometry | GNOME: Shell extension over D-Bus; KDE: KWin script; Sway/Hyprland: IPC socket |
| Pointer position | `NSEvent.mouseLocation` (no permission) | `GetCursorPos` + `WindowFromPoint` | `XQueryPointer` | GNOME/KDE extension; wlroots: limited |
| Idle | `CGEventSourceSecondsSinceLastEventType` (no permission) | `GetLastInputInfo` | XScreenSaver ext | `ext-idle-notify-v1` / `org.gnome.Mutter.IdleMonitor` |
| Input *counts* | `CGEventTap` listen-only (Input Monitoring permission, **optional**) | `SetWindowsHookEx` LL hooks (counts only) | XInput2 raw events | not generally available; fall back to idle deltas |

Wayland gives no global window information by design. Plan on shipping a
small GNOME Shell extension and a KWin script that the agent talks to over
D-Bus (ActivityWatch's `awatcher` uses the same approach). Without them,
Linux Wayland falls back to app-level focus only, and the dashboard says so.

### 4.2 Terminal instrumentation

We use **shell hooks**, not process scanning or keylogging (ADR-0003).

`burnout install-hooks` adds a single line to `.zshrc`, `.bashrc`,
`config.fish` and the PowerShell `$PROFILE`:

- zsh: `preexec` / `precmd` via `add-zsh-hook`
- bash: `bash-preexec` (vendored) or `PROMPT_COMMAND` + `DEBUG` trap
- fish: `fish_preexec` / `fish_postexec` events
- PowerShell: wrap `prompt` + PSReadLine `AddToHistoryHandler`

Each command produces one event:

```json
{
  "kind": "shell.cmd",
  "ts_start": "...", "ts_end": "...",
  "exit": 1,
  "argv0": "kubectl",               // first word only, after alias resolution
  "category": "infra",              // classified on the agent side
  "subcmd": "logs",                 // allowlisted tools only (git, kubectl, docker, npm, go, cargo, terraform…)
  "cwd_hash": "hmac(project_root)", // stable per project, not reversible
  "tty": "/dev/ttys004",
  "term": {"tmux_pane": "%3", "term_session_id": "...", "wezterm_pane": null},
  "shell_pid": 81234,
  "ssh": false
}
```

**Privacy defaults:** full argv is never stored, because command lines contain
secrets (`curl -H "Authorization: …"`, `export AWS_SECRET…`). Only
`argv0` + an allowlisted subcommand are kept. Users can opt in to more detail
per tool.

**Features this enables:**

- command categories over time (build/test, VCS, infra/deploy, remote/ssh, editors, agents)
- **failure loops**: the same command failing N times within M minutes, a frustration proxy
- late-night `ssh`/`kubectl`/`terraform`, an incident or on-call proxy
- long-running builds/tests = forced waiting, and whether the user context-switches while waiting

**Window attribution:** we map `shell_pid → tty → terminal emulator PID → window`
by walking the process tree. Tabs and panes inside one window are
disambiguated with terminal-specific identifiers (tmux active pane, iTerm2
session ID, WezTerm/kitty pane IDs). Where that isn't possible, the window
is the unit of attribution.

### 4.3 AI-agent sessions (Claude Code first)

Claude Code exposes **hooks** configured in `~/.claude/settings.json`.
`burnout install-hooks --claude` registers `burnout hook claude` for these
events:

| Hook event | Meaning for us |
|------------|----------------|
| `SessionStart` / `SessionEnd` | session boundaries |
| `UserPromptSubmit` | human hands work to the agent → **agent-working segment starts** |
| `PreToolUse` / `PostToolUse` | agent activity inside the turn (tool mix: edits vs. bash vs. reads) |
| `Notification` | agent is **blocked on the human** (permission prompt / waiting for input) |
| `Stop` / `SubagentStop` | agent finished → **human segment starts** |

Each hook receives JSON on stdin (`session_id`, `transcript_path`, `cwd`,
`hook_event_name`, …). We forward a minimal event and never copy prompt
or response text.

**Timeline for one session:**

```
UserPromptSubmit      Notification(permission)   PostToolUse          Stop                 UserPromptSubmit
      │── agent working ──│── human: approving ──│── agent working ──│── human segment ───│
                                                                      │  split by attention│
                                                                      │  engine into:      │
                                                                      │   reading          │
                                                                      │   composing        │
                                                                      │   elsewhere        │
                                                                      │   away             │
```

The "invert the agent time" idea is the right starting point, but the inverse
is not the same as reading. We split each human segment using the attention engine:

| Sub-state | Rule |
|-----------|------|
| **reading** | the Claude Code terminal window holds most of the attention, few keystrokes, scroll/hover or fresh output |
| **composing** | keystrokes going to that terminal (typing the next prompt) |
| **approving** | the `Notification` → next tool/stop window (decision latency) |
| **elsewhere** | attention is on another window (browser, Slack, editor) |
| **away** | presence ≈ 0 |

We do the same during **agent-working** segments: is the human watching the
agent, multitasking, or away? "Context switches while the agent works" is
probably one of the more useful fragmentation signals for agent-heavy work.

**Sanity check:** we estimate an expected reading time from the response size
(the token count from the transcript length, never the content) and flag
"reading" that is 10× longer or shorter than expected.

**Fallbacks:** if hooks aren't installed, we tail
`~/.claude/projects/**/*.jsonl` timestamps (metadata only). Claude Code's
OpenTelemetry export (including its active-time metrics) can be received by
a local OTLP endpoint in the agent as a cross-check.

**Extensibility:** an `AgentAdapter` interface (`Name()`, `Install()`,
`Translate(raw) []Event`) lets us add Codex CLI, Aider, Cursor and others
later without changing the sessionizer.

### 4.4 Browser (Phase 3)

A WebExtension (Chrome/Edge/Firefox) reports per-tab **domain** (not full
URL by default), `document.visibilityState`, audible state and window ID to
the agent via **native messaging**. That host is the same `burnout` binary in
`native-host` mode. Chromium already does occlusion tracking, so a tab in a
visible window on another monitor reports `visible`, which plugs straight into
the attention engine as a richer "what's inside the browser window" signal.

### 4.5 Self check-ins (labels)

These run weekly, plus an optional daily one-tap mood prompt, from the tray:

- **Weekly:** a short validated instrument. The Copenhagen Burnout Inventory
  (free to use; personal + work-related subscales) is a good fit. Avoid MBI
  (licensed).
- **Daily (optional):** 1–2 sliders: energy and "how drained do you feel".
- **Sleep (optional):** self-reported hours, or inferred from first and last
  activity (a weak proxy, labelled as such).

Check-ins are the ground truth that eventually turns the heuristic score
into a learned model (§6). The UX must keep them very light: about 30 s a week.

---

## 5. Data model (SQLite)

```sql
-- append-only, short retention (default 30 days)
CREATE TABLE raw_events (
  id        INTEGER PRIMARY KEY,
  ts        INTEGER NOT NULL,           -- unix ms
  source    TEXT    NOT NULL,           -- os.focus | os.idle | shell | agent.claude | browser
  kind      TEXT    NOT NULL,
  payload   BLOB    NOT NULL            -- JSON
);

-- derived, medium retention (default 180 days)
CREATE TABLE intervals (
  id          INTEGER PRIMARY KEY,
  start_ts    INTEGER NOT NULL,
  end_ts      INTEGER NOT NULL,
  kind        TEXT NOT NULL,            -- attention | shell_cmd | agent_turn | human_segment | idle | locked
  app_id      TEXT,                     -- bundle id / exe name
  category    TEXT,                     -- code | terminal | comms | meeting | docs | browse | media | other
  window_key  TEXT,                     -- hashed window identity
  monitor     INTEGER,
  attended_s  REAL, focused_s REAL, visible_s REAL,
  attrs       BLOB                      -- JSON: sub_state, exit code, argv0, session_id…
);
CREATE INDEX intervals_time ON intervals(start_ts);

-- kept forever (tiny): one row per local day
CREATE TABLE daily_features (
  day         TEXT PRIMARY KEY,         -- YYYY-MM-DD local
  features    BLOB NOT NULL,            -- JSON map name → value (versioned)
  schema_ver  INTEGER NOT NULL
);

CREATE TABLE checkins (ts INTEGER PRIMARY KEY, instrument TEXT, answers BLOB, score REAL);
CREATE TABLE scores   (day TEXT PRIMARY KEY, strain REAL, components BLOB, forecast BLOB, model_ver TEXT);
```

- Driver: `modernc.org/sqlite` (pure Go) keeps Linux and Windows builds free
  of cgo. macOS needs cgo anyway for AppKit/CoreGraphics, so either driver
  works there.
- Encryption at rest: a database key is held in the OS keychain (macOS
  Keychain, Windows Credential Manager / DPAPI, Secret Service on Linux). The
  file is `0600`. SQLCipher is worth considering if a security review asks for it.
- Retention jobs run nightly. `burnout export` writes JSON/CSV and
  `burnout forget --since …` deletes data. Both matter for trust.

---

## 6. Features and scoring

### 6.1 Daily features (v1 set)

Feature choice follows occupational-health research on overwork and recovery,
plus developer-specific signals:

| Group | Feature | Why |
|-------|---------|-----|
| **Load** | attended work hours; share in "work" categories | volume |
| | after-hours share (outside the user's configured or learned core hours) | lost recovery time |
| | weekend/holiday work minutes | lost recovery time |
| | first-activity / last-activity time | rough proxy for the sleep window |
| **Fragmentation** | context switches/hour (attention changes > N s) | cognitive load |
| | longest uninterrupted deep-work block | ability to focus |
| | comms share (Slack/Teams/mail) and interrupt rate | reactive work |
| | context switches during agent-working segments | waiting fragmentation |
| **Recovery** | number and length of breaks (presence = 0 for ≥ 5 min) | micro-recovery |
| | days since a "light day" | sustained load |
| **Friction** | shell failure loops; failure rate | frustration |
| | late-night infra/ssh activity | incidents/on-call |
| | approval latency trend (Claude Code `Notification` → response) | decision fatigue (weak, exploratory) |
| | keystroke-rate variance vs. baseline (counts only, optional) | fatigue (weak, exploratory) |
| **Agent work** | reading vs. composing vs. elsewhere ratio; agent-working share | how the work is shifting |

Feature values are versioned (`schema_ver`) so the model can be retrained
when definitions change.

### 6.2 Strain score: v1 (cold start, no labels)

1. **Baseline:** for each feature, robust per-user statistics (median and MAD)
   over a trailing 28-day window, after a **14-day calibration period**. Until
   calibration ends, the UI shows data but no score.
2. **Deviation:** `z_f = (x_f − median_f) / (1.4826·MAD_f)`, signed so that
   "worse" is positive, then clipped to ±4.
3. **Composite:** `strain = Σ w_f · EWMA_7d(z_f)` with weights from the
   literature-informed defaults (load and recovery weighted highest). The
   result is mapped to 0–100.
4. **Explainability:** the dashboard always shows the top 3 contributing
   components ("after-hours work +2.1σ vs. your baseline").

The baseline also drifts upward if someone stays overworked for long enough. To
counter that, keep a **long-term anchor** (the best 28-day window in the last
6 months) next to the rolling baseline, and score against a blend of the two.

### 6.3 Trajectory / "how far off" (Phase 2)

- Fit a local linear trend (or a simple Kalman filter for level + slope) on the
  daily strain series.
- If the slope is positive, report the estimated **days until strain crosses
  the user's threshold** with an uncertainty band. The threshold starts at a
  default (for example strain 70) and is personalised from check-ins: the
  strain level at which CBI scores reached the "high" band.
- Phrase it carefully: *"If the last 3 weeks' trend continues, you'll reach
  your high-strain zone in ~3–6 weeks."* Never give a single date as if it
  were certain.

### 6.4 Learned model (Phase 2+)

When a user has about 8 or more weekly check-ins:

- **Per-user model:** ordinal or Bayesian logistic regression of CBI band on
  lagged weekly features. Strong priors keep it stable with few points.
- **Population priors (opt-in only):** if users choose to donate anonymised
  weekly feature vectors and check-in scores, train a hierarchical model
  offline. Ship only the priors and coefficients with app updates; no raw data
  leaves the device.
- **Survival framing:** time-to-"high-burnout band" as a discrete-time hazard
  model. This needs cohort data, so it is Phase 4 research.

**Stack:** research and training in Python (JAX / NumPy / scikit-learn /
PyMC) on donated or synthetic data. Export coefficients as JSON. **Inference
runs in pure Go** (gonum), with no Python or ONNX runtime in the shipped
binary. If we later need tree models, `leaves` (pure-Go LightGBM/XGBoost
inference) is the escape hatch. See ADR-0004.

---

## 7. Local API and UI

- **Transport:** HTTP+JSON over a Unix socket (macOS/Linux) or a named pipe
  (Windows). The dashboard is served on `127.0.0.1:<random>` with a per-launch
  token in the URL, so other local users and web pages can't read it.
- **Endpoints (sketch):**
  - `GET /v1/status`: today's attended hours, strain, top drivers
  - `GET /v1/days?from&to`: daily features + scores
  - `GET /v1/timeline?day=`: intervals for a day's timeline view
  - `GET /v1/agents/sessions?day=`: agent sessions with the human-segment breakdown
  - `POST /v1/checkins`
  - `POST /v1/ingest`: hot path used by `emit`/`hook`. In practice this is a
    unixgram datagram, not HTTP, to keep it under 1 ms
- **UI:** a tray/menubar icon (`fyne.io/systray` or `getlantern/systray`) and
  an embedded SPA (built once, `go:embed`). Wails or Fyne for a native
  window is a Phase 2 decision; a browser tab is fine for v1.
- **Nudges:** gentle and rate-limited (at most 1/day). For example: "4th day
  this week past 10 pm. Want to set a shutdown reminder?"

---

## 8. Reliability and operations

- **Supervision:** launchd LaunchAgent (macOS), systemd `--user` unit
  (Linux), and a Task Scheduler logon task or Startup entry (Windows). Each
  restarts on crash.
- **Hot-path safety:** `emit`/`hook` use a 50 ms timeout on the socket write
  and always exit 0, so Claude Code hooks can never block the agent. Shell hooks
  run the emitter in the background (`&!` in zsh, `disown` in bash).
- **Backpressure:** a bounded in-memory queue with drop-oldest for
  low-value events (pointer ticks). High-value events (shell, agent) go to a
  small on-disk spool if the DB is busy.
- **Clock and sleep:** monotonic clocks for durations. Suspend and resume
  close all open intervals. Time-zone changes are stored per day.
- **Self-telemetry:** local only. `burnout doctor` reports which collectors
  are active, missing permissions, hook health and queue drops.
- **Upgrades:** `daily_features` uses `schema_ver`, and migrations are
  embedded and run at start.

**Load estimate:** about 1 attention tick/s × 86,400 s/day, but only *changes*
are persisted as intervals. Typical volume is 2–5k intervals/day plus
200–2,000 shell events/day, roughly 1–3 MB/day raw before TTL. That is well
within the disk budget.

---

## 9. Security and privacy

This app collects data at a level close to surveillance software. Trust is
the product.

1. **Local-first.** No network calls by default, including no phone-home
   analytics. Update checks are opt-in.
2. **Minimise at the source.** No key values, no screen capture, no
   prompt/response text, no full command lines, no full URLs. Window titles
   are off by default; when enabled, they're classified locally and only the
   category and a hash are stored.
3. **Personal, not managerial.** No team or manager dashboard in the roadmap.
   If team features ever come, they must be aggregate, opt-in and k-anonymous.
   Write this down publicly.
4. **Encrypted at rest**, and export/forget are always available.
5. **Open source the agent.** People should be able to verify what is
   collected, which also helps distribution through Homebrew and AUR.

---

## 10. Packaging and distribution (summary; see ADR-0005)

- **GoReleaser** builds and publishes everything from tags.
- **macOS:** a signed and notarized `.app` bundle shipped through a
  **Homebrew cask**. TCC permissions (Accessibility, Screen Recording, Input
  Monitoring) attach to a signed bundle ID. A bare formula binary would get its
  permissions attributed to the user's terminal. Build on macOS CI runners
  because cgo cross-compiles poorly.
- **Linux:** Homebrew formula (Linuxbrew), `.deb`/`.rpm` via nfpm, AUR, and
  tarballs. Also ship the GNOME Shell extension (extensions.gnome.org) and a
  KWin script. Flatpak sandboxing would block most collectors, so skip it.
- **Windows:** winget + Scoop, MSI via WiX, and Authenticode signing (to
  avoid SmartScreen warnings).
- **Browser (P3):** Chrome Web Store, Firefox AMO, Edge Add-ons. The installer
  registers the native messaging host manifest.

---

## 11. Proposed repository layout

```
burnout-tracker/
├── cmd/burnout/                 # single binary: daemon, emit, hook, status, doctor, install-hooks
├── internal/
│   ├── collector/               # interface + per-OS impls (build tags)
│   │   ├── darwin/  windows/  x11/  wayland/{gnome,kde,wlroots}/
│   ├── integrations/
│   │   ├── shell/               # hook scripts (embedded) + installer
│   │   └── agents/claude/       # AgentAdapter impl
│   ├── bus/                     # typed events, channels
│   ├── sessionize/              # raw events → intervals
│   ├── attention/               # attention engine (pure, heavily unit-tested)
│   ├── store/                   # sqlite, migrations, retention
│   ├── features/                # daily feature extraction (versioned)
│   ├── score/                   # baseline, strain, forecast, model inference
│   ├── api/                     # local HTTP/datagram server
│   └── ui/                      # tray + embedded dashboard assets
├── extensions/
│   ├── gnome-shell/             # Wayland window/pointer bridge
│   ├── kwin/
│   └── browser/                 # Phase 3
├── research/                    # Python/JAX notebooks, synthetic data, model export
├── packaging/                   # goreleaser, cask, nfpm, wix, launchd/systemd units
└── docs/                        # this document + ADRs
```

The `attention`, `sessionize`, `features` and `score` packages are **pure
functions over event streams**. Recorded event traces can be replayed through
them in tests, so tuning doesn't depend on any particular OS.

---

## 12. Roadmap

| Phase | Scope | Exit criteria |
|-------|-------|---------------|
| **0: Spike (2–3 wks)** | macOS + Windows focus/idle/pointer collectors, zsh hook, Claude Code hooks, SQLite, `burnout status` | One developer's full day captured correctly on a 2-monitor setup; manual timeline matches reality within 10% |
| **1: MVP (6–8 wks)** | Attention engine v1, human-segment split, all shells, Linux X11 + GNOME, daily features, heuristic strain, dashboard, weekly check-in, Homebrew/winget/deb | 10–20 alpha users run it for 4 weeks; < 1% CPU; no shell regressions |
| **2: Personalised** | Trend forecast, per-user learned model from check-ins, KDE/wlroots, nudges | Strain correlates with CBI (target ρ ≥ 0.4 within-user) on alpha cohort |
| **3: Browser** | Extension + native messaging, tab-level attention | — |
| **4: Research** | Opt-in donation, population priors, survival model; optional E2E sync | Ethics/privacy review done before any data collection |

---

## 13. Trade-offs and what to revisit

| Decision | Trade-off | Revisit when |
|----------|-----------|--------------|
| Heuristic attention model | Explainable and deterministic, but the weights are hand-tuned | Enough check-ins and "what were you doing?" spot-labels exist to fit the weights |
| Hooks over process scanning | Accurate and cheap, but requires install and misses unhooked shells | Users report gaps; add a process-tree sampler as a fallback |
| Pure-Go inference | Simple shipping, but limited model families | A model clearly beats the linear/Bayesian baseline and needs a heavier runtime |
| Local-only | Maximum trust, but no cross-device view and no population learning | Users ask for multi-device; do E2E sync, never plaintext cloud |
| Single binary | Easy to distribute, but platform code is mixed with core | Platform glue grows large; split collectors into helper processes |
| Weekly CBI labels | Validated and cheap, but sparse | Add daily micro-check-ins if label density limits the model |

---

## 14. Open questions

1. **"jev":** confirm what was meant (JAX? a specific library?). The design
   assumes Python research → pure-Go inference either way.
2. **Audience:** developers only at first, or knowledge workers generally?
   This changes how much effort goes into terminal/agent features versus
   comms/meeting features.
3. **Business model:** open-source core plus a paid tier? It affects
   whether sync and population models are ever server-side.
4. **Calendar integration:** meetings are a big load factor. Reading
   calendar *metadata* (busy blocks only) via local CalDAV/ICS or the OS
   calendar is cheap and high-value. Should it go in Phase 1?
