# ADR-0005: Packaging and distribution

**Status:** Proposed
**Date:** 2026-10-20
**Deciders:** Project maintainers

## Context

The target channels are Homebrew, Linux package managers and Windows package
managers, with a browser extension later. The agent needs OS permissions
(macOS TCC: Accessibility, Screen Recording for window titles, optional Input
Monitoring), autostart, and a stable identity for those permissions.

## Decision

Use **GoReleaser** for all channels from a tagged release:

| Platform | Artifact | Channel | Notes |
|----------|----------|---------|-------|
| macOS | Signed + notarized `.app` (universal) | Homebrew **cask** | TCC permissions attach to the bundle ID. Built on macOS CI runners (cgo) |
| Linux | static binary; `.deb`, `.rpm` (nfpm); tarball | Homebrew formula, apt/rpm repo, AUR | + GNOME Shell extension, KWin script; systemd user unit |
| Windows | signed `.exe` + MSI (WiX) | winget, Scoop | Authenticode signing avoids SmartScreen warnings; logon task for autostart |
| Browser (P3) | WebExtension | Chrome Web Store, AMO, Edge | Installer writes native-messaging host manifest |

## Options Considered

### Option A: Homebrew formula on macOS (CLI binary only)
**Cons:** macOS attributes TCC grants to the *terminal app* that launched it,
or prompts unpredictably. Background launchd agents without a bundle get
poor permission UX. Rejected for macOS (fine for Linuxbrew).

### Option B: Cask with signed .app (chosen for macOS)
**Pros:** stable permissions, login item, menubar app.
**Cons:** needs an Apple Developer ID and notarization in CI.

### Option C: Flatpak/Snap on Linux
**Cons:** the sandbox blocks window/pointer/idle access and process-tree
walking. Not worth it.

## Consequences

- Easier: one pipeline, reproducible releases, simple user install commands.
- Harder: signing secrets in CI (Apple, Windows code-signing cert), macOS runners.
- Revisit: auto-update (Sparkle-style) vs. relying on package managers. Default to package managers.

## Action Items
1. [ ] `.goreleaser.yaml` with darwin (cgo, macOS runner), linux, windows builds.
2. [ ] Homebrew tap repo (`homebrew-tap`) with cask + formula.
3. [ ] launchd plist, systemd user unit, Windows logon task templates in `packaging/`.
4. [ ] Apple Developer ID + notarization; Windows code-signing certificate.
