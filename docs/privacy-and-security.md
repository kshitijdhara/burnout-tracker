# Privacy and Security

**Status:** Draft v0.3 (decisions accepted 2026-10-21)
**Date:** 2026-10-20
**Related:** [System design](system-design.md) ·
[ADR-0007 Data protection at rest](adr/0007-data-protection-at-rest.md) ·
[Design review](reviews/2026-10-20-design-review.md)

> **This document is authoritative on what is collected, how it is stored and
> where it can go.** If another doc disagrees, this one wins and the other is
> a bug.

Keywords **MUST**, **MUST NOT**, **SHOULD** and **MAY** are used as in RFC 2119.

---

## 1. Principles

1. **Local-first.** Data is collected, processed and stored only on the
   user's device. There is no server. Nothing leaves the device unless the
   user turns on one of the three opt-in network calls (§6).
2. **Minimise at the source.** If we don't need a value, we never read it.
   If we need it briefly, we discard it before persistence. If we need to
   keep it, we keep the least identifying form (category, count, HMAC).
3. **Personal, not managerial.** The tool serves the person being measured.
   It has no remote access and no team view, and it is designed to resist
   being installed *on* someone (§5.2).
4. **Honest claims.** We say what we protect against and what we don't
   (§2). We never claim protections the implementation doesn't deliver.
5. **Verifiable.** The agent is open source, and releases carry signed
   provenance. The privacy guarantees in this doc are tested in CI (§12).

---

## 2. Threat model

| # | Adversary | STRIDE | In scope | Mitigations |
|---|-----------|--------|----------|-------------|
| T1 | Other local OS users on the same machine | I, T, S | **Yes** | 0700 dirs / 0600 files (§7); socket and pipe ACLs + peer-credential checks (§8.1); no command lines in the process table (§4.2) |
| T2 | Websites in the user's browser: localhost port probing, DNS rebinding, CSRF, clickjacking | I, T, E | **Yes** | Dashboard auth via one-time token → cookie, `Host` allowlist, no CORS, CSP, frame denial, CSRF tokens (§8.2) |
| T3 | Other apps in the desktop session (session D-Bus, accessibility APIs) | I | **Yes** | No public session-bus service; private peer-to-peer bridge with token handshake (§8.3) |
| T4 | Backups, cloud sync of the home directory, a stolen powered-off device | I | **Yes** | OS full-disk encryption, checked and warned about when off (§7.1, ADR-0007); data dirs outside synced folders and excluded from Time Machine (§7.3); portable backups passphrase-encrypted |
| T5 | Supply chain: dependencies, CI, release assets, installers, model weights | T, E | **Yes** | Pinned release signing key, downgrade protection, pinned Actions, reproducible builds, SBOM, SHA-256-pinned model weights (§9) |
| T6 | Coercive installer: an employer, partner or family member installing it to watch someone | I, R | **Yes** | Anti-coercion requirements (§5.2): always-visible indicator, no silent install, interactive first run, periodic reminders, no remote access |
| T7 | Malicious input to the agent from same-user processes (forged hook events, oversized messages) | T, D | **Yes** (integrity of our own data, availability) | Strict parsing, size caps, rate limits, fuzzing (§8.1). Forged events can skew the user's own stats; they can't escalate |
| T8 | Malware running as the same user | all | **No** | Same-user malware can read anything the agent can, including keystore items it is allowed to unlock. We say so plainly and don't claim otherwise. We limit what is *worth* stealing (§3) and avoid making it easier (§8.6, §9.3) |
| T9 | Root/administrator or a compromised OS | all | **No** | Out of scope for any user-level application |

---

## 3. Data inventory

This is **the canonical list**. A datum that isn't in this table MUST NOT
be collected. Adding a row requires a doc change reviewed like code.

Defaults: **on** = collected after first-run consent; **opt-in** = off until
the user enables it; **never** = not collected or not persisted at all.
Retention values are defaults; all are configurable downwards (§5.1).
Everything stored is in the local database, which is readable only by the
user's account and protected at rest by OS full-disk encryption
(ADR-0007).

### 3.1 Desktop activity

| Field | Source | Default | Stored as | Retention | Leaves device? |
|-------|--------|---------|-----------|-----------|----------------|
| App identifier (bundle id / exe name) | OS window APIs | on | Clear text in minute rollups. Apps on the sensitive denylist or the user's exclusions are stored as `private` (identity dropped) | 180 d | Never |
| Window identity (`window_key`) | OS window APIs | on | HMAC-SHA256 (per-install key, §4.5) of app id + OS window id. Used to join a window's minutes; not reversible to a title | 180 d | Never |
| Window title | OS window APIs (macOS: needs Screen Recording) | **opt-in** ("title classification") | **Never persisted.** Redacted (§4.1), classified in memory, discarded. Title hashes are never persisted; the classification cache is in memory only, keyed by HMAC | — | Never |
| Activity category | Classifier output | on | Clear text label (e.g. `code`, `comms`, `browser`, `private`) | 180 d (rollups), 2 y (as daily features) | Never |
| Monitor topology | OS display APIs | on | Count, resolutions, arrangement, primary flag, snapshot on change | 180 d | Never |
| Focus / visibility / attention rollups | Attention engine | on | Per minute per window: `focused_s`, `visible_s`, `attended_s`, monitor index | 180 d | Never |
| Pointer position | OS pointer API, 1 Hz | on (in memory) | **Never persisted.** Used only within the current tick to find the hovered window | — | Never |
| Attention ticks (1 Hz) | Attention engine | on (in memory) | **Never persisted.** Only minute rollups are written | — | Never |
| Input activity counts (keys, clicks, scroll) | Windows Raw Input; macOS listen-only event tap (Input Monitoring); X11 XInput2 | **opt-in** | **Never persisted.** Rolling 5 s counters in memory feed the attention engine only. Key codes are **never read** (§8.6). Without this opt-in, attention uses idle-time deltas | — | Never |
| Idle / lock / sleep state | OS idle and session APIs | on | State-change intervals | 180 d | Never |
| Media / call state | macOS: which app captures audio (CoreAudio process objects), which app keeps the display awake (IOKit assertions), all without a permission. Other platforms: not yet | on | Which app is in a call or playing video is used in memory only, to hold presence and attribute attention. Persisted only as the reason presence was held (`input` / `video` / `call`). No audio, video, track, title or URL is ever read. Cameras are not queried: on older macOS that would load third-party camera plugins into the agent | 180 d | Never |

### 3.2 Terminal

| Field | Source | Default | Stored as | Retention | Leaves device? |
|-------|--------|---------|-----------|-----------|----------------|
| Command name (`argv0`) | Shell hook (extracted in the shell, §4.2) | on (after `install-hooks`) | Clear text, after alias resolution | 180 d | Never |
| Subcommand | Shell hook | on | Clear text, **only** for allowlisted tools (git, kubectl, docker, npm, go, cargo, terraform, …); otherwise absent | 180 d | Never |
| Full command line / arguments | — | **never** | Not read by the agent. Per-tool opt-in for more detail MAY be added later, and only with a new row here | — | Never |
| Exit code | Shell hook | on | Integer | 180 d | Never |
| Start/end times, duration | Shell hook + daemon receipt time | on | Timestamps + UTC offset | 180 d | Never |
| Working directory / project | Shell hook | on | HMAC-SHA256 of the project root (§4.5). The raw path is never stored | 180 d | Never |
| TTY / pane ids, shell PID | Shell hook env | on (in memory) | **Never persisted.** Used only to attribute a command to a terminal window | — | Never |
| `ssh` flag | Shell hook | on | Bool (command is an outbound ssh) | 180 d | Never |

### 3.3 AI-agent sessions (Claude Code)

| Field | Source | Default | Stored as | Retention | Leaves device? |
|-------|--------|---------|-----------|-----------|----------------|
| Session id | Hook payload `session_id` | on (after `install-hooks --claude`) | HMAC-SHA256 (it would otherwise link our data to transcript files on disk) | 180 d | Never |
| Hook event type | Hook payload `hook_event_name` | on | Enum (`UserPromptSubmit`, `Stop`, `PermissionRequest`, …) | 7 d (raw events), then folded into timelines | Never |
| Tool name | Hook payload `tool_name` | on | Clear text (`Bash`, `Edit`, `Read`, …) | 180 d (as counts per turn) | Never |
| Project | Hook payload `cwd` | on | HMAC-SHA256 of project root, same key as shell | 180 d | Never |
| Agent timeline states | Derived | on | Intervals: `working` / `blocked_on_human` / `idle` per session | 180 d | Never |
| Human timeline states | Derived (attention + hooks) | on | Intervals: `reading` / `composing` / `approving` / `elsewhere` / `away` / `unattributed` | 180 d | Never |
| Prompt and response text, tool inputs/outputs, transcript contents | — | **never** | Not read. The hook drops these fields at parse time (§4.3). The transcript-tailing fallback and OTLP receiver are removed | — | Never |

### 3.4 Calendar

| Field | Source | Default | Stored as | Retention | Leaves device? |
|-------|--------|---------|-----------|-----------|----------------|
| ICS URL | User input | **opt-in** | OS keystore only, never in config files or logs | Until removed | Sent only to its own host (§6) |
| Busy blocks | ICS feed | opt-in | Start, end, busy/free, status (confirmed/tentative/cancelled). No title, attendees, location or description | 180 d | Never |

### 3.5 Self-report and derived data

| Field | Source | Default | Stored as | Retention | Leaves device? |
|-------|--------|---------|-----------|-----------|----------------|
| Check-in answers (CBI items, daily sliders) | User | on (prompted; skippable) | Item answers and computed score | 2 y | Never |
| Check-in free text | User | **opt-in**, requires the Laya classifier | **Never persisted.** Scored in memory against fixed questions; only the scores are stored | — | Never |
| Annotations (on-call, exam week, vacation, sick, …) | User | on (user-created) | Tag + period; optional short note | 2 y | Never |
| Core hours, role, settings | User | on | Config file (no secrets) | Until changed | Never |
| Daily features | Feature pipeline | on | JSON per logical day | 2 y (configurable) | Never (Phase 4 research export only on explicit request, §11) |
| Scores, components, forecasts | Scorer | on | Numbers and driver labels | 2 y (configurable) | Never |

### 3.6 Operational data

| Field | Source | Default | Stored as | Retention | Leaves device? |
|-------|--------|---------|-----------|-----------|----------------|
| Logs | Agent | on | Structured `slog`, PII-free by construction (§10); the home folder is shown as `~`. Crash output (panics, failed starts) goes to `logs/stderr.log` on macOS and the user journal on Linux | Size-capped rotation (default 3 × 10 MB); `stderr.log` rotated at 1 MB on each start; the journal follows the system's settings | Never |
| Health metrics (SLIs: coverage, drops, hook delivery, seconds of presence held by calls, video and confirmed idle time) | Agent | on | Counters per day | 180 d | Never |
| Diagnostics bundle | `burnout doctor --bundle` | on demand | Redacted archive at a user-chosen path, previewed before writing (§10) | User-managed | Only if the user attaches it somewhere |
| Local recovery copies | Nightly `VACUUM INTO` | on | Database copies, 3 generations, in the data dir (same permissions) | 3 nights | Never |
| Portable backup archive | `burnout backup` | on demand | Passphrase-encrypted archive (Argon2id + AES-256-GCM) at a user-chosen path | User-managed | Only where the user puts it |
| Exports | `burnout export` | on demand | Plaintext JSON or CSV at a user-chosen path, owner-only; the data folder keeps only a list of export paths and times, so `forget` and `privacy` can point at them | User-managed; a path leaves the list once its file is gone | Only where the user puts it |
| Spool (events buffered while the DB is busy) | Agent | on | Same redaction as raw events | Until flushed (minutes) | Never |
| Raw events | Collectors / hooks | on | Only already-redacted events (classification and redaction happen before persistence) | 7 d | Never |

---

## 4. Redaction rules

Redaction happens **at the earliest point possible**, ideally in the
process that produces the data, and always before persistence.

### 4.1 Window titles (when title classification is on)

1. Read the title into memory. It MUST NOT be logged, persisted, hashed into
   storage or sent anywhere.
2. Redact before any classifier sees it: remove email addresses, URLs, file
   paths, numbers of 3+ digits, and tokens ≥ 20 characters that look like
   identifiers or secrets (high entropy, base64/hex).
3. Classify (rules → user overrides → in-memory cache → optional Laya).
4. Keep only the category. The in-memory cache is keyed by HMAC of the
   redacted title and is cleared on restart.

### 4.2 Shell commands

1. The hook function extracts `argv0` (after alias resolution) and, for
   allowlisted tools, the subcommand **inside the shell**.
2. Only those values, plus exit code, times, tty/pane ids and the
   working directory, are passed to the emitter, via **environment variables
   or stdin**. The command line MUST NOT appear in the emitter's argv, so it
   never appears in `ps`, `/proc` or process-audit logs.
3. The daemon HMACs the project root on receipt, before anything is
   persisted or logged. The raw path only crosses the same-user socket
   (§8.1). That keeps keystore access out of the hot-path hook process.
4. PowerShell writes to the named pipe in-process (`NamedPipeClientStream`)
   and never spawns a process.
5. Hooks run only in interactive shells.

### 4.3 Claude Code hook payloads

The hook parser uses a **field allowlist**. It decodes only `session_id`,
`hook_event_name`, `tool_name` and `cwd`, and it MUST skip all other fields
without buffering them (`prompt`, `tool_input`, `tool_response`,
`transcript_path`, `last_assistant_message`, …). The daemon HMACs
`session_id` and `cwd` on receipt, before persistence. The hook MUST write nothing to
stdout, because for some events Claude Code adds hook stdout to the
model's context.

### 4.4 ICS feeds

The parser keeps only `DTSTART`, `DTEND`/`DURATION`, `TRANSP` and
`STATUS`. For expanding repeats it also uses `RRULE`, `EXDATE`,
`RECURRENCE-ID` and `UID`; the UID is held in memory only, to match a moved
instance to its series, and is never stored.

It drops `SUMMARY`, `DESCRIPTION`, `LOCATION`, `ATTENDEE`, `ORGANIZER` and
everything else while streaming, without holding the full feed in memory.
A canary test confirms none of them reach the database.

### 4.5 Identity hashing

Every identity hash (project root, window key, session id, in-memory
classifier cache key) is **HMAC-SHA256 with a per-install 256-bit key** held
in the OS keystore. A plain hash is never used, because low-entropy inputs
(paths, titles) can be reversed by guessing. Rotating the key (`burnout keys
rotate`) produces new identities, so history stops joining across the
rotation. That is documented as the price of rotation.

---

## 5. User controls

### 5.1 Controls

The agent MUST provide:

| Control | Behaviour |
|---------|-----------|
| **Pause** | 1 hour / until tomorrow / until resumed. While paused, collectors stop. Hooks still run but the daemon discards their events. The tray shows the paused state |
| **Private hours** | Recurring windows (e.g. evenings) during which nothing is collected |
| **Exclusions** | Per app and per domain (when known). Time is kept as category `private`; identity is dropped |
| **Sensitive-apps denylist** | On by default: password managers, banking, health, dating and similar apps are recorded as `private` |
| **Delete recent** | "Delete last hour" and "Delete today", one click from the tray |
| **Forget** | `burnout forget --since <date>` / `--all`. Also removes backups, spools, logs, caches and exports created by the app (§7.2) |
| **Aggregate-only mode** | Keep only category rollups and daily features; drop per-app/per-window detail. New minutes are recorded per category; days recorded before the mode was on are reduced at the next nightly maintenance |
| **Retention** | Every retention period in §3 is configurable downwards |
| **Export** | `burnout export` (JSON/CSV) with a warning that the export is plaintext |
| **Privacy view** | `burnout privacy` and a dashboard page render §3 with the user's current settings and what is enabled |
| **Screen-share safety** | Nudges are suppressed in Do Not Disturb / Focus mode or while a screen share is active; the dashboard has a "blur numbers" option |

Phase 1 so far:
- **Pause** is built: `burnout pause [DURATION|tomorrow]` and `resume`.
  - The daemon checks a pause file in the data folder every second.
  - While paused it samples nothing, discards hook events, and drops
    the paused span from agent sessions that stay open.
  - An unreadable pause file counts as paused.
- **Delete recent and Forget** are built: `burnout forget --last D |
  --today | --since DATE | --all`. A running daemon does the deleting:
  1. It writes out what it holds in memory.
  2. It deletes rows from then on and the derived rows of those days,
     plus every recovery and pre-migration copy made since then.
  3. It overwrites freed pages and truncates the WAL.
  - Annotations, consent and hooks are kept.
  - Logs aren't touched: they hold no activity data (§10).
- **Privacy view** is built: `burnout privacy`.
- **Sensitive-apps denylist** is built for desktop apps: password
  managers, finance and tax apps, crypto wallets and a few health apps.
  Banking, health and dating mostly happen in a browser, where only the
  window title says what a page is; those sites are covered when opt-in
  title classification arrives.
- **The config file** is built (system design §3.4): `burnout config
  get|set|add|remove|unset|validate`. It carries:
  - **private hours** (`privacy.private_hours`): the daemon treats them
    exactly as a pause;
  - **exclusions** (`privacy.exclude_apps`): per app; per domain comes
    with title classification;
  - **aggregate-only mode** (`privacy.aggregate_only`): new minutes are
    written as one row per category, with no app id or window key, and
    the nightly maintenance reduces days recorded before the mode was on.
    Terminal commands and agent sessions keep their detail: they hold
    command names and keyed session ids, not apps or windows;
  - **retention** (`retention.*`): each period can be lowered from its
    default down to a floor the scoring needs, never raised.
  - An invalid file is never half-applied. A running daemon keeps its
    current settings, and a starting one refuses to run on defaults,
    which could silently drop private hours or exclusions.
- **Export** is built: `burnout export --out FILE [--format csv]
  [--since DATE] [--until DATE]`.
  - It reads one consistent snapshot, read-only, so the daemon keeps
    running.
  - It warns that the export is plaintext before writing. Files are
    new and owner-only: it never overwrites one, and a failed export
    leaves nothing behind.
  - CSV text fields that a spreadsheet would run as a formula are
    prefixed with `'`.
  - The format is versioned (`schemas/v1/export.schema.json`).
  - Exports hold what the database holds: app ids, command names,
    category minutes, features, scores, check-in answers and
    annotations. Keyed hashes stay opaque.
  - Exports live wherever the user saved them, so `forget` can't delete
    them. The data folder keeps a list of export paths (no contents), and
    `forget` and `privacy` show the ones still on disk.
- **Calendar** is built: `burnout calendar add|sync|status|remove`.
  - The feed URL is kept in the OS keystore only. It is never in config,
    logs, errors or the sync status file, which names only the host.
  - The ICS fetch is the only network call so far. `doctor` and `privacy`
    show it.
  - `calendar.ics.enabled = false` stops it.
  - No fetch happens while paused, and meetings in private hours are
    dropped.
  - Busy blocks are intervals: `forget` deletes them, and later syncs
    never re-import meetings from before the last forget.
  - `calendar remove --forget` deletes everything stored from the feed.
- **Dashboard:** its Privacy page shows the same settings as
  `burnout privacy`, from the same code. "Blur numbers" covers screen
  sharing.
- **The tray** is built (`internal/adapters/tray`): a menu-bar icon on
  macOS, a notification-area icon on Windows and Linux. The daemon shows
  it. Its menu has:
  - a first line saying whether anything is recorded: "Recording",
    "Paused until 15:30" or "Private hours: nothing is recorded". The icon
    changes too;
  - pause for an hour, until tomorrow or until resumed, and resume;
  - "Delete the last hour" and "Delete today", one click each. They go
    through the daemon like `burnout forget`, and the first line says
    whether they worked;
  - "Open dashboard" (§8.2).
  - There is no hide or quit item (§5.2).

### 5.2 Anti-coercion requirements

A local activity tracker can be installed on someone else's machine to watch
them. These are requirements, not intentions:

1. **AC-1** While any collector is active, a tray/menubar indicator MUST be
   visible. There MUST be no option to hide it while collecting.
2. **AC-2** There MUST be no silent, headless or unattended install flag.
   Installers MUST NOT start collection on their own.
3. **AC-3** Collection MUST NOT start until the logged-in user has finished
   the first-run flow interactively, including reading the data summary
   (§3) and consenting.
4. **AC-4** The agent MUST show a "here's what's being collected" reminder
   at least monthly, with one-click access to pause, settings and uninstall.
5. **AC-5** The agent MUST NOT offer remote access of any kind: no network
   listener, no remote export, no sync to another person's account. Local IPC
   is restricted to the same OS user (§8.1).
6. **AC-6** The README and the first-run screen MUST state that the tool is
   for self-use only and must not be used to monitor others.

AC-1 in Phase 1:
- The daemon shows the icon before it collects anything. Shell and Claude
  Code hooks count as collectors too: if no icon can be shown, the daemon
  records nothing and exits, and its log says why:
  - macOS: no login session at the screen, e.g. started over ssh;
  - Linux: the desktop has no tray (GNOME needs the AppIndicator
    extension);
  - Windows: the icon didn't appear within 10 seconds.
  The background service retries every 30 s, so a tray that appears after
  login is picked up. Hook-only use without an icon (servers, ssh, GNOME
  without the extension) is therefore not supported.
- The menu has no hide or quit item. On macOS the icon can't be
  ⌘-dragged off the menu bar.
- The end-to-end test binary, built with `-tags e2e`, skips the tray.
  Release builds have no flag, setting or variable that does.

AC-4 in Phase 1:
- 30 days after consent, and 30 days after it was last opened, the tray
  menu shows "Review what's collected…" (on macOS, "Review" also appears
  beside the icon). It shows whether or not recording is paused.
- Opening it shows the dashboard's Privacy page: what is collected, the
  current settings, and how to pause, change settings and uninstall
  (`burnout uninstall`, `--purge` to delete all data too).
- When it was last opened is kept in the data folder (`reminder-seen`). If
  that or the consent date can't be read, or lies in the future, the
  reminder shows.

---

## 6. Network egress

The agent makes **exactly three kinds of outbound connection**. Each is
**opt-in**, listed in `burnout doctor`, and can be turned off with one
config key. No other code path may open a network connection. CI enforces
this by checking that only the packages below import the network client
code (§12).

| Call | Enabled by | Sends | To | Disable |
|------|-----------|-------|----|---------|
| **ICS fetch** | Adding an ICS URL | An HTTPS GET of the user's own feed URL (no cookies, no identifying headers beyond a generic User-Agent) | The calendar provider host in the URL | `burnout config set calendar.ics.enabled false` or remove the URL |
| **Update check** | `burnout update` (manual) or `update.check = "weekly"` | An HTTPS GET of the GitHub Releases API for this repository (no token, no cookies, a generic User-Agent). `burnout update` also downloads `checksums.txt`, its signature and the one asset for this platform; the weekly check reads the release list only and never downloads or installs | `api.github.com`; downloads from `github.com`, which redirects to `release-assets.githubusercontent.com` (formerly `objects.githubusercontent.com`) | `update.check = "off"` (the default); `burnout update` runs only when you type it |
| **Model pull** | `burnout model pull laya` | An HTTPS GET of a pinned asset from this repository's `models-*` release | `github.com` / `objects.githubusercontent.com` | Don't run it; delete the model with `burnout model rm` |

HTTPS only, with certificate verification. Proxies come from the OS settings.
No telemetry, crash reporting or analytics exist.

The update client (`internal/adapters/egress/update`) refuses any request,
redirects included, that isn't HTTPS to one of the four GitHub hosts in the
table, and `burnout doctor` and `burnout privacy` list them while the weekly
check is on. It trusts nothing it downloads until `checksums.txt` verifies
against a pinned release key and the asset matches its SHA-256 there
(ADR-0005).

---

## 7. Protection at rest

Full rationale in [ADR-0007](adr/0007-data-protection-at-rest.md).

### 7.1 Encryption

- **At-rest protection is OS full-disk encryption** (FileVault, BitLocker /
  Device Encryption, LUKS). The database itself is not encrypted by the app.
- Onboarding MUST check FDE status and explain the risk when it is off.
  `burnout doctor` and the dashboard MUST show a persistent warning while it
  is off. Install is not blocked.
- Owner-only permissions (§7.3) protect against other local users.
- The data directory is kept out of places FDE doesn't cover:
  - excluded from Time Machine (sticky exclusion);
  - never in OneDrive/Documents;
  - documented exclusion for Linux backup tools.
- **Portable backups** (`burnout backup`) are passphrase-encrypted
  (Argon2id + AES-256-GCM), because they're meant to leave the device.
- **Claim:** "Your data stays on your device, readable only by your user
  account. Turn on disk encryption to protect it if your device is lost or
  stolen." We do **not** claim the database is encrypted, or that anything
  protects against same-user malware (T8).
- The **HMAC key** (§4.5) lives in the OS keystore, or in a `0600` key file
  on Linux without Secret Service.

### 7.2 Retention and deletion

- `PRAGMA secure_delete=ON`, `auto_vacuum=INCREMENTAL` with a nightly
  incremental vacuum after TTL jobs, and periodic `wal_checkpoint(TRUNCATE)`,
  so deleted rows don't linger in free pages or the WAL.
- TTL jobs run nightly per §3.
- `forget` and `uninstall --purge` MUST also delete: local recovery copies, spool files,
  logs, the in-memory and on-disk caches, sidecar caches, the model (purge
  only), keystore items and any exports the app wrote to its own directory.
  Exports saved elsewhere are listed for the user to delete.

### 7.3 Locations

| OS | Data dir | Notes |
|----|----------|-------|
| macOS | `~/Library/Application Support/burnout` | Not synced by iCloud Drive; **excluded from Time Machine** (sticky `tmutil addexclusion`), since backup drives are often unencrypted |
| Windows | `%LOCALAPPDATA%\burnout` | Never under Documents (OneDrive Known Folder Move syncs it) |
| Linux | `$XDG_DATA_HOME/burnout` (default `~/.local/share/burnout`) | |

Directories MUST be `0700` (owner-only ACL on Windows), files `0600`.

---

## 8. Local attack surface

### 8.1 IPC (hooks, CLI, local API)

- **Unix socket** in a `0700` directory: `$XDG_RUNTIME_DIR/burnout/` on
  Linux, `~/Library/Application Support/burnout/run/` on macOS. The daemon
  MUST check peer credentials (`SO_PEERCRED` / `LOCAL_PEERCRED`) and reject
  other UIDs.
- **Windows named pipe** with an explicit DACL granting access only to the
  user's SID, plus `PIPE_REJECT_REMOTE_CLIENTS`.
- **Ingest:** at most 4 KiB per message, rate-limited per source, strict
  parsing against a versioned JSON Schema. Unknown fields are ignored;
  malformed messages are dropped and counted. The parser is fuzzed
  (`go test -fuzz`).

### 8.2 Dashboard

- Served on `127.0.0.1:<random port>`.
- `burnout dashboard` opens a URL with a **one-time launch token**. The
  server exchanges it for an `HttpOnly; SameSite=Strict` session cookie and
  redirects to a token-free URL. Tokens are single-use and expire in 60 s.
- **`Host` header allowlist** (`127.0.0.1:<port>`, `localhost:<port>`), which
  blocks DNS rebinding.
- No CORS headers. Strict CSP (`default-src 'self'`, no inline script),
  `X-Frame-Options: DENY`, `frame-ancestors 'none'`, and a CSRF token on
  every state-changing request.

Phase 1 implementation (`internal/adapters/dashboard`, `burnout dashboard`):
- **Lifecycle:** `burnout dashboard` runs the server for as long as the
  command runs, until Ctrl-C. The tray's "Open dashboard" starts one inside
  the daemon on first use, which runs until the daemon stops; each click
  opens it with a new launch token. Each server has its own port, and both
  read the database through a read-only connection.
- **Launch token:** 256 bits, compared in constant time, single-use, and
  valid for 60 s.
- **Session cookie:** 256 bits; `HttpOnly`, `SameSite=Strict`, `Path=/`, and
  named after the port.
- **CSRF token:** 256 bits per session, minted with the cookie. The page
  reads it from `GET /api/session`; a cross-origin page can send that
  request but never read the answer, because there are no CORS headers.
  - It is not `Secure`. The server is plain HTTP on loopback, and Safari
    refuses `Secure` cookies from `http://127.0.0.1`.
- **Every request** must:
  - carry an allowed `Host` (`127.0.0.1:<port>` or `localhost:<port>`);
  - be GET or HEAD, unless it is one of the write routes below;
  - not be marked cross-site or same-site by `Sec-Fetch-Site`. `none` (a
    typed URL) is allowed only for the page and the launch link, never the
    API.
- **Every write** (POST or DELETE) must also:
  - match a write route exactly; any other method or path is `405`;
  - be marked `Sec-Fetch-Site: same-origin`, or, from a browser without
    Fetch Metadata, carry no `Sec-Fetch-Site` and an `Origin` of this
    server. `none`, `null` and a missing `Origin` are refused;
  - carry the session's CSRF token in `X-CSRF-Token`, compared in constant
    time. A token in the URL or a body doesn't count;
  - for POST, have `Content-Type: application/json`, which an HTML form
    can't send; bodies are capped at 8 KiB and parsed strictly.
- **Responses** carry:
  - `default-src 'none'; script-src 'self'; style-src 'self'; …;
    frame-ancestors 'none'`;
  - `X-Frame-Options: DENY`, `nosniff`, `no-referrer`, `no-store`, and
    same-origin COOP/CORP;
  - no CORS headers.
- **Write routes:** `POST /api/pause` (`1h`, `tomorrow` or
  `until-resumed`), `POST /api/resume`, `DELETE /api/data?range=hour|today`,
  `POST /api/annotations`, `DELETE /api/annotations/{id}` and
  `POST /api/checkins` (the weekly CBI, every item). The page asks before
  deleting.
- **Data access:** both dashboards (the CLI's and the tray's) can change
  data, the same way the CLI does, so a button and its command can't
  disagree:
  - pause, resume and delete go through the control files the daemon's
    loop reads. While the daemon runs it does the deleting, as for
    `burnout forget`;
  - annotations and check-ins open a short-lived read-write connection, as
    `burnout annotate` and `burnout checkin` do while the daemon runs.
  Errors are logged, never shown; the page sees only validation messages
  about its own request. A high check-in score shows the same support
  message and lines as the CLI (system design §8.4).
- **Errors:** API errors are logged to the terminal, never sent to the
  page, since they can name paths.
- **The page itself:**
  - plain embedded HTML, CSS and JS: no external resources, no inline
    script or style;
  - data is inserted with `textContent` only, never parsed as HTML;
  - a test enforces all of this.

### 8.3 GNOME Shell extension and KWin script

- They MUST NOT register a public service on the session bus.
- The agent asks the extension to open a **private peer-to-peer
  connection** (private socket or p2p D-Bus) and authenticates with a token
  handshake. The extension verifies the caller.
- The bridge returns only the fields the agent needs: window geometry,
  stacking, focus, pointer position, and titles only when title
  classification is on.

### 8.4 Laya sidecar

- On-demand, batched, runs only on AC power, capped at 500 items/day, exits
  after 60 s idle. Transient RAM budget ≤ 1.5 GB.
- Sandboxed with **no network** and a minimal filesystem view: Linux
  Landlock + seccomp, Windows job object + low integrity level, macOS sandbox
  profile.
- Input is already redacted (§4.1). The model file is verified against its
  pinned SHA-256 before every load.

### 8.5 Modifying user files

`install-hooks` edits shell rc files and `~/.claude/settings.json`. It MUST:
- merge and never overwrite;
- use a marked block (`# >>> burnout >>>` … `# <<< burnout <<<`);
- take a backup first, into the app's data directory (`hook-backups/`,
  `0600`, unique names), never next to the file;
- validate JSON before writing, and write atomically (temp file + rename);
- preserve existing hooks;
- respect managed/enterprise and project-level Claude settings (never edit
  those);
- be idempotent.

`uninstall-hooks` MUST restore the files exactly, minus our block.

### 8.6 Input-counting APIs

The APIs we use to count input are the same ones keyloggers use. So:

- **Windows:** Raw Input (`RegisterRawInputDevices` with `RIDEV_INPUTSINK`)
  on a dedicated locked OS thread, **not** `SetWindowsHookEx` low-level
  hooks. It adds no system-wide input latency and has a lower EDR profile.
- **macOS:** a listen-only `CGEventTap` on a dedicated thread.
- **All platforms:** the callback MUST only increment a counter. Key codes,
  characters and modifiers are never read. This is enforced structurally:
  the callback has no access to the event payload beyond its type. Covered
  by code review rules and a unit test.

---

## 9. Supply chain and release integrity

### 9.1 Release signing

- `checksums.txt` for every release is signed with a **pinned ECDSA P-256
  release key**. The public key is embedded in `install.sh`, `install.ps1`
  and the binary, alongside a second **next** key for rotation.
- Verification uses `openssl dgst -sha256 -verify` on macOS/Linux
  (LibreSSL-compatible) and .NET `ECDsa` on Windows, so no extra tools are
  needed.
- The private key lives in a protected GitHub Actions environment with
  required reviewers.
- **Cosign keyless** signatures and GitHub build-provenance attestations are
  published as well. They're optional extra checks for users who have the
  tools.

### 9.2 Updates

- `burnout update` MUST reject versions ≤ the current one unless
  `--allow-downgrade` is passed. It keeps the previous binary for
  `update --to vX` rollback.
- Install-type aware:
  - **Package installs** (`.deb`, `.rpm`, `.msi`, root-owned) → download and
    verify the new package, then ask the user to install it or elevate.
  - **Script/tarball installs** in a user prefix (`~/.local/bin`,
    `%LOCALAPPDATA%\Programs`) → atomic swap.
- **macOS automatic updates notify only**, because re-signing needs the user
  present (§9.3).
- Beta channel = GitHub pre-releases (`update --channel beta`).

### 9.3 macOS self-signed code-signing key

- Created by `burnout setup-signing` in the login keychain, with an access
  control list restricted to `/usr/bin/codesign` and **user confirmation on
  every use**.
- Re-signing happens only during user-initiated install or update.
- **Residual risk:** same-user malware that gets the user to confirm a
  prompt could sign itself with our identity and inherit our TCC grants
  (Accessibility, Screen Recording, Input Monitoring). This is documented in
  the README.

### 9.4 Repository and build hygiene

- GitHub Actions pinned by commit SHA; least-privilege `permissions:` per
  job; OIDC for signing; no long-lived cloud credentials.
- Protected `main` and release tags; signed tags; 2FA required for
  maintainers; CODEOWNERS on `packaging/`, `.github/` and `docs/privacy-and-security.md`.
- `govulncheck` and `golangci-lint` on every PR; Renovate for dependency
  updates.
- Reproducible builds (`-trimpath`, `-buildvcs`, `SOURCE_DATE_EPOCH`), an SBOM
  per release, and an OpenSSF Scorecard on the repo.
- **Model weights:** mirrored on a `models-*` release with LICENSE/NOTICE
  and pinned by SHA-256 in the binary.
- **ONNX Runtime** (bundled in the sidecar) is tracked as an explicit native
  dependency, with CVE monitoring and a bump policy.

---

## 10. Logging and diagnostics

- Structured logging with `slog`, size-capped rotation (default 3 × 10 MB).
- **PII-free by construction.** Every sensitive value travels in a
  `Sensitive[T]` wrapper whose `String()`, `GoString()`, `Format()` and
  `LogValue()` return `[redacted]`. Code that needs the raw value calls
  `.Reveal()`, which is greppable and flagged in review.
- Never logged: window titles, command lines, paths, ICS URLs, keystore
  material, check-in content. The app's own file locations can appear in
  error messages, with the home folder shown as `~`.
- `burnout doctor --bundle FILE` builds a redacted diagnostics archive (a
  zip): the `doctor` report (version, collector and permission states,
  disk encryption, keystore, daemon and database health), today's health
  SLIs, the config file, and the last 200 lines of the daemon log. Secrets
  are never in the config. Apps in `privacy.exclude_apps` are replaced with
  `[excluded app]` everywhere in the bundle, and the home folder is shown as
  `~`. A config file that doesn't parse is left out, with its error,
  because the error can quote it. The bundle **shows the user every file in
  full** and writes nothing until they type `yes` in a terminal. It goes to a
  new owner-only file at a path they choose, never over an existing one.
  It never uploads.

---

## 11. Research data and regulatory positioning

### 11.1 Research export (Phase 4 only)

`burnout export --research` is **not built** until all of these are in
place:
- features coarsened and bucketed, with no exact timestamps (week granularity);
- a k-anonymity threshold enforced on the export;
- a preview of the exact payload, shown to the user before writing;
- consent text, reviewed for clarity;
- ethics/IRB review if a university or study is involved;
- a dataset policy, because maintainers who publish a dataset become data
  controllers for it. Burnout and check-in data is health-related, so it is
  GDPR Art. 9 special-category data.

### 11.2 Regulatory positioning

- The product is a **wellness and self-reflection tool**, not a medical
  device. It doesn't diagnose, treat or predict disease.
- Software intended for prediction or prognosis of disease can be a medical
  device (EU MDR Rule 11; FDA outside its general-wellness policy). Burnout
  is an occupational phenomenon in ICD-11, not a disease, which helps, but
  claims still matter.
- Marketing and UI copy MUST avoid "predict burnout". Use phrasing like
  "spot early warning signs in your work patterns". The disclaimer stays in
  the product.
- A local-only tool with no server means the project processes no personal
  data itself. That changes only if a research dataset is published (§11.1).

---

## 12. Verification

The privacy guarantees above are only as good as their tests. CI MUST run:

| Test | What it proves |
|------|----------------|
| **Canary test** | Secret-looking canary strings are injected into window titles, argv, cwd, Claude Code payloads (prompt, tool input), ICS fields and check-in free text. After a full trace replay, the DB file bytes, WAL, logs, exports, backups and spool are grepped. **Any hit fails the build** |
| **ps-leak test** | While a hooked command with a canary in its arguments runs, process listings (`ps`, `/proc/*/cmdline`, Windows process command lines) contain no canary. `TestPSLeak` runs the real binary, daemon, zsh, bash and fish hooks, and Claude Code hook on Linux (`/proc` command lines and environments) and macOS (`ps -E`), and fails unless it saw the hook processes. Windows: not yet |
| **Hook stdout test** | `burnout hook claude` produces zero bytes on stdout for every event type and for malformed input |
| **Fuzzing** | Ingest parser, hook payload parser, ICS parser and the bridge protocol (`go test -fuzz`, run continuously) |
| **Contract tests** | Recorded payloads per Claude Code version, per shell and per extension version parse correctly. The allowlisted fields are the only ones decoded |
| **Egress test** | Only the packages behind the three opt-in calls (§6) import the network client code |
| **Permission tests** | Data dir, files, socket and pipe have the modes/ACLs in §7.3 and §8.1, and a second OS user is rejected |
| **Deletion test** | After `forget --all` / `uninstall --purge`, the canary scan finds nothing in any app path, and the keystore items are gone |

### Vulnerability disclosure

Security issues are reported privately through **GitHub private
vulnerability reporting** on this repository. Action item: add `SECURITY.md`
with the policy, supported versions and expected response times before the
first public release.
