# ADR-0007: Data protection at rest

**Status:** Accepted
**Date:** 2026-10-20
**Deciders:** Project maintainers

## Context

The database holds data that reveals a lot about someone: work hours, a
sleep-window proxy, check-in answers, strain scores and the apps they use.
The v0.1 design claimed "encrypted at rest", but the chosen driver
(`modernc.org/sqlite`, pure Go) can't encrypt, and there was no threat model
to say what encryption should protect against.

**What we defend against:**
- other local OS users;
- a stolen powered-off laptop;
- backups and cloud sync of the home directory picking up our files;
- files left behind after the user deletes data or uninstalls.

**Out of scope:** malware running as the same user. It can read anything
our process can. We say so plainly rather than over-promise.

Retention also has to be real. SQLite `DELETE` doesn't remove bytes from
the file or the WAL.

## Decision

**Rely on OS full-disk encryption (FDE) for the database. No app-level
encryption of the database.**

1. **FDE is the at-rest protection.** That means FileVault on macOS,
   BitLocker or Device Encryption on Windows, and LUKS on Linux.
   - Onboarding checks FDE status. If it's off, onboarding explains the risk
     and links the OS instructions.
   - `burnout doctor` and the dashboard show a **persistent warning** while
     FDE is off.
   - We don't block install. Users on machines without FDE (for example
     managed machines they can't change) can still use the tool knowingly.
2. **Owner-only file permissions** protect against other local users:
   directories `0700` and files `0600`, or owner-only ACLs on Windows.
3. **Keep the data out of places FDE doesn't cover**:
   - **macOS:** the data directory is excluded from Time Machine with a
     sticky exclusion (`tmutil addexclusion`). Time Machine destinations are
     often unencrypted external drives, which FDE doesn't protect.
   - **Windows:** `%LOCALAPPDATA%` isn't covered by OneDrive Known Folder Move
     or by default File History.
   - **Linux:** `$XDG_DATA_HOME` is documented as a path to exclude from
     unencrypted backup tools.
4. **Portable backups are encrypted.** `burnout backup` writes a single
   archive encrypted with a user passphrase (Argon2id key derivation +
   AES-256-GCM, Go standard library and `x/crypto`), because the archive is
   meant to leave the machine (F17). `burnout restore` reverses it. This
   protects a file in transit. It isn't encryption of the live database.

**What we claim:** "Your data stays on your device, readable only by your
user account. Turn on disk encryption to protect it if your device is lost
or stolen." We don't claim the database itself is encrypted.

**Keys still needed:** the **HMAC key** for identity hashing (Privacy &
Security §4.5). It isn't an encryption key; it makes project and window
identities non-reversible.
- It lives in the OS keystore: macOS Keychain, Windows DPAPI / Credential
  Manager, or Linux Secret Service.
- Linux without Secret Service (i3, sway, headless) falls back to a `0600`
  key file in the data directory, with a note in `doctor`.
- `burnout keys rotate` creates a new HMAC key. History no longer links to
  new data, and the command says so before running.

**Data directories:**

| OS | Path | Note |
|----|------|------|
| macOS | `~/Library/Application Support/burnout` | Not iCloud-synced; excluded from Time Machine (sticky exclusion) |
| Windows | `%LOCALAPPDATA%\burnout` | Never Documents, which OneDrive Known Folder Move syncs |
| Linux | `$XDG_DATA_HOME/burnout` (default `~/.local/share/burnout`) | Document exclusion from unencrypted backup tools |

**Retention and real deletion:**

| Data | Default retention |
|------|-------------------|
| `raw_events` (redacted events only) | 7 days |
| Per-minute rollups / intervals | 180 days |
| `daily_features` | 2 years (configurable) |
| Check-ins | 2 years (configurable) |

- `PRAGMA secure_delete=ON`, so deleted content is overwritten in the file.
- `auto_vacuum=INCREMENTAL`, with an incremental vacuum after the nightly
  TTL job.
- Periodic `PRAGMA wal_checkpoint(TRUNCATE)`, so old pages don't linger in
  the WAL.
- On SSDs, overwriting doesn't guarantee physical erasure, because of wear
  levelling. FDE is what makes leftover blocks unreadable. That's one more
  reason the warning matters.

**Local recovery copies:** nightly `VACUUM INTO`, keeping 3 generations in
the data directory, with the same permissions, retention and deletion rules
as the database. They exist for corruption recovery (system design §11.2),
not for moving data between machines.

**Purge scope:** `burnout forget`, "delete last hour / today" and
`burnout uninstall --purge` remove matching data from:
- the database and WAL;
- local recovery copies;
- ingest spools;
- logs;
- the in-memory classification cache;
- exports the app wrote into its own directories.

`uninstall --purge` also deletes the keystore entry. Exports and `burnout
backup` archives saved elsewhere are listed, not deleted.

## Options Considered

### Option A: Rely on OS full-disk encryption (chosen)
| Dimension | Assessment |
|-----------|------------|
| Complexity | Lowest: no crypto in the storage layer, no data-key management |
| Protection | Stolen powered-off device when FDE is on; other users via permissions; backup/sync exposure handled by keeping the data dir out of them |
| Honesty | Clear, specific claim; FDE status visible in `doctor` |
| Failure mode | Users without FDE are unprotected against device theft. We warn them persistently |

### Option B: FDE + app-level column encryption
| Dimension | Assessment |
|-----------|------------|
| Complexity | Medium: crypto wrapper, data-key management, rotation, key escrow for restore |
| Protection | Sensitive columns unreadable in backups, synced copies and on disks without FDE |
| Cost | A lost keystore entry makes data unrecoverable; more code in the most sensitive path |

### Option C: SQLCipher
| Dimension | Assessment |
|-----------|------------|
| Complexity | Medium–High: cgo on every platform, cross-compilation pain |
| Protection | Whole file, including indexes and metadata |
| Cost | Loses the pure-Go driver; larger release matrix |

### Option D: Whole-file encryption with a custom SQLite VFS
| Dimension | Assessment |
|-----------|------------|
| Complexity | High: custom VFS, page-level crypto, WAL handling |
| Risk | Bespoke crypto in the storage engine; hard to get right and to audit |

## Trade-off Analysis

All modern desktop OSes ship FDE, and it's on by default on most new Macs
and many Windows 11 devices. With FDE on, app-level encryption adds little
against the adversaries in scope. Its main extra benefit, covering backups
and synced copies, comes more simply from keeping the data directory out
of them. A also removes a whole class of failure: a lost or reset keystore
entry no longer makes data unreadable. And it keeps the storage layer
simple and auditable.

What we give up: users who run without FDE have no protection if their
device is stolen. We accept that, make it visible, and don't pretend
otherwise. If the threat model changes (shared machines without FDE become
a target audience, or a security review demands it), B is the next step and
fits behind the existing `Store` port.

## Consequences

- Easier: simpler store adapter, no data-key lifecycle, no
  unrecoverable-data failure mode, an honest claim users can verify.
- Harder: the FDE warning has to be clear without nagging. The Time Machine
  exclusion means users rely on `burnout backup` for portability, so
  onboarding and the docs have to say so.
- Revisit: Option B if users without FDE turn out to be common, or if a
  security review requires app-level encryption.

## Action Items
1. [ ] FDE detection on macOS (`fdesetup status`), Windows (BitLocker / Device Encryption status) and Linux (LUKS/dm-crypt on the data dir's backing device). Onboarding explanation, persistent `doctor` and dashboard warning.
2. [ ] Data directories with `0700`/`0600` (owner-only ACL on Windows), created at first run and checked by `doctor`.
3. [ ] macOS sticky Time Machine exclusion for the data directory; documentation for Linux backup tools.
4. [ ] `KeyStore` port for the HMAC key (macOS Keychain, Windows DPAPI/Credential Manager, Linux Secret Service, `0600` key-file fallback); `burnout keys rotate`.
5. [ ] `secure_delete`, incremental auto-vacuum, WAL checkpointing; a test that a deleted canary is absent from the file and WAL bytes.
6. [ ] Retention jobs with the defaults above, all configurable.
7. [ ] Nightly `VACUUM INTO` recovery copies (3 generations) and the restore path.
8. [ ] `burnout backup`/`restore`: a passphrase-encrypted archive (Argon2id + AES-256-GCM); round-trip and wrong-passphrase tests.
9. [ ] Purge scope implemented and covered by the canary test across DB, WAL, recovery copies, spools, logs and app-written exports.
