# ADR-0005: Packaging and distribution via GitHub Releases

**Status:** Proposed
**Date:** 2026-10-20
**Deciders:** Project maintainers

## Context

The agent ships to macOS, Windows and Linux. It needs OS permissions (macOS
TCC: Accessibility, Screen Recording for window titles, optional Input
Monitoring), autostart, and a stable identity for those permissions. The
project is open source and non-commercial, with no server. **Everything
must be free: no paid accounts, certificates or hosting.** Users download
the packages, along with anything the app needs at runtime, and run it on
their own devices.

Because the project runs no server, the installer and updater are the main
supply-chain attack surface. They have to verify authenticity, not just
integrity, using only tools already on the user's machine.

## Decision

**GoReleaser** builds every artifact from a tagged commit in GitHub Actions
and publishes it to **GitHub Releases**. No external package registries.

| Platform | Artifacts on the release | Notes |
|----------|--------------------------|-------|
| macOS | `.dmg` with ad-hoc-signed universal `.app`; `.tar.gz` of the bare CLI | Not notarized (that needs a paid Apple account). Re-signed locally for stable permissions (see below). Built on macOS runners (cgo) |
| Linux | `.deb`, `.rpm` (nfpm), `.tar.gz` (amd64/arm64) | Packages include the systemd user unit and install the GNOME Shell extension + KWin script |
| Windows | `.msi` (WiX) and `.zip` (amd64/arm64) | Unsigned by default; SignPath's free OSS signing if accepted. MSI registers the logon task |
| All | `checksums.txt` + `checksums.txt.sig` (pinned release key), cosign keyless signature, GitHub artifact attestations (SLSA provenance), SBOM | See "Release signing" |
| Optional | `burnout-classify` sidecar per OS (ONNX Runtime bundled) | Separate asset; see ADR-0006 |

**Supported platforms:**
- **Windows 11.** Windows 10 is best-effort; it has been out of support since
  October 2025.
- **macOS:** the last 3 releases.
- **Linux:** Ubuntu LTS (GNOME) and Fedora (KDE) are supported, each with CI
  coverage. Arch + Sway is best-effort.

**No runtime requirements beyond the download.** The agent is a static
binary. Everything else it needs ships as release assets in this repo:
- the GNOME/KWin bridges, inside the Linux packages;
- the Laya sidecar, with ONNX Runtime bundled;
- the model weights.

Building from source (`make build`) needs only the Go toolchain.

**Free infrastructure only:** GitHub Actions (free for public repos,
including macOS and Windows runners), GitHub Releases for hosting, Sigstore
keyless signing, and GitHub artifact attestations.

### Release signing

`checksums.txt` is signed with a **pinned ECDSA P-256 release key**.

- **Pinned public key.** It is embedded in `install.sh`, `install.ps1` and
  the `burnout` binary. Two keys are embedded, *current* and *next*, so the
  key can be rotated without breaking existing installs.
- **Verification uses tools users already have.** macOS and Linux use
  `openssl dgst -sha256 -verify` (works with OpenSSL and the LibreSSL that
  ships with macOS). Windows uses .NET's `ECDsa`. That gives authenticity,
  not just integrity, with no extra tools installed.
- **The private key** lives in a protected GitHub Actions environment with
  required reviewers. Only the tag-triggered release workflow can use it.
- **Cosign keyless signatures and GitHub attestations** are published as
  well, as an *additional* check for anyone who wants transparency-log
  verification. They're not required, because `cosign` isn't on users'
  machines.

### Install paths

- **`install.sh` (macOS/Linux) and `install.ps1` (Windows).** Each one:
  1. detects the OS and architecture;
  2. downloads the matching asset plus `checksums.txt` and its signature;
  3. **verifies the signature against the pinned key**, then the checksum;
  4. installs to a **user-owned prefix** (`~/.local/bin`,
     `%LOCALAPPDATA%\Programs\burnout`);
  5. runs `burnout service install`.

  The README tells people to read the script before piping it to a shell,
  and shows the manual verification steps.
- **Manual:** download from the Releases page, then open the `.dmg`/`.msi` or
  run `sudo dpkg -i` / `sudo rpm -i`. These install to system paths.

### What the app does instead of a package manager

- **`burnout service install|uninstall`** sets up the launchd LaunchAgent,
  the systemd `--user` unit or the Windows logon task. It's idempotent, and
  the `.deb`/`.rpm`/`.msi` call it on install.
- **`burnout install-hooks`** installs the shell and Claude Code hooks
  (ADR-0003).
- **`burnout update`** is manual, or an opt-in weekly check (local-first: no
  network calls unless asked). It:
  - verifies the pinned-key signature and checksum before touching anything;
  - **rejects versions ≤ the current one** unless `--allow-downgrade` is
    given, so it can't be rolled back to a vulnerable release;
  - **keeps the previous binary**, so `burnout update --to vX` can roll back;
  - offers a **beta channel** of GitHub pre-releases
    (`--channel beta`).

  What it does depends on how the app was installed:

  | Install type | What `update` does |
  |--------------|--------------------|
  | Script/tarball (user prefix) | downloads, verifies, swaps the binary atomically (rename on Unix; staged replace on next start on Windows) |
  | `.deb` / `.rpm` / `.msi` (root-owned paths) | downloads and verifies the new package, then asks the user to install it (or prompts for elevation). It never writes to system paths itself |
  | macOS `.app` | automatic updates **only notify**. Re-signing needs the user present (see below), so the update applies when they run `burnout update` |

- **`burnout uninstall`** removes the service, hooks and extension
  manifests. With `--purge` it also removes all data (ADR-0007).

### Release policy

- **Versioning:** semantic versioning.
- **Release notes** are generated from conventional commits.
- **Data compatibility:** version N reads data written by N-2. Migrations
  refuse to run on a schema newer than the binary knows.
- **Component compatibility:** daemon and component versions are tracked in
  the compatibility matrix (ADR-0001).

### Laya weights

Laya weights (ADR-0006) are mirrored as assets on a dedicated `models-*`
release in this repo, with LICENSE/NOTICE included. Apache-2.0 permits
redistribution. `burnout model pull laya` fetches them pinned by SHA-256,
and the hashes ship inside the signed binary. Files must stay under
GitHub's 2 GiB per-asset limit, which the ONNX builds do.

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
| Trust | Good: pinned-key signatures verifiable with built-in tools; provenance next to the source |

### Option C: Flatpak/Snap on Linux
Rejected: the sandbox blocks window/pointer/idle access and process-tree
walking.

### Option D: Checksums only, or cosign-only verification
Rejected. A checksum from the same release proves integrity, not
authenticity. Cosign-only verification needs a tool users don't have,
which contradicts "no extra requirements".

## Trade-off Analysis

B removes most of the release overhead and every third-party gatekeeper. It
fits a small, non-commercial project, and having the source and binaries side
by side is a trust signal for a monitoring app.

In exchange, we take on updates, service registration, uninstall and a
long-lived signing key. The key is the main new risk. A protected
environment, required reviewers and a pre-embedded *next* key limit the
blast radius and make rotation routine.

**Code signing without paying.** The pinned-key signature covers
authenticity. The OS-level gates are handled like this:

- **macOS, the hard one.** Without a paid Developer ID we can't notarize.
  That causes two problems:
  1. *Gatekeeper* blocks a `.dmg` downloaded in a browser. Fix: `install.sh`
     downloads with `curl`, which doesn't set the quarantine flag. For the
     `.dmg`, document System Settings → Privacy & Security → "Open Anyway".
  2. *TCC permissions reset on every update*, because an ad-hoc signature's
     identity is the binary hash. Fix: `burnout setup-signing` creates a
     **self-signed code-signing certificate in the user's own login
     keychain**, and the `.app` is re-signed with it on install and update.
     The signing identity stays stable, so permissions survive updates. This
     is the established approach used by other OSS macOS tools that need
     Accessibility (for example yabai).
     - **Hardening:** the private key gets an access-control list that only
       allows `/usr/bin/codesign` and requires user confirmation on each use.
       Re-signing happens **only during a user-initiated install or update**,
       never in the background.
     - **Residual risk, documented:** malware already running as the user
       that gets the user to approve a codesign prompt could sign itself with
       our identity and inherit our TCC grants. That's why there's no silent
       re-signing and why automatic updates only notify.
  - CI still ad-hoc-signs (`codesign -s -`), because Apple Silicon refuses to
    run unsigned arm64 code.
- **Windows:** unsigned `.msi`/`.exe` show a SmartScreen warning ("More info
  → Run anyway"), documented in the README with screenshots. Apply to
  **SignPath Foundation**, which signs OSS projects for free; if accepted,
  the warning goes away at no cost. `install.ps1` runs `Unblock-File` only
  after the pinned-key signature check passes.
- **Linux:** no OS gate. Packages are verified via the pinned-key signature.

## Consequences

- Easier: one release pipeline, no registry accounts or review queues,
  full control over release timing, verifiable provenance with built-in
  tools.
- Harder:
  - We maintain the updater, the install scripts, service registration and
    the local re-signing step on macOS.
  - We guard and rotate a release key.
  - First-run warnings on macOS/Windows have to be documented clearly.
  - There's less discoverability, and Linux users don't get updates through
    `apt upgrade` or `dnf`.
- Revisit:
  - Adding a Homebrew tap or winget manifest later is cheap with GoReleaser
    if users ask for it.
  - Sigstore verification becomes the primary check if a pure-Go verifier
    gets embedded in the binary.

## Action Items
1. [ ] `.goreleaser.yaml`: darwin (cgo, macOS runner, universal, dmg), linux (nfpm deb/rpm, tar.gz), windows (msi, zip); checksums, cosign keyless, SBOM.
2. [ ] Release workflow on `v*` tags: pinned-key signing in a protected environment with required reviewers, `actions/attest-build-provenance`, Actions pinned by SHA, least-privilege `permissions:`.
3. [ ] Generate the ECDSA P-256 *current* and *next* keys; embed both public keys; document rotation.
4. [ ] `install.sh` / `install.ps1`: pinned-key verification with `openssl` / .NET, user-prefix install, manual-steps docs.
5. [ ] `burnout update`: install-type detection, downgrade protection, previous-binary retention, `--to`, `--channel beta`, macOS notify-only.
6. [ ] `burnout service`, `uninstall` subcommands; launchd plist, systemd user unit, Windows logon task templates in `packaging/`.
7. [ ] `burnout setup-signing`: self-signed cert, key ACL restricted to `codesign` with per-use confirmation, re-sign only on user-initiated install/update.
8. [ ] Apply to SignPath Foundation for Windows.
9. [ ] CI matrix for the supported platforms (ADR-0001 compatibility matrix).
10. [ ] `models-laya-<ver>` release with mirrored weights + LICENSE/NOTICE; SHA-256s compiled into the binary.
