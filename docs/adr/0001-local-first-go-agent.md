# ADR-0001: Local-first, single-binary Go agent with SQLite

**Status:** Proposed
**Date:** 2026-10-20
**Deciders:** Project maintainers

## Context

The app runs all day on users' machines, collects sensitive behavioural data
and must ship to macOS, Windows and Linux through package managers. The
constraints are a small team, a strict resource budget (< 1% CPU, < 60 MB RAM)
and a trust-first product.

## Decision

Build one Go binary (`burnout`) that runs as a user-level daemon and also
provides the hot-path subcommands (`emit`, `hook`) and the CLI. Store all
data locally in SQLite. No data leaves the device by default.

## Options Considered

### Option A: Go single binary + SQLite (chosen)
| Dimension | Assessment |
|-----------|------------|
| Complexity | Low–Med (cgo on macOS for AppKit/CoreGraphics) |
| Cost | No infrastructure |
| Scalability | Per-device. Volume is tiny (~1–3 MB/day raw) |
| Team familiarity | High (chosen language) |

**Pros:** static binaries, easy cross-compilation for Linux/Windows, low memory, good
concurrency for collectors, `go:embed` for UI assets and hook scripts.
**Cons:** desktop UI options in Go are weaker. macOS needs cgo/ObjC glue.

### Option B: Rust agent (ActivityWatch-style watchers) + Tauri UI
**Pros:** smaller binaries, strong desktop/FFI story, Tauri UI.
**Cons:** not the team's language; slower iteration.

### Option C: Electron app
**Pros:** fastest UI development, easy browser-extension sharing.
**Cons:** 150 MB+ RAM, breaks the resource budget, and hard to justify for
an always-on agent.

### Option D: Cloud backend from day one
**Pros:** cross-device view, population learning.
**Cons:** this data is too sensitive to start in the cloud. It adds backend
cost, compliance work and a trust hurdle.

## Trade-off Analysis

The agent is mostly OS glue plus stream processing, which plays to Go's
strengths. UI quality matters less in v1: a tray icon plus a localhost
dashboard is enough. Local-first removes the need for a backend and is the
strongest trust signal available.

## Consequences

- Easier: distribution, privacy story, offline use, no ops.
- Harder: no cross-device aggregation, and no population model without an opt-in pipeline.
- Revisit: native window UI (Wails/Fyne) in Phase 2; E2E-encrypted sync in Phase 4.

## Action Items
1. [ ] Scaffold `cmd/burnout` with cobra-style subcommands.
2. [ ] `internal/store` with `modernc.org/sqlite`, WAL, embedded migrations.
3. [ ] Keychain-backed DB key and `0600` file permissions.
