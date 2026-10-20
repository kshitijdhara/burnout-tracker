# ADR-0005: Packaging and distribution via GitHub Releases

**Status:** Proposed
**Date:** 2026-10-20
**Deciders:** Project maintainers

## Context

The agent ships to macOS, Windows and Linux, with a browser extension later.
It needs OS permissions (macOS TCC: Accessibility, Screen Recording for window
titles, optional Input Monitoring), autostart, and a stable identity for
those permissions. The project is open source and non-commercial, with no
server.

The first draft distributed through Homebrew, winget/Scoop and Linux package
repositories. The maintainers have decided instead to **build the packages
and serve them only from this GitHub repository**.

## Decision

**GoReleaser** builds every artifact from a tagged commit in GitHub Actions
and publishes it to **GitHub Releases**. No external package registries.

| Platform | Artifacts on the release | Notes |
|----------|--------------------------|-------|
| macOS | `.dmg` with signed + notarized universal `.app`; `.tar.gz` of the bare CLI | TCC permissions attach to the bundle ID. Built on macOS runners (cgo) |
| Linux | `.deb`, `.rpm` (nfpm), `.tar.gz` (amd64/arm64) | Packages include the systemd user unit and install the GNOME Shell extension + KWin script |
| Windows | `.msi` (WiX) and `.zip` (amd64/arm64) | Authenticode-signed when possible; MSI registers the logon task |
| All | `checksums.txt`, a Sigstore (cosign) signature over it, GitHub artifact attestations (SLSA provenance), SBOM | Users and the updater verify before running anything |
| Optional | `burnout-classify` sidecar per OS (ONNX Runtime bundled) | Separate asset; see ADR-0006 |

**Install paths:**

- `install.sh` (macOS/Linux) and `install.ps1` (Windows) in the repo. Each
  one detects OS and architecture, downloads the matching asset from the
  latest release, **verifies the checksum and signature**, installs it, and
  runs `burnout service install`. The README tells people to read the
  script before piping it to a shell, and shows the manual steps.
- Manual: download from the Releases page, then open the `.dmg`/`.msi` or
  run `sudo dpkg -i` / `sudo rpm -i`.

**The app does what a package manager would otherwise do:**

- `burnout service install|uninstall`: launchd LaunchAgent, systemd `--user`
  unit, Windows logon task. It's idempotent, and the `.deb`/`.rpm`/`.msi`
  call it on install.
- `burnout install-hooks` / `install-browser` (native-messaging manifests).
- `burnout update`: **manual, or opt-in automatic.** It queries the GitHub
  Releases API, downloads the asset, verifies the cosign signature and
  checksum, then swaps the binary atomically (rename on Unix; staged replace
  on next start on Windows). The default is an *opt-in* weekly check, because
  the app makes no network calls unless asked (local-first).
- `burnout uninstall`: removes the service, hooks, extension manifests and,
  with `--purge`, the data.

**Laya weights** (ADR-0006) are mirrored as assets on a dedicated
`models-*` release in this repo. Apache-2.0 permits redistribution;
we include the LICENSE/NOTICE. `burnout model pull laya` fetches them
pinned by SHA-256, so we don't depend on a third-party model host.
Files have to stay under GitHub's 2 GiB per-asset limit, which the ONNX
builds do.

## Options Considered

### Option A: Package registries (Homebrew cask/formula, winget, Scoop, apt/rpm repos, AUR)
| Dimension | Assessment |
|-----------|------------|
| Discoverability | High |
| Updates | Free, via the package manager |
| Maintenance | High: tap repo, winget manifests PR per release, repo signing keys, AUR PKGBUILD |
| Policy exposure | Homebrew Gatekeeper rules for casks, winget review |

### Option B: GitHub Releases only (chosen)
| Dimension | Assessment |
|-----------|------------|
| Discoverability | Lower: users come via the repo |
| Updates | We own them (`burnout update`) |
| Maintenance | Low: one pipeline, one place |
| Policy exposure | None beyond OS code-signing rules |
| Trust | Good: provenance is visible next to the source, with signed checksums and attestations |

### Option C: Flatpak/Snap on Linux
Rejected: the sandbox blocks window/pointer/idle access and process-tree
walking.

## Trade-off Analysis

B removes most of the release overhead and every third-party gatekeeper. It
fits a small, non-commercial project, and having the source and binaries side
by side is a trust signal for a monitoring app. In exchange, we take on
updates, service registration and uninstall, which a package manager would
otherwise handle. Those are well-understood problems, and they are needed
anyway for hooks and browser manifests.

**OS signing is still needed**, because the browser quarantines downloaded
files just as Homebrew does:

- **macOS:** an unsigned `.app` downloaded from GitHub is blocked by
  Gatekeeper. It can be opened via System Settings → Privacy & Security →
  "Open Anyway", or with `xattr -d com.apple.quarantine`. TCC grants are also
  fragile for unsigned or ad-hoc-signed bundles, since they reset whenever the
  binary hash changes. So notarization (Apple Developer account, $99/yr) is
  strongly recommended; without it, every update re-prompts for permissions.
  The `install.sh` path avoids the quarantine flag, because `curl` doesn't set
  it, but the TCC problem remains.
- **Windows:** unsigned `.msi`/`.exe` get SmartScreen warnings. SignPath
  Foundation provides free Authenticode signing for OSS projects.
- **Linux:** no OS gate. Packages are verified via cosign/checksums.

## Consequences

- Easier: one release pipeline, no registry accounts or review queues,
  full control over release timing, clear provenance.
- Harder: we maintain the updater, the install scripts and service
  registration; there's less discoverability; Linux users don't get updates
  through `apt upgrade` or `dnf`.
- Revisit: adding a Homebrew tap or winget manifest later is cheap with
  GoReleaser (a few config lines) if users ask for it. Nothing in this design
  blocks that.

## Action Items
1. [ ] `.goreleaser.yaml`: darwin (cgo, macOS runner, universal, dmg), linux (nfpm deb/rpm, tar.gz), windows (msi, zip); checksums, cosign keyless signing, SBOM.
2. [ ] GitHub Actions release workflow on `v*` tags, with `actions/attest-build-provenance`.
3. [ ] `install.sh` / `install.ps1` with checksum + signature verification.
4. [ ] `burnout service`, `update`, `uninstall` subcommands.
5. [ ] launchd plist, systemd user unit, Windows logon task templates in `packaging/`.
6. [ ] Apple Developer ID + notarization in CI; apply to SignPath for Windows.
7. [ ] `models-laya-<ver>` release with mirrored weights + LICENSE/NOTICE + SHA-256.
