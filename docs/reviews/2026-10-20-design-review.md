# Design Review: System Design v0.1 + ADR-0001…0006

**Date:** 2026-10-20
**Scope:** `docs/system-design.md`, `docs/adr/0001`–`0006`
**Lenses:** architecture, product, systems, SRE, security, privacy, correctness,
performance and scale, maintainability.
**Status:** **Resolved in design v0.2** (same day). See the
[resolution table](#9-resolution-v02) at the end. Three items are proposed
defaults that need maintainer confirmation.

**Original verdict:** **Not ready to build against yet.** The direction is sound: local-first,
the attention model, hooks, and statistical scoring with local classification.
But the docs contradict themselves on encryption and window titles. Several
privacy and security mechanisms are asserted rather than specified, and
there are correctness holes in the agent-session and scoring logic that
would make the headline numbers wrong. None of this needs a rethink. All
of it should be resolved in the docs before Phase 0 code hardens around it.

Severity: **B** = blocker (fix before Phase 0 code) · **H** = high (fix
before alpha) · **M** = medium (fix before 1.0) · **L** = low / hygiene.

---

## 0. Top 10 to resolve first

| # | Sev | Finding | § |
|---|-----|---------|---|
| 1 | B | "Encrypted at rest" is claimed, but the chosen driver (`modernc.org/sqlite`) can't encrypt. There is no threat model to say what encryption should protect against | 3.1 |
| 2 | B | No **data inventory** (every field: source, default on/off, stored form, retention). Privacy is "utmost", but nothing pins down what's collected | 2.1 |
| 3 | B | Shell hooks would pass the **full command line as argv** to `burnout emit`. That puts secrets in `ps` output and in the process table, which is the exact leak §4.2 says it prevents | 3.4 |
| 4 | B | `UserPromptSubmit`/`SessionStart` hook **stdout is injected into Claude's context**. The hook must never print, and the contract must say so | 4.2 |
| 5 | B | Window titles are "off by default" (§9) but are also the browser fallback (§4.4). Hashes of titles are dictionary-reversible. Decide the default and use keyed HMAC everywhere | 2.2 |
| 6 | B | **Parallel agent sessions** (common today) break the single human/agent timeline: human time gets double-counted across sessions | 4.1 |
| 7 | H | The GNOME Shell extension exposes the window list on the **session D-Bus**, so every app on the desktop can read it. That's a new privacy leak we'd be introducing | 3.6 |
| 8 | H | Scoring is **purely relative**. A user who is already overloaded in their 14-day calibration gets a "normal" baseline. Plus MAD = 0 → divide-by-zero for zero-inflated features | 4.4 |
| 9 | H | Background **audio** keeps presence at 1.0 indefinitely, so the user leaves with Spotify playing and "works" all night | 4.3 |
| 10 | H | Updater and installer integrity: cosign verification needs the `cosign` binary (contradicts "no requirements"). `.deb`/`.msi` installs can't self-update without root. There is no downgrade protection | 3.3 |

---

## 1. Architecture and maintainability

**1.1 (H) The dependency rule isn't stated.** §11 lists packages but not
which may import which. Without that, platform code leaks into the domain.
Recommend ports and adapters, enforced in CI with `go-arch-lint` or a
`depguard` rule:

```
domain (attention, sessionize, features, score, classify-rules)  ← imports nothing below
   ▲ ports: EventSource, Store, Classifier, Clock, KeyStore
adapters (collector/*, store/sqlite, classify/laya, api, ui, integrations/*)
   ▲
cmd/burnout (wiring only)
```

Define interfaces in the consuming package, inject `Clock` everywhere (no
`time.Now()` in the domain) and keep the domain free of goroutines. That's
what makes the "replay traces" promise in §11 actually work.

**1.2 (H) Event and ingest contracts aren't versioned.** The datagram format
from `emit`/`hook`, the GNOME extension's D-Bus API and the sidecar's
stdin/stdout protocol are all cross-process contracts that will skew across
versions. A user updates the binary while old shells, an old extension and an
old Claude Code are still running. Needed:
- an explicit `v` field and a schema file (JSON Schema or protobuf);
- daemon accepts N-1;
- unknown fields ignored, malformed messages dropped and counted;
- contract tests against recorded payloads.

**1.3 (H) No config model.** Core hours, role, retention, opt-ins,
exclusions and the ICS URL are mentioned all over the doc but never
defined. Specify:
- one TOML file with a schema, validation and defaults in code;
- secrets (ICS URL, HMAC key) live in the keystore, not the file;
- hot reload or `burnout config set`;
- every privacy opt-in is a config key, shown in `burnout doctor`.

**1.4 (M) Event payloads use untyped JSON blobs.** `raw_events.payload`,
`intervals.attrs` and `daily_features.features` are all JSON. That's fine
for storage, but the Go side should decode into typed structs at the
boundary. Use a **feature registry** where each feature is a declared unit
with a name, version, unit, direction (higher = worse?), role weights, a doc
string and a pure compute function. Then docs, schema version and code can't
drift apart.

**1.5 (M) "Single binary" isn't true any more.** It's actually the binary,
the sidecar, the GNOME extension, the KWin script, shell hook scripts and
eventually the browser extension. Name them as **components with independent
versions and a compatibility matrix**, so ADR-0001's claim stays honest.

**1.6 (M) Native collectors can crash the daemon.** A segfault in cgo or AX
code or a COM call can't be caught by `recover()`. ADR-0001's "revisit:
split collectors into helper processes" should become a crash-loop policy
now: after K crashes, start in **safe mode** without the last-enabled
collector, and report it in `doctor`. Process isolation for native
collectors stays a Phase 2 option.

**1.7 (L) Doc hygiene:**
- F-numbers are out of order (F10 after F12).
- The repo layout still says "Python/JAX notebooks", but ADR-0004 dropped JAX.
- ADR-0005 still says "with a browser extension later" in Context.
- The daemon is called both `burnoutd` and `burnout daemon`.
- NFR "Platforms" lists KDE/wlroots, but those are Phase 2.
- Add a glossary: attention, presence, segment, interval, strain, logical day.

---

## 2. Privacy (the product's core promise)

**2.1 (B) Write a data inventory and make it the source of truth.** One
table in the doc, mirrored by `burnout privacy` in the CLI and the
dashboard:

| Field | Source | Default | Stored as | Retention | Leaves device? |
|-------|--------|---------|-----------|-----------|----------------|
| app id | OS | on | bundle id / exe name | 180 d | never |
| window title | OS | **?** | category + HMAC | — | never |
| keystroke count | OS | off | per-minute count | 30 d | never |
| pointer position | OS | on | **never persisted** | — | — |
| argv0 / subcmd | shell | on | clear text | 180 d | never |
| cwd | shell | on | HMAC(project root) | 180 d | never |
| … | | | | | |

Add a **canary test** in CI: inject secret-looking strings into window
titles, argv, cwd, Claude Code payloads, ICS events and check-in text, run a
replay, then grep the DB file bytes, WAL, logs and exports. The test fails if
any canary appears. This is the single most valuable privacy test.

**2.2 (B) Window titles: decide, then be consistent.**
- §9 says titles are off by default; §4.4 relies on titles for browser coverage.
  On macOS titles also require the Screen Recording permission, which many
  users will decline. State the default and what the browser looks like
  without titles (it's just "browser").
- "Store category + hash" is **not private**. Titles have low entropy ("Inbox
  (3) – Gmail"), so a plain hash can be dictionary-reversed. Use **HMAC-SHA256
  with a per-install key held in the keystore** for every identity-bearing
  hash (`cwd_hash`, `window_key`, title). Also ask whether the hash is
  needed at all. If only the cache uses it, keep it in memory.
- Define "redacted-ish" in §4.6 precisely: strip emails, numbers, URLs and
  file paths before titles reach Laya.

**2.3 (H) Missing user privacy controls:**
- **Pause** (1 h / until tomorrow) and **private hours**.
- **Per-app/domain exclusions**, recorded as a `private` category so time
  totals stay correct but identity is gone.
- A default **sensitive-apps denylist** (password managers, banking,
  health, dating) → `private`.
- A "delete today / this hour" action.

These are table-stakes for anything that watches the screen.

**2.4 (H) Coercive-use risk (stalkerware / employer monitoring).** A
local, silent activity tracker can be installed *on* someone. Write the
mitigations as requirements, not intentions:
- The tray icon is always visible while collecting; there is no headless or
  silent install flag.
- No remote access; the API is local-user only.
- First run must be completed interactively by the logged-in user.
- A periodic "this is what's being collected" reminder.

Also state this publicly in the README.

**2.5 (H) Stated retention isn't enforceable as designed.** SQLite `DELETE`
doesn't remove bytes from the file or WAL. Use `secure_delete=ON` (or
incremental vacuum after TTL jobs), checkpoint the WAL, and make
`forget`/`uninstall --purge` also remove backups, spools, logs, sidecar
caches and exports the app created. `daily_features` "forever" includes a
sleep-window proxy, which is sensitive, so retention must be configurable.

**2.6 (H) Location of the data directory.** It must be non-synced and
per-user: `%LOCALAPPDATA%` on Windows (not Documents, which OneDrive Known
Folder Move syncs), `~/Library/Application Support` on macOS (it does get
backed up to Time Machine, so document that), and `$XDG_DATA_HOME` on Linux.
Directories `0700`, files `0600`.

**2.7 (H) Hidden network calls and secrets.**
- An ICS "secret address" is a **bearer credential to the full calendar**,
  including titles and attendees. Store it in the keystore, accept HTTPS only,
  discard everything except DTSTART/DTEND/TRANSP/STATUS while parsing, and
  never log it.
- ICS fetch, update checks and `model pull` are the only network calls. List
  them in one place, make each opt-in, and show them in `doctor`.

**2.8 (H) Fallback paths read content.**
- The Claude transcript fallback (`~/.claude/projects/**/*.jsonl`) streams full
  prompts and responses through our process.
- With `OTEL_LOG_USER_PROMPTS`, Claude Code's OTLP events can carry prompt
  text.
- Either cut both from Phase 1 or specify field-level allowlists in the
  parser: decode timestamps and types only, and drop the rest without
  buffering.

**2.9 (H) `export --research` isn't anonymous.** Longitudinal weekly
feature vectors plus CBI scores are health-related data (GDPR Art. 9
special category) and can be re-identified. Before Phase 4:
- coarsen and bucket features, drop exact timestamps, require k-anonymity
  thresholds;
- show the user a preview of exactly what will be shared;
- write a consent text, and get IRB/ethics review if a university is involved;
- the maintainers who publish a dataset become data controllers, which needs
  a policy.

**2.10 (M) Leaks during screen sharing.** The dashboard and nudges can show
up during screen share or presentations. Suppress nudges when Do Not Disturb
/ Focus mode is on or a screen share is active. "Blur sensitive numbers" is a
dashboard option.

**2.11 (M) App identifiers are sensitive in clear text.** Bundle IDs reveal
health, dating or religious apps. The denylist in 2.3 covers most of this.
Also offer an "aggregate-only" mode: after the day closes, store categories
only.

---

## 3. Security

**3.1 (B) Threat model and encryption at rest.** Add a short STRIDE-style
threat model with explicit in-scope and out-of-scope adversaries:

| Adversary | In scope? | Mitigation |
|-----------|-----------|------------|
| Other local OS users | yes | 0700/0600, socket/pipe ACLs, peer-cred checks |
| Websites in the user's browser (localhost probing, DNS rebinding, CSRF) | yes | see 3.2 |
| Backups / cloud sync of home dir / stolen powered-off laptop | yes | non-synced dir + app-level encryption or reliance on FDE (decide) |
| Supply chain (deps, CI, release assets, model weights) | yes | see 3.5 |
| Malware running as the same user | **no**, state this honestly | can read anything we can; don't over-promise |
| Coercive installer | yes | see 2.4 |

Then **decide encryption honestly**. `modernc.org/sqlite` has no encryption,
and SQLCipher needs cgo on all platforms. Options:

- **(a)** Rely on OS full-disk encryption, `doctor` warns when FileVault,
  BitLocker or LUKS is off, and drop the "encrypted at rest" claim.
- **(b)** App-level AES-GCM on sensitive columns (`attrs`, `features`,
  `checkins`) with a keystore-held key.
- **(c)** SQLCipher with cgo everywhere.

Recommend **(a) + (b)**. Also cover Linux without Secret Service (i3/sway,
headless): fall back to a key file at `0600` and warn.

**3.2 (B) The localhost dashboard design is attackable.**
- A token in the URL leaks via history, `Referer` and screenshots. Use a
  **one-time token** exchanged for an `HttpOnly; SameSite=Strict` cookie, then
  redirect.
- **Validate the `Host` header** (allow `127.0.0.1:<port>` only). That blocks
  DNS rebinding.
- No CORS, a strict CSP, `X-Frame-Options: DENY`, and CSRF protection on
  POSTs.
- Prefer binding to the UDS/named pipe and opening the dashboard through a
  tiny native webview later.

**3.3 (H) Update and install integrity:**
- `install.sh` claims signature verification, but `cosign` isn't installed
  on users' machines. Checking a SHA-256 from the same release only proves
  integrity, not authenticity. Fix: sign `checksums.txt` with a **pinned
  Ed25519 / ECDSA key** whose public half is embedded in `install.sh`,
  `install.ps1` and the binary, and verify with `openssl` (present on
  macOS/Linux) or .NET (Windows). Keep cosign keyless signing *as well*, for
  people who want transparency-log verification.
- `burnout update` must reject versions ≤ the current one unless asked
  (`--allow-downgrade`), so it can't be rolled back to a vulnerable release.
  It should pin the expected release workflow identity, and keep the previous
  binary for rollback.
- Installs from **`.deb`/`.rpm`/`.msi` live in root-owned paths**, so
  self-update would need privilege escalation. Specify that `update` on
  package installs downloads the new package and asks the user to install it
  (or prompts for elevation), while tarball/script installs go to a
  user-owned prefix (`~/.local/bin`, `%LOCALAPPDATA%\Programs`) and swap
  in place.

**3.4 (B) Shell hooks leak the command line.** If the hook runs
`burnout emit "$1" &!`, the full command (with secrets) appears in the
emitter's argv. Every local user can see that via `ps`/`/proc`, and process
auditing (EDR, auditd) logs it. Fix:
- extract `argv0` and the allowlisted subcommand **inside the shell function**;
- pass only those, via stdin or env;
- in PowerShell, write to the pipe in-process with
  `System.IO.Pipes.NamedPipeClientStream`; never spawn.

Add a test that greps `ps` output during a hooked command.

**3.5 (H) Supply chain and repo security:**
- Pin GitHub Actions by commit SHA, use least-privilege `permissions:`, and
  use OIDC for signing.
- Protect branches and tags; require signed tags; require 2FA for maintainers.
- Run `govulncheck` and `golangci-lint` in CI, plus Renovate/Dependabot.
- Reproducible builds (`-trimpath`, `-buildvcs`, `SOURCE_DATE_EPOCH`).
- An SBOM is already planned; add the OpenSSF Scorecard.
- A `SECURITY.md` with a private disclosure channel (GitHub private
  vulnerability reporting).
- ONNX Runtime in the sidecar is a native dependency with its own CVEs, so
  track it explicitly.

**3.6 (H) The GNOME Shell extension and KWin script widen exposure.** A
D-Bus service on the session bus that returns window titles, geometry and
pointer position can be read by *every* process in the session. Fix: a
**peer-to-peer D-Bus connection** or a private socket, created on request by
the agent, with the extension checking the caller's PID/executable (or a
shared token handed over at startup). Return only the fields we need.

**3.7 (H) macOS self-signed signing key → TCC escalation path.** The local
code-signing key in the login keychain is usable by any process running as
the user. Malware could sign itself with our bundle ID and certificate and
**inherit our Accessibility/Screen Recording/Input Monitoring grants**. Fix:
- create the key with an access-control list restricted to `/usr/bin/codesign`,
  requiring a user prompt per use;
- re-sign only during user-initiated install/update;
- document the residual risk.

The yabai docs, which this design cites, accept the same trade-off; we should
state it explicitly.

**3.8 (H) Local IPC hardening:**
- Unix socket goes in a `0700` directory (`$XDG_RUNTIME_DIR`,
  `~/Library/Application Support/<app>/run`).
- Check peer credentials (`SO_PEERCRED` / `LOCAL_PEERCRED`).
- Windows named pipe gets an explicit DACL restricted to the user's SID
  (named pipes don't restrict access unless you set one), plus
  `PIPE_REJECT_REMOTE_CLIENTS`.
- Every ingest message is size-capped, rate-limited, strictly parsed, and
  **fuzzed** (`go test -fuzz`).

**3.9 (H) The input-counting API is a keylogger API.** `SetWindowsHookEx`
low-level hooks and `CGEventTap` are how keyloggers work. AV/EDR will flag
us, and on Windows a slow LL-hook callback adds latency to *all* system
input; Windows silently removes the hook after a timeout. Recommend:
- **Windows Raw Input** (`RegisterRawInputDevices` + `RIDEV_INPUTSINK`),
  counted on a dedicated locked OS thread. It adds no latency and has a lower
  EDR profile.
- macOS: a listen-only tap on a dedicated thread with a trivial callback.
- Never read key codes. Make this structurally impossible: the callback
  increments a counter and nothing else.

**3.10 (M) Laya sidecar sandbox.** It parses a downloaded model and receives
titles. Run it with no network and a minimal filesystem where the OS
allows (Linux Landlock/seccomp, Windows job object + low integrity, macOS
App Sandbox profile on the helper), and make it short-lived.

**3.11 (M) Modifying users' dotfiles and `~/.claude/settings.json`.**
- Merge, never overwrite. Make a timestamped backup and keep the change
  idempotent, using a marked block (`# >>> burnout >>>`).
- Preserve existing hooks, and handle managed/enterprise and project-level
  Claude settings.
- Validate the JSON before writing, and write atomically.
- `uninstall-hooks` restores exactly.

---

## 4. Correctness

**4.1 (B) Concurrent agent sessions and the "one human" invariant.**
Developers now run several Claude Code sessions (and subagents or
background agents) at once. The current model creates a human segment per
session, so 3 idle sessions turn into 3× "reading" time. Model it as:
- **one global human-state timeline**: reading / composing / approving /
  elsewhere / away, attributed to at most one session at a time, namely the
  one whose terminal holds attention;
- **N agent-state timelines**: working / blocked / idle, which may overlap.

Features then become "agent-parallelism" and "time blocked on me", and
nothing is double-counted. Add a property test: Σ human sub-states ≤ wall time.

**4.2 (B) Hook semantics in Claude Code.**
- **Stdout:** for `UserPromptSubmit` and `SessionStart`, plain stdout is
  added to Claude's context. Our hook must write nothing to stdout, ever,
  and exit 0. A stray `fmt.Println` is a prompt-injection bug in every
  user's sessions.
- **Interrupts:** a turn the user cancels (Esc) may not fire `Stop`. There is
  also a separate `StopFailure` event for errors. Close open agent segments
  on the next `UserPromptSubmit`, `SessionEnd`, `StopFailure`, or after an
  inactivity timeout.
- **Approvals:** "approving" ends at `PostToolUse` in the current timeline,
  but that event fires *after the tool has run*, so tool runtime gets counted
  as human approval time. Use the `PermissionRequest` event (newer versions)
  for the start, and the first `PreToolUse`/`PostToolUse`/terminal input after
  it as an upper bound. Version-detect the available events.
- **Cost:** `PreToolUse`/`PostToolUse` fire on every tool call, so spawning a
  Go binary twice per call adds up in long agent loops. Register these as
  **async** hooks where supported, and budget < 10 ms p99 per invocation.
- **Terminal mapping:** the hook payload carries no tty. Walk the hook
  process's parents to find the `claude` process and its controlling
  terminal.
- **Remote:** Claude Code over SSH, in devcontainers or on the web runs hooks
  where no daemon is listening. Detect it and drop silently (already
  fail-open). Say explicitly that it's out of scope.

**4.3 (H) Attention and presence edge cases:**
- **Audio:** only **video in a visible window** or an **active call (mic in
  use)** should keep presence at 1.0. Audio-only media must not.
- **Off-screen attention:** softmax always assigns all presence to some
  window, even when the user is reading paper or a phone. Make that
  explicit, or add an "unattributed" bucket when no window has a signal
  above a threshold.
- **Scale sensitivity:** raw keystroke and scroll counts inside softmax make
  the weights depend on typing speed. Normalise each signal to [0,1]
  (saturating), and set a temperature.
- **Accessibility users:** keyboard-only, voice control, switch access and
  screen-reader users produce very different input patterns. Presence
  detection must not penalise them; add a profile or calibration and test
  with these modes.
- **Spaces and virtual desktops:** clamshell mode, macOS Spaces, Windows
  virtual desktops (cloaked windows), full-screen apps and remote desktop
  (RDP/Citrix/VM = one window). List what each yields.

**4.4 (H) Scoring statistics:**
- **MAD = 0** for zero-inflated features (weekend minutes, failure loops) →
  infinite z. Use a floor (`max(MAD, ε_f)` per feature), or model those
  features as rates or counts.
- **Purely relative scoring misses chronic overload.** If the calibration
  period is already a crunch, the baseline is wrong, and "best 28-day window"
  is undefined at day 14. Add **absolute guardrails** next to relative
  z-scores. Two examples: ≥ 55 h/week, which WHO/ILO evidence ties to health
  risk; and < 7 h between last and first activity. Seed the anchor from them.
- **Day-of-week seasonality:** compare weekdays to weekdays and weekends
  to weekends.
- **Missing vs. zero:** a day with the device off, or work done on another
  machine, is *not* rest. Track **coverage** (hours the agent ran and the
  user was present) and treat low-coverage days as missing, not recovery.
  Multi-device users are common (work and personal laptop). Say so.
- **Correlated features** (hours, after-hours, last-activity) triple-count
  the same signal. Group them and cap each group's contribution, or use
  PCA/orthogonalised groups.
- **Undefined mapping:** "mapped to 0–100" isn't defined. Specify it, for
  example a logistic of the composite with documented parameters.

**4.5 (H) Validation plan is statistically unsound.** A within-user ρ ≥ 0.4
on a 4-week alpha means about 4 CBI points per user, which isn't meaningful.
Use **repeated-measures correlation (rmcorr)** across users, run the alpha
for ≥ 8 weeks, and add the daily micro-check-in as a denser label. Pre-register
the analysis in `research/`.

**4.6 (H) Defining "day" and time:**
- Define a **logical day** with a configurable rollover (default 04:00), so
  1 a.m. work counts toward the day it belongs to.
- Split intervals at the rollover; handle DST and time-zone travel by storing
  the UTC offset per interval.
- Wall-clock jumps (NTP, manual changes) and timestamps from separate hook
  processes: stamp on receipt with the daemon's monotonic clock, keep the
  emitter's time for ordering only, and reject skew beyond a threshold.

**4.7 (M) Shell coverage gaps:**
- Guard hooks to **interactive shells only** (`[[ $- == *i* ]]`), otherwise
  scripts and CI-like loops flood events.
- Coexist with other preexec users (starship, atuin, iTerm2 shell integration).
- **WSL** on Windows can't reach a Windows named pipe directly, so define a
  bridge (AF_UNIX interop or a TCP-on-loopback bridge with a token) or
  declare WSL out of scope for Phase 1. Many Windows developers live in WSL.
- Remote SSH sessions aren't captured, and that's by design. Say so.

**4.8 (M) Persistence granularity.** Attention changes on every tick,
because it decays continuously, so "persist only changes" doesn't bound
volume. Persist **per-minute rollups per window** (`attended_s`,
`focused_s`, `visible_s`) and never persist 1 Hz ticks. Then state exactly
what goes into `raw_events`, and confirm nothing un-redacted ever lands
there. Classification must happen *before* persistence.

**4.9 (M) Laya fit to the budget.** A ~421M-parameter model on CPU likely
needs hundreds of ms per item and ≥ 1 GB RAM. That's fine only if the
sidecar is **separate from the 60 MB budget, on-demand, batched, on AC
power, capped per day, and exits when done**. Write those numbers down as
the sidecar's own NFRs.

---

## 5. Performance and scale

"Scale" for a local agent means **long-lived installs, heavy users and a
growing contributor base**, not QPS. None of these is sized today.

**5.1 (H) Per-collector budgets and event-driven design.**
`CGWindowListCopyWindowInfo` and `EnumWindows` at 1 Hz are not free. Specify
the cadence per signal:
- focus: event;
- window geometry: event (AXObserver / `EVENT_OBJECT_LOCATIONCHANGE`,
  filtered and debounced) plus a 5 s reconcile;
- pointer: 1 Hz;
- idle: 5 s.

Stop sampling when locked or idle, and reduce it on battery.

**5.2 (H) Energy.** On macOS, opt out of App Nap for the sampling thread
(`NSProcessInfo.beginActivity`), or ticks will drift and be throttled. Use
timer coalescing and track **wakeups/s** as a budget, alongside CPU %.
Measure Energy Impact in the alpha.

**5.3 (H) Stress cases to design and test for:**
- 50+ windows, 6 monitors, 30 tmux panes;
- 10k commands/day;
- 10 parallel agent sessions with subagents;
- 2 years of data.

Add a **soak test**: a simulated 7-day trace replayed at 100× speed, with
assertions on RSS, DB size, query latency for the dashboard (< 100 ms for a
90-day view) and feature-pipeline time.

**5.4 (M) SQLite operational settings:**
- one writer goroutine; batched transactions (flush every 10–30 s or N rows);
- `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout`, prepared
  statements;
- `auto_vacuum=INCREMENTAL` with a nightly incremental vacuum after TTL
  deletes, and periodic `wal_checkpoint(TRUNCATE)`;
- indexes on `(kind, start_ts)` and `(category, start_ts)`.

The feature pipeline should be incremental per logical day, not a full
recompute each hour.

**5.5 (M) Go runtime.** Set `GOMEMLIMIT`. Keep cgo callbacks on locked OS
threads, and keep GC-sensitive work out of input callbacks. Profile
allocations per tick (target: zero-alloc steady state in the attention
engine).

**5.6 (M) Shell hook cost.** Go binary startup is about 2–10 ms (slower on
macOS because of code-signature checks). That's fine backgrounded on
zsh/bash/fish. PowerShell must not spawn a process (`Start-Process` is
50 ms+); use an in-process pipe write. Benchmark per shell and OS in CI.

---

## 6. SRE: reliability, operability, supportability

**6.1 (H) Define local SLIs/SLOs, and show them to users.** Without them,
"correct" can't be measured:
- **Coverage:** % of present time attributed. Target ≥ 98%.
- **Hook delivery:** % of hook invocations ingested. Target ≥ 99.9%.
- **Drops:** events dropped per day.
- **Daemon availability:** % of logged-in time running.
- **Ingest latency:** p99.

Show a "data quality" badge in the dashboard, and refuse to score days below
a coverage threshold. That ties into 4.4.

**6.2 (H) Failure modes need explicit behaviour:**
- **Disk full:** stop raw ingestion, keep rollups, alert once.
- **DB corruption:** run `PRAGMA quick_check` at start. On failure, restore
  from the nightly `VACUUM INTO` backup (encrypted, 3 generations) and
  quarantine the bad file.
- **Migration failure:** back up before migrating, wrap each migration in a
  transaction, refuse to start on a newer schema than the binary knows
  (downgrade safety).
- **Crash loops:** systemd `Restart=on-failure` + `StartLimitBurst`, launchd
  `ThrottleInterval`, safe mode (1.6).
- **Watchdog:** systemd `WatchdogSec` with `sd_notify`, and an internal
  heartbeat per collector.

**6.3 (H) Supportability without telemetry.** Specify `burnout doctor
--bundle`: a **redacted** diagnostic archive (versions, collector states,
permission states, SLIs, the last N log lines) that the user previews before
attaching to an issue. Logs use structured `slog` with size-capped rotation,
and are PII-free *by construction*. Wrap sensitive values in a
`Sensitive[T]` type whose `String()`/`LogValue()` return `[redacted]`, so a
careless log line can't leak.

**6.4 (M) Release engineering without a server:**
- a **beta channel** (GitHub pre-releases, `update --channel beta`);
- `update --to vX` for rollback;
- a semver policy;
- a data-schema compatibility promise;
- release notes generated from conventional commits.

Make it explicit that version N reads data written by N-2.

**6.5 (M) Platform support policy.** **Windows 10 reached end of support in
October 2025.** Target Windows 11, with 10 best-effort. Support the last 3
macOS versions. For Linux, pick named distros/DEs with CI coverage (Ubuntu
LTS GNOME, Fedora KDE, Arch + Sway best-effort).

**6.6 (M) CI test matrix.**
- Platforms: macOS, Windows, and Linux under Xvfb (X11) and headless
  Mutter/Weston (Wayland).
- Shell versions: bash 3.2 (the macOS default!), bash 5, zsh, fish 3,
  PowerShell 7.
- Recorded payloads per Claude Code version.
- Migration tests from every released schema.
- Fuzzing, the canary privacy test (2.1), property tests (Σ attention ≤
  presence ≤ 1; segments non-overlapping per timeline), and the soak test.

---

## 7. Product

**7.1 (H) The score has no "so what".** The doc specifies measurement in
detail but not **intervention**. Define the core loop: weekly review →
1–2 concrete suggestions tied to the top drivers → user-set boundaries
(shutdown time, no-weekend rule, deep-work blocks) → track adherence. Also
state a **design principle**: the tool must not become a stressor. No streaks,
no guilt copy, no red badges at 11 p.m.

**7.2 (H) Safety.** If check-in scores are in the high band, or free text
suggests distress, show supportive copy and regional mental-health resources.
Never deliver a "you'll burn out in 3 weeks" message without context and
options. Don't show forecasts while the confidence interval spans zero slope.
Have a mental-health professional review all copy before alpha.

**7.3 (H) Onboarding and permission UX is where users drop off.** macOS can
ask for three separate permissions. Specify:
- what works with **zero permissions** (app focus, idle, pointer, hooks);
- what each permission adds, asked just-in-time with an explanation;
- the role pick and core hours;
- an explanation of the 14-day calibration so the empty score isn't a let-down.

**7.4 (H) Context annotations.** Students have exam weeks; developers have
releases and on-call; PMs have planning cycles. Let users tag periods
("on-call", "exam week", "vacation", "sick"). The model can then treat
expected crunch differently, exclude vacations from the baseline, and say
"this week was planned crunch; recovery matters next week".

**7.5 (M) Product success metrics without telemetry:**
- calibration completion;
- check-in completion rate;
- 4/8-week retention;
- whether users changed a boundary after a nudge.

Collect them via opt-in in-app surveys and alpha interviews, and define them
now so the alpha has exit criteria beyond CPU and regressions.

**7.6 (M) Portability.**
- New laptop = lost baseline today. Add `burnout backup`/`restore`
  (encrypted), with import that merges.
- For multi-device users before sync exists, import another machine's daily
  features.

**7.7 (M) Accessibility and i18n.**
- The dashboard meets WCAG 2.2 AA.
- Localise copy.
- Use only **validated translations** of CBI per language, because the
  audience is global and so is Laya's multilingual variant.
- Handle locales for day and week start (Sunday vs. Monday).

**7.8 (L) Regulatory wording.** Software "intended for prediction or
prognosis of disease" can count as a medical device (EU MDR Rule 11; FDA
outside the general-wellness policy). Burnout being an occupational
phenomenon in ICD-11, not a disease, helps. Even so:
- position the tool as **wellness / self-reflection**;
- avoid "predict burnout" in marketing; prefer "spot early warning signs in
  your work patterns";
- keep the disclaimer.

---

## 8. Recommended doc changes (checklist)

**Before Phase 0 code:**
- [x] Add §9.1 threat model + §9.2 data inventory; decide encryption (3.1) and title default (2.2)
- [x] Specify keyed HMAC everywhere; shell-side argv0 extraction (3.4); hook stdout rule (4.2)
- [x] Rewrite §4.3 timeline for one-human/N-agents (4.1), interrupts, PermissionRequest
- [x] Add dependency rule + versioned contracts + config model (1.1–1.3)
- [x] Fix localhost dashboard auth (3.2) and IPC ACLs (3.8)

**Before alpha:**
- [x] Scoring fixes: MAD floor, absolute guardrails, weekday seasonality, coverage/missing, correlated groups, 0–100 mapping (4.4)
- [x] Validation plan with rmcorr, 8+ weeks (4.5); logical day (4.6)
- [x] Privacy controls: pause, exclusions, sensitive denylist, delete-recent (2.3); anti-stalkerware requirements (2.4); secure deletion (2.5)
- [x] Updater/installer integrity + package-install update path (3.3); supply-chain hardening (3.5)
- [x] D-Bus exposure fix (3.6); macOS signing-key ACL (3.7); Raw Input on Windows (3.9)
- [x] SLIs + coverage badge, failure-mode runbook, `doctor --bundle`, `Sensitive[T]` logging (6.1–6.3)
- [x] Intervention loop, safety copy, onboarding/permissions, annotations (7.1–7.4)
- [x] Per-collector cadence, energy, stress/soak tests (5.1–5.3)

**Before 1.0:** the remaining M/L items.

---

## 9. Resolution (v0.2)

SD = [system design](../system-design.md), PS = [privacy & security](../privacy-and-security.md), ADRs in [`docs/adr/`](../adr/).

| # | Finding | Resolved in |
|---|---------|-------------|
| 1.1 | Dependency rule | SD §3.2; ADR-0001 |
| 1.2 | Versioned contracts | SD §3.3; ADR-0001 |
| 1.3 | Config model | SD §3.4; ADR-0001 |
| 1.4 | Typed payloads / feature registry | SD §3.5; ADR-0001 |
| 1.5 | Components + compat matrix | SD §3.1; ADR-0001 |
| 1.6 | Native crash → safe mode | SD §11.2; ADR-0001 |
| 1.7 | Doc hygiene + glossary | SD §2.1 (F-numbers), §18 glossary; JAX and `burnoutd` removed; ADR-0005 context |
| 2.1 | Data inventory + canary test | PS §3, §12; SD §14 |
| 2.2 | Title default + keyed HMAC + redaction | PS §3.1, §4.1, §4.5; SD §4.6; ADR-0006 |
| 2.3 | Privacy controls | PS §5.1; SD F13 |
| 2.4 | Anti-coercion | PS §5.2 (AC-1…AC-6); SD §8.1, §12 |
| 2.5 | Real deletion / retention | PS §7.2; SD §6; ADR-0007 |
| 2.6 | Data directory locations | PS §7.3; SD §6; ADR-0007 |
| 2.7 | Network calls + ICS secret | PS §6, §4.4; SD §4.7, §12 |
| 2.8 | Transcript / OTLP fallbacks | Removed from Phase 1: SD §4.3; ADR-0003 Option D |
| 2.9 | Research export | PS §11.1; ADR-0004; SD §7.4 |
| 2.10 | Screen-share leaks | PS §5.1; SD §8.3 |
| 2.11 | Sensitive app ids | PS §5.1 (denylist, aggregate-only) |
| 3.1 | Threat model + encryption | PS §2, §7.1; ADR-0007 (FDE + column AES-GCM) |
| 3.2 | Dashboard auth | PS §8.2; SD §9 |
| 3.3 | Installer/updater integrity | PS §9.1–9.2; ADR-0005; SD §13 |
| 3.4 | Shell argv leak | PS §4.2; SD §4.2; ADR-0003; ps-leak test PS §12 |
| 3.5 | Supply chain | PS §9.4 |
| 3.6 | D-Bus exposure | PS §8.3; SD §4.1; ADR-0002 |
| 3.7 | macOS signing key ACL | PS §9.3; ADR-0005 |
| 3.8 | IPC hardening | PS §8.1; SD §9 |
| 3.9 | Keylogger APIs | PS §8.6; SD §4.1; ADR-0002 |
| 3.10 | Sidecar sandbox | PS §8.4; ADR-0006 |
| 3.11 | Dotfile/settings edits | PS §8.5; SD §4.2–4.3; ADR-0003 |
| 4.1 | One human / N agents | SD §4.3; ADR-0003 |
| 4.2 | Hook semantics (stdout, interrupts, approvals, async, tty, remote) | SD §4.3; ADR-0003; PS §4.3 |
| 4.3 | Attention edge cases | SD §4.1; ADR-0002 |
| 4.4 | Scoring statistics | SD §7.1–7.2; ADR-0004 |
| 4.5 | Validation plan | SD §7.5; ADR-0004 |
| 4.6 | Logical day / time | SD §5 |
| 4.7 | Shell coverage (interactive, coexistence, WSL, SSH) | SD §4.2; ADR-0003 |
| 4.8 | Persistence granularity | SD §4.1, §6; ADR-0002 |
| 4.9 | Laya budget | SD §2.2, §10; ADR-0006; PS §8.4 |
| 5.1 | Collector cadence | SD §4.1; ADR-0002 |
| 5.2 | Energy / App Nap | SD §2.2, §4.1; ADR-0002 |
| 5.3 | Stress + soak | SD §10, §14 |
| 5.4 | SQLite settings | SD §6 |
| 5.5 | Go runtime | SD §10 |
| 5.6 | Shell hook cost | SD §2.2, §4.2; ADR-0003 |
| 6.1 | SLIs/SLOs | SD §11.1 |
| 6.2 | Failure modes | SD §11.2 |
| 6.3 | Supportability | SD §11.3; PS §10 |
| 6.4 | Release engineering | SD §11.4; ADR-0005 |
| 6.5 | Platform policy | SD §2.2; ADR-0005 |
| 6.6 | CI test matrix | SD §14; PS §12 |
| 7.1 | Intervention loop + principle | SD §1, §8.2; F15 |
| 7.2 | Safety | SD §8.4; F16 |
| 7.3 | Onboarding + permissions | SD §8.1, §2.3 (zero-permission mode) |
| 7.4 | Annotations | SD §8.2, §6, §7.2; F14 |
| 7.5 | Success metrics | SD §8.6 |
| 7.6 | Portability | SD F17; ADR-0007 (passphrase-wrapped key) |
| 7.7 | Accessibility + i18n | SD §2.2, §8.5, §4.1 (input profiles) |
| 7.8 | Regulatory wording | SD §1; PS §11.2 |

**Decisions made while resolving**, which go beyond the review's wording:
- Input activity counts are **opt-in on every platform** and **never persisted**. They exist only as in-memory 5 s counters for the attention engine.
- Agent session ids are stored as HMACs, so our data doesn't link to transcript files on disk.
- Hook processes don't compute HMACs; the daemon does it on receipt. That keeps keystore access out of the hot path, and raw paths only cross the same-user socket.
- An egress CI test enforces that only the three opt-in calls can reach the network.
- `raw_events.payload` is encrypted along with the other sensitive columns.

**Proposed defaults awaiting maintainer confirmation** (SD §19):
1. FDE + column encryption (ADR-0007).
2. Window titles off by default, with opt-in title classification.
3. The anti-coercion requirements AC-1…AC-6.
