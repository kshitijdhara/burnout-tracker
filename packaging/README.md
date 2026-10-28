# Packaging and releases

How releases are built, signed and installed (ADR-0005).

| File | What it is |
|------|------------|
| `../.goreleaser.yaml` | Builds every asset from a tag: darwin universal `.tar.gz` and `.dmg`, linux `.tar.gz`/`.deb`/`.rpm` (amd64, arm64), windows `.zip` (amd64, arm64), `checksums.txt`, SBOMs, cosign keyless signature |
| `../.github/workflows/release.yml` | Runs GoReleaser on a `v*` tag, signs `checksums.txt` with the pinned key in the protected `release` environment, attests provenance, publishes |
| `install.sh`, `install.ps1` | User-prefix installers that verify the pinned-key signature, then the checksum |
| `test-install.sh`, `test-install.ps1` | CI tests for the installers' verifiers, against `testdata/` (signed by a throwaway key) |
| `macos/build-dmg.sh` | Wraps the universal binary in `BurntOut.app` inside a `.dmg` (GoReleaser's own dmg support is Pro-only) |
| `nfpm/postinstall.sh` | `.deb`/`.rpm` post-install: prints next steps only |
| `../internal/release/keys.go` | The same pinned keys, for `burntout update`; a test keeps all three copies identical |

**Nothing here starts collection.** No package script, installer or
`.app` runs `burntout setup` or `burntout service install`, and none has an
option to. They print those steps for the user (privacy & security §5.2,
AC-2).

## Release assets

```
burntout_<ver>_darwin_all.tar.gz      universal CLI
burntout_<ver>_darwin_all.dmg         BurntOut.app (ad-hoc signed, not notarized)
burntout_<ver>_linux_{amd64,arm64}.{tar.gz,deb,rpm}
burntout_<ver>_windows_{amd64,arm64}.zip
*.sbom.json                          SBOM per archive (syft, SPDX)
checksums.txt                        SHA-256 of every asset above
checksums.txt.sig                    pinned-key signature of checksums.txt
checksums.txt.sigstore.json          cosign keyless bundle (optional check)
```

GitHub build-provenance attestations cover every file in `checksums.txt`.

Builds are reproducible: the same commit and Go toolchain give identical
archives and packages (`-trimpath`, empty build ID, commit timestamps).
The `.dmg` is the exception, because `hdiutil` writes a fresh volume UUID
and timestamps; the binary inside it is the reproducible one.

### Signature format

`checksums.txt.sig` is the output of
`openssl dgst -sha256 -sign key.pem checksums.txt`: an ASN.1 DER ECDSA
P-256 signature over SHA-256 of the file. A release is valid if the
*current* or the *next* pinned key verifies it. `internal/release`'s
`VerifyChecksums` implements the same check in Go.

## Installing

Read the script first, then run it:

```bash
curl -fsSLO https://raw.githubusercontent.com/kshitijdhara/burntout/main/packaging/install.sh
less install.sh
sh install.sh
```

```powershell
Invoke-WebRequest -UseBasicParsing https://raw.githubusercontent.com/kshitijdhara/burntout/main/packaging/install.ps1 -OutFile install.ps1
notepad install.ps1
powershell -ExecutionPolicy Bypass -File install.ps1
```

Both take a version (`--version v0.1.0`, `-Version v0.1.0`) and an install
directory (`--dir`, `-Dir`); the default is the latest release into
`~/.local/bin` or `%LOCALAPPDATA%\Programs\burntout`.

### Verifying by hand

Save the two public keys from `install.sh` as `current.pem` and `next.pem`,
then, in the folder with the downloads:

```bash
openssl dgst -sha256 -verify current.pem -signature checksums.txt.sig checksums.txt \
  || openssl dgst -sha256 -verify next.pem -signature checksums.txt.sig checksums.txt
```

It must print `Verified OK`. Only then check the asset against it:

```bash
shasum -a 256 -c --ignore-missing checksums.txt
```

(`sha256sum -c --ignore-missing` on Linux.) Optionally, with `cosign` and
`gh` installed:

```bash
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/kshitijdhara/burntout/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
gh attestation verify burntout_<ver>_linux_amd64.tar.gz --repo kshitijdhara/burntout
```

## Key ceremony

The real *current* and *next* keys were generated and embedded on
2026-10-27, replacing the placeholders. The steps below are the record of
that ceremony and the procedure for a fresh one.

Do this once, on a trusted machine, offline if you can:

1. Generate two P-256 key pairs, *current* and *next*:

   ```bash
   umask 077
   openssl ecparam -name prime256v1 -genkey -noout -out current.key
   openssl ecparam -name prime256v1 -genkey -noout -out next.key
   openssl ec -in current.key -pubout -out current.pub
   openssl ec -in next.key -pubout -out next.pub
   ```

2. Replace the two placeholder PEMs, in the same order, in
   `packaging/install.sh`, `packaging/install.ps1` and
   `internal/release/keys.go`, and remove the PLACEHOLDER notes.
   `go test ./internal/release/` checks the copies match.
3. In the repository settings, create an environment named **`release`**:
   - **Required reviewers:** at least one maintainer (ideally two people);
     enable "Prevent self-review" if there are two.
   - **Deployment branches and tags:** selected tags only, pattern `v*`.
   - **Environment secret `RELEASE_SIGNING_KEY`:** the full contents of
     `current.key` (PEM, including the BEGIN/END lines).
4. Protect `v*` tags with a tag ruleset (only maintainers create them, no
   deletion or update).
5. Store `next.key` offline (for example, an encrypted USB drive and a
   password manager), **not** in GitHub. Delete `current.key` from the
   machine once the secret is saved, or store it offline alongside
   `next.key`.

### Rotating

When the current key is retired (routinely, or because it may have leaked):

1. Generate a new *next* key.
2. In all three files: the old *next* becomes *current*, the new key
   becomes *next*. Release that change normally while the old current key
   still signs, so installed copies of `burntout update` learn the new keys.
3. Put the old *next* private key into `RELEASE_SIGNING_KEY`.

Installers and binaries released before the rotation already trust the old
*next* key, so they keep verifying. If the current key leaked, skip the
interim release: switch the secret to *next* right away and ship the key
change in the next release.

## Local checks

```bash
goreleaser check
goreleaser release --snapshot --clean --skip=sign,sbom   # on macOS
sh packaging/test-install.sh
shellcheck -x packaging/*.sh packaging/*/*.sh
```

`--skip=sbom` avoids needing syft; drop it if syft is installed.
