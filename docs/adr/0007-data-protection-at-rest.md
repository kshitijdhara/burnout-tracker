# ADR-0007: Data protection at rest

**Status:** Proposed
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
- backups and cloud sync of the home directory picking up our files;
- a stolen powered-off laptop;
- files left behind after the user deletes data or uninstalls.

**Out of scope:** malware running as the same user. It can read anything
our process can, including the keystore entry. We say so plainly rather
than over-promise.

Retention also has to be real. SQLite `DELETE` doesn't remove bytes from
the file or the WAL.

## Decision

**Two layers.**

1. **OS full-disk encryption is the base layer** (FileVault, BitLocker,
   LUKS). Onboarding and `burnout doctor` check for it and warn clearly when
   it's off. We don't block install.
2. **App-level AES-256-GCM on sensitive columns:**
   - `raw_events.payload`
   - `intervals.attrs`
   - `daily_features.features`
   - `checkins.answers`
   - `scores.components` and `scores.forecast`
   - `annotations.note`

   Each value gets a random nonce and is bound to its table, column and row
   id as additional authenticated data, so ciphertext can't be swapped
   between rows. Timestamps, categories and durations stay in clear so
   queries and indexes work. They're covered by layer 1 and the file
   permissions.

**Keys:**
- **The data key** is a random 256-bit key in the OS keystore: macOS
  Keychain, Windows DPAPI / Credential Manager, or Linux Secret Service.
- **Linux without Secret Service** (i3, sway, headless): a key file at
  `0600` in the data directory, with a persistent warning in `doctor`.
  This protects only against other users and backups that skip the data
  dir.
- **The HMAC key** for identity hashes is separate, and kept in the
  keystore the same way.
- **Rotation:** `burnout keys rotate` re-encrypts the columns in batches
  with a new data key. Rotating the HMAC key creates new project and window
  identities, so history no longer links to new data. The command says so
  before running.

**Data directories** (directories `0700`, files `0600`):

| OS | Path | Note |
|----|------|------|
| macOS | `~/Library/Application Support/burnout` | Not iCloud-synced; *is* backed up by Time Machine (ciphertext columns only) |
| Windows | `%LOCALAPPDATA%\burnout` | Never Documents, which OneDrive Known Folder Move syncs |
| Linux | `$XDG_DATA_HOME/burnout` (default `~/.local/share/burnout`) | |

**Retention and real deletion:**

| Data | Default retention |
|------|-------------------|
| `raw_events` (redacted events only) | 7 days |
| Per-minute rollups / intervals | 180 days |
| `daily_features` | 2 years (configurable) |
| Check-ins | 2 years (configurable) |

- `PRAGMA secure_delete=ON`, so deleted content is overwritten.
- `auto_vacuum=INCREMENTAL`, with an incremental vacuum after the nightly
  TTL job.
- Periodic `PRAGMA wal_checkpoint(TRUNCATE)`, so old pages don't linger in
  the WAL.

**Backups:** nightly `VACUUM INTO`, keeping 3 generations in the data
directory. Sensitive columns stay encrypted with the same data key, since
`VACUUM INTO` copies ciphertext as it is. Backups follow the same retention
and deletion rules.

**Purge scope:** `burnout forget`, "delete last hour / today" and
`burnout uninstall --purge` all remove matching data from:
- the database and WAL;
- backups;
- ingest spools;
- logs;
- the in-memory classification cache;
- exports the app wrote into its own directories.

`uninstall --purge` also deletes both keystore entries. Exports the user
saved elsewhere are listed, not deleted.

## Options Considered

### Option A: Rely on OS full-disk encryption only
| Dimension | Assessment |
|-----------|------------|
| Complexity | Lowest |
| Protection | Stolen powered-off device only, and only if FDE is on; nothing against backups or sync of the home dir |
| Honesty | Fine if we stop claiming "encrypted at rest" |

### Option B: FDE + app-level column encryption (chosen)
| Dimension | Assessment |
|-----------|------------|
| Complexity | Medium: crypto wrapper in the store adapter, key management, rotation |
| Protection | Sensitive columns unreadable in backups, synced copies and on disks without FDE |
| Performance | Negligible: AES-GCM on small blobs, written in 15 s batches |
| Portability | Pure Go (`crypto/aes`, `crypto/cipher`); keeps Linux/Windows builds cgo-free |

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
| Protection | Whole file |
| Risk | Bespoke crypto in the storage engine; hard to get right and to audit |

## Trade-off Analysis

B protects the data that matters most, in the places FDE doesn't reach
(backups, sync, machines without FDE), without giving up the pure-Go driver.
Timestamps and categories stay in clear, which is a deliberate trade for
queryability. They're less sensitive than the encrypted payloads and are
still covered by FDE and file permissions.

C and D protect more of the file but cost cgo everywhere, or carry real
risk from bespoke crypto, for little extra protection against the
adversaries we actually defend against.

## Consequences

- Easier: an honest, specific claim ("sensitive fields are encrypted with a
  key in your OS keychain; enable disk encryption for the rest"), backups
  that are safe to sync, deletion that actually deletes.
- Harder:
  - key management and rotation;
  - a Linux fallback that's weaker and has to be explained;
  - losing the keystore entry makes encrypted columns unrecoverable, so
    `burnout backup` exports the key wrapped with a user passphrase.
- Revisit: SQLCipher if a security review requires whole-file encryption, or
  if cgo becomes unavoidable on every platform anyway.

## Action Items
1. [ ] `KeyStore` port with macOS Keychain, Windows DPAPI/Credential Manager, Linux Secret Service and the `0600` key-file fallback.
2. [ ] Column-encryption wrapper in `store/sqlite` (AES-256-GCM, random nonce, table/column/row-id AAD); round-trip, tamper and wrong-key tests.
3. [ ] FDE detection on all three OSes; onboarding and `doctor` warnings.
4. [ ] `secure_delete`, incremental auto-vacuum, WAL checkpointing; a test that a deleted canary is absent from the file and WAL bytes.
5. [ ] Retention jobs with the defaults above, all configurable.
6. [ ] Nightly `VACUUM INTO` backups (3 generations); restore path; `burnout backup`/`restore` with passphrase-wrapped key export.
7. [ ] Purge scope implemented and covered by the canary test across DB, WAL, backups, spools, logs and app-written exports.
8. [ ] `burnout keys rotate` for the data key; documented consequences of HMAC key rotation.
9. [ ] Data directories with `0700`/`0600` permissions created at first run and checked by `doctor`.
