# Contributing to BurntOut

Thanks for your interest. BurntOut is a local-first agent that records how
someone works, so privacy rules come first here; everything else follows
the usual Go project habits.

## Before you start

- Read [Privacy & Security](docs/privacy-and-security.md). It is the source
  of truth for what may be collected, stored or sent anywhere. A change that
  collects something new, keeps data longer, or adds a network call needs
  that document updated in the same pull request.
- For anything larger than a small fix, open an issue first so we can agree
  on the approach. Design notes live in
  [System design](docs/system-design.md) and the [ADRs](docs/adr).
- By contributing you agree that your work is licensed under the project's
  licence: [Apache-2.0](LICENSE), or GPL-2.0-or-later for the GNOME Shell
  extension in `extensions/gnome-shell`.

## Development setup

You need Go 1.26.10 or newer. On macOS, also the Xcode command-line tools
(`xcode-select --install`): the macOS collector uses cgo.

```sh
git clone https://github.com/kshitijdhara/burntout
cd burntout
go test ./...
go build -o bin/burntout ./cmd/burntout
```

Build into `bin/` (it's ignored), not the repository root.

## Checks your change must pass

`main` only takes changes that pass CI's **lint**, **lint-macos**, **test**
(Linux, macOS, Windows) and **fuzz** jobs. To run the same checks locally:

```sh
# test: race on Linux and macOS (Windows runs it without -race)
go test -race ./...

# lint: golangci-lint v2 (config in .golangci.yml) and go vet, for every OS
golangci-lint run ./...
GOOS=windows golangci-lint run ./...
GOOS=darwin CGO_ENABLED=0 golangci-lint run ./...
go vet ./... && GOOS=windows go vet ./... && GOOS=darwin CGO_ENABLED=0 go vet ./...
go vet -tags e2e ./cmd/burntout/
govulncheck ./...

# fuzz: one target, for a short run (CI runs each listed fuzz target)
go test ./internal/wire/ -run='^$' -fuzz=FuzzDecode -fuzztime=30s
```

If you touched `packaging/`, also run `sh packaging/test-install.sh` and
`shellcheck -x packaging/*.sh packaging/*/*.sh`; CI's packaging job runs
them, plus `goreleaser check`.

The suite includes privacy **canary tests**: they plant marker strings
(window titles, command arguments, paths) and fail if any reaches the
database, logs, exports or the diagnostics bundle. If one fails, the change
leaks data; don't weaken the test.

## Rules of the road

- **No new outbound connections** without a documented, opt-in reason
  (Privacy & Security §6); `net/http` is only allowed in
  `internal/adapters/egress` and `internal/adapters/dashboard`.
- **Domain code stays pure**: `internal/domain` does no I/O and imports no
  adapters (enforced by the linter).
- **Never record without the tray icon** (AC-1), and never add a flag that
  skips consent or starts collecting silently (AC-2).
- Hook commands (`emit`, `hook`) must stay silent and always exit 0.
- Match the surrounding code: its naming, comment style and error handling.
  Tests come with behaviour changes.

## Pull requests

- Branch from `main` and open the pull request against it. Commits must be
  signed (`git commit -S`); `main` requires it.
- Fill in the template: what changed, how you tested it, and the privacy
  checklist.
- Keep pull requests focused; split unrelated changes.

## Reporting bugs and asking for features

Use the [issue templates](https://github.com/kshitijdhara/burntout/issues/new/choose).
Please don't paste your recorded data or unredacted logs into an issue.
Security problems go through [SECURITY.md](SECURITY.md), never a public
issue.

Everyone taking part follows the [code of conduct](CODE_OF_CONDUCT.md).
