# ADR-0001: Local-first Go agent with SQLite: one main binary plus versioned components

**Status:** Proposed
**Date:** 2026-10-20
**Deciders:** Project maintainers

## Context

The app runs all day on users' machines, collects sensitive behavioural data
and ships to macOS, Windows and Linux as packages served from GitHub
Releases (ADR-0005). The constraints are a small team, a strict resource
budget (< 1% CPU, < 60 MB RAM for the agent) and a trust-first product.

The code has to stay maintainable as platform glue grows. That means keeping
the domain logic testable without an OS, and keeping cross-process contracts
stable across versions.

## Decision

Build one main Go binary (`burnout`). It runs as a user-level daemon
(`burnout daemon`) and also provides the hot-path subcommands (`emit`,
`hook`) and the CLI. Store all data locally in SQLite; at-rest protection
is covered by ADR-0007. No data leaves the device by default.

The main binary ships with **separately versioned components**:

| Component | Role | Contract with the daemon |
|-----------|------|--------------------------|
| `burnout` | daemon, CLI, hot-path emitters | n/a |
| `burnout-classify` | optional Laya sidecar (ADR-0006) | stdin/stdout JSON |
| GNOME Shell extension | Wayland window/pointer bridge | private peer-to-peer socket |
| KWin script | KDE bridge | private peer-to-peer socket |
| Shell hook scripts | zsh/bash/fish/pwsh hooks (ADR-0003) | ingest datagram |

A **compatibility matrix** in the repo lists which component versions work
with which daemon versions. `burnout doctor` reports what's installed and
flags mismatches.

### Architecture rules

- **Ports and adapters.** Domain packages (`attention`, `sessionize`,
  `timeline`, `features`, `score`, `classify/rules`) import no adapter
  packages. They depend only on ports defined where they are used:
  `EventSource`, `Store`, `Classifier`, `Clock`, `KeyStore`. Adapters
  (`collector/*`, `store/sqlite`, `classify/laya`, `api`, `ui`,
  `integrations/*`) implement the ports. `cmd/` only wires things together.
  The rule is enforced in CI with `go-arch-lint` or a `depguard` rule.
- **Deterministic domain.** A `Clock` is injected everywhere, and the domain
  never calls `time.Now()`. Domain packages don't start goroutines.
  Concurrency lives in the adapters. That's what makes trace replay tests
  possible.
- **Versioned contracts.** Every cross-process message (ingest datagrams,
  sidecar protocol, desktop bridges) has a `v` field and a JSON Schema in
  `/schemas`. The daemon accepts the current and previous version (N-1),
  ignores unknown fields, and drops and counts malformed messages. Contract
  tests run against recorded payloads.
- **Config model.** One TOML file with a schema, defaults in code and
  validation at load. Secrets (ICS URL, HMAC key) live in the
  keystore, never in the file. Use `burnout config get/set`. Every privacy
  opt-in is a config key and is shown in `burnout doctor`.
- **Feature registry.** Each daily feature is declared once: name, version,
  unit, direction (higher = worse?), role weights, doc string and a pure
  compute function. Docs, schema version and code come from the same
  declaration.
- **Redaction by type.** Sensitive values are wrapped in `Sensitive[T]`,
  whose `String()` and `LogValue()` return `[redacted]`, so a careless log
  line or `%v` can't leak them.
- **Crash-loop safe mode.** A crash in native code (cgo, AX, COM) can't be
  caught by `recover()`. After 3 crashes in 10 minutes, the daemon starts in
  safe mode without the most recently enabled native collector and reports
  it in `doctor`. Moving native collectors into helper processes stays a
  Phase 2 option.

## Options Considered

### Option A: Go main binary + SQLite + versioned components (chosen)
| Dimension | Assessment |
|-----------|------------|
| Complexity | Low–Med (cgo on macOS for AppKit/CoreGraphics) |
| Cost | No infrastructure |
| Scalability | Per-device. Per-minute rollups keep volume small (~1–3 MB/day before TTL) |
| Team familiarity | High (chosen language) |

**Pros:** static binaries, easy cross-compilation for Linux and Windows, low
memory, good concurrency for collectors, `go:embed` for UI assets and hook
scripts.
**Cons:** desktop UI options in Go are weaker; macOS needs cgo/ObjC glue;
several components to version and test together.

### Option B: Rust agent (ActivityWatch-style watchers) + Tauri UI
**Pros:** smaller binaries, strong desktop/FFI story, Tauri UI.
**Cons:** not the team's language; slower iteration.

### Option C: Electron app
**Pros:** fastest UI development, easy browser-extension sharing.
**Cons:** 150 MB+ RAM breaks the resource budget, which is hard to justify
for an always-on agent.

### Option D: Cloud backend from day one
**Pros:** cross-device view, population learning.
**Cons:** this data is too sensitive to start in the cloud. It adds backend
cost, compliance work and a trust hurdle.

## Trade-off Analysis

The agent is mostly OS glue plus stream processing, which plays to Go's
strengths. UI quality matters less in v1: a tray icon and a localhost
dashboard are enough. Local-first removes the need for a backend and is the
strongest trust signal available.

The architecture rules cost some upfront ceremony: interfaces, schemas, a
registry. They buy a domain that can be tested without an OS, and they stop
platform code from leaking into scoring logic. They also mean version skew
between the daemon, old shells and an old desktop extension degrades
gracefully instead of corrupting data.

## Consequences

- Easier: distribution, privacy story, offline use, no ops, trace-replay
  testing, safe upgrades across component versions.
- Harder: no cross-device aggregation; no population model without an
  opt-in pipeline; schemas and the compatibility matrix need upkeep.
- Revisit: native window UI (Wails/Fyne) in Phase 2; process isolation for
  native collectors in Phase 2; E2E-encrypted sync in Phase 4.

## Action Items
1. [ ] Scaffold `cmd/burnout` with subcommands (`daemon`, `emit`, `hook`, `status`, `doctor`, `config`).
2. [ ] Ports in the domain packages; `go-arch-lint`/`depguard` rule in CI.
3. [ ] `/schemas` with JSON Schemas for every cross-process message; N-1 contract tests.
4. [ ] TOML config with schema, defaults and validation; secrets via `KeyStore`.
5. [ ] Feature registry in `internal/features`.
6. [ ] `Sensitive[T]` type and a lint check that log calls don't take raw sensitive fields.
7. [ ] Crash counter + safe mode; compatibility matrix in `docs/`.
8. [ ] `internal/store` with `modernc.org/sqlite`, WAL and embedded migrations; at-rest protection per ADR-0007.
