# ADR-0005: Packaging and distribution via GitHub Releases

**Status:** Proposed
**Date:** 2026-10-20
**Deciders:** Project maintainers

## Context

The agent ships to macOS, Windows and Linux, with a browser extension later.
It needs OS permissions (macOS TCC: Accessibility, Screen Recording for window
titles, optional Input Monitoring), autostart, and a stable identity for
those permissions. The project is open source and non-commercial, with no
server. **Everything must be free: no paid accounts, certificates or
hosting.** Users download the packages, along with anything the app needs at
runtime, and run it on their own devices.

The first draft distributed through Homebrew, winget/Scoop and Linux package
repositories. The maintainers have decided instead to **build the packages
and serve them only from this GitHub repository**.

## Decision

**GoReleaser** builds every artifact from a tagged commit in GitHub Actions
and publishes it to **GitHub Releases**. No external package registries.

| Platform | Artifacts on the release | Notes |
|----------|--------------------------|-------|
| macOS | `.dmg` with ad-hoc-signed universal `.app`; `.tar.gz` of the bare CLI | Not notarized (that needs a paid Apple account). The installer re-signs locally for stable permissions (see below). Built on macOS runners (cgo) |
| Linux | `.deb`, `.rpm` (nfpm), `.tar.gz` (amd64/arm64) | Packages include the systemd user unit and install the GNOME Shell extension + KWin script |
| Windows | `.msi` (WiX) and `.zip` (amd64/arm64) | Unsigned by default; SignPath's free OSS signing if accepted. MSI registers the logon task |
| All | `checksums.txt`, a Sigstore (cosign) signature over it, GitHub artifact attestations (SLSA provenance), SBOM | Users and the updater verify before running anything |
| Optional | `burnout-classify` sidecar per OS (ONNX Runtime bundled) | Separate asset; see ADR-0006 |

**No runtime requirements beyond the download.** The agent is a single
static binary. Everything else it needs ships as release assets in this
repo:
- the GNOME/KWin bridges, inside the Linux packages;
- the Laya sidecar, with ONNX Runtime bundled;
- the model weights.

Nothing needs to be installed from elsewhere: no Go, Python, Node or system
libraries. Building from source (`make build`, needs only the Go toolchain)
is documented as an alternative.

**Free infrastructure only:** GitHub Actions (free for public repos,
including macOS and Windows runners), GitHub Releases for hosting, Sigstore
cosign keyless signing, and GitHub artifact attestations.

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

**Code signing without paying.** Our own verification (cosign + checksums)
covers integrity. The OS-level gates are handled like this:

- **macOS, the hard one.** Without a paid Developer ID we can't notarize.
  That causes two problems:
  1. *Gatekeeper* blocks a `.dmg` downloaded in a browser. Fix: `install.sh`
     downloads with `curl`, which doesn't set the quarantine flag. For the
     `.dmg`, document System Settings → Privacy & Security → "Open Anyway".
  2. *TCC permissions reset on every update*, because an ad-hoc signature's
     identity is the binary hash. Fix: on first install, `burnout setup-signing`
     creates a **self-signed code-signing certificate in the user's own login
     keychain**. It then re-signs the `.app` with it on install and on every
     `burnout update`. The signing identity stays stable, so permissions
     (Accessibility, Screen Recording, Input Monitoring) survive updates. This
     is the established approach used by other OSS macOS tools that need
     Accessibility (for example yabai). It costs nothing; the user approves one
     keychain prompt. The cert never leaves their machine.
  - CI still ad-hoc-signs (`codesign -s -`), because Apple Silicon refuses to
    run unsigned arm64 code.
- **Windows:** unsigned `.msi`/`.exe` show a SmartScreen warning ("More info
  → Run anyway"), documented in the README with screenshots. Apply to
  **SignPath Foundation**, which signs OSS projects for free. If accepted, the
  warning goes away at no cost. `install.ps1` uses `Unblock-File` after
  verifying the cosign signature.
- **Linux:** no OS gate. Packages are verified via cosign/checksums.

## Consequences

- Easier: one release pipeline, no registry accounts or review queues,
  full control over release timing, clear provenance.
- Harder: we maintain the updater, the install scripts, service
  registration and the local re-signing step on macOS; first-run warnings on
  macOS/Windows have to be documented clearly; there's less discoverability; Linux users don't get updates
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
6. [ ] `burnout setup-signing` (macOS self-signed cert + re-sign on install/update); apply to SignPath Foundation for Windows.
7. [ ] `models-laya-<ver>` release with mirrored weights + LICENSE/NOTICE + SHA-256.
