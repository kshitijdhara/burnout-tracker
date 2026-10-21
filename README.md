# burnout-tracker

A local-first desktop agent that helps you spot early warning signs in your
work patterns. It measures attention (multi-monitor aware), terminal
activity and AI-agent sessions (Claude Code reading vs. waiting time), then
compares them against your own baseline and absolute limits.

> **Not a medical device.** It reports behavioural signals for
> self-reflection, not diagnoses.

> **For self-use only.** Don't install it to monitor anyone else. It always
> shows a tray icon while collecting, can't be installed silently, has no
> remote access, and only starts after the person using the computer
> completes setup themselves.

## Privacy in one paragraph

Everything stays on your device. There is no server and no telemetry. The
only outbound connections are three opt-in ones: calendar feed, update
check, model download. Window titles, command arguments and prompt text are
never stored. [Privacy & Security](docs/privacy-and-security.md) lists
every field that is collected.

## Trying Phase 0

Phase 0 is a developer preview: no installer, tray icon or dashboard yet.
Desktop collection works on macOS and Windows; on Linux only shell and
Claude Code hooks are recorded for now.

### macOS

You need Go 1.26.10 or newer and the Xcode command-line tools (the macOS
collector uses cgo).

**1. Build and install to a stable path.** The hooks record the binary's
absolute path, so copy it somewhere it will stay before installing them.

```sh
xcode-select --install          # skip if already installed
git clone https://github.com/kshitijdhara/burnout-tracker
cd burnout-tracker
go test ./...
go build -o bin/burnout ./cmd/burnout
mkdir -p ~/.local/bin && cp bin/burnout ~/.local/bin/
```

**2. Set up and start.**

```sh
~/.local/bin/burnout setup          # shows what is collected, asks for consent
~/.local/bin/burnout install-hooks  # zsh hook + Claude Code hooks (backs up files first)
~/.local/bin/burnout daemon         # in its own terminal tab; leave it running
```

`setup` must run in an interactive terminal. If macOS asks whether burnout
may use your keychain, choose **Always Allow**. If the keychain can't be
reached within a few seconds, burnout keeps its key in an owner-only file
in the data folder instead, and stays on that choice.

**3. Pick up the hooks.** Open a new terminal tab so zsh loads the hook, and
start a new Claude Code session (`/hooks` inside Claude Code lists them).
Sessions that were already open are not tracked.

**4. Check it works.**

```sh
~/.local/bin/burnout doctor   # permissions, disk encryption, daemon health
~/.local/bin/burnout status   # today's summary
```

Minutes appear in `status` after a minute or two of use.

**5. Things worth trying.**

- Work across two monitors for a day and compare `status` with your own
  sense of where your attention went; Phase 0 aims to be within about 10%.
- Give Claude Code a task, switch to another window while it works, then
  come back and read its output. Reading time should count as yours, and
  the agent's working time as the agent's.
- Lock the screen or walk away: nothing should be recorded while you are
  absent.
- Note whether attention follows the window you actually use. On macOS
  focus is inferred from window order, which has not yet been checked on
  many real machines, so reports are very welcome.

**6. Remove everything.**

```sh
~/.local/bin/burnout uninstall-hooks   # removes exactly what install-hooks added
# stop the daemon with Ctrl-C in its tab, then:
rm -rf ~/Library/Application\ Support/burnout
security delete-generic-password -s burnout -a hmac   # if the keychain was used
rm ~/.local/bin/burnout
```

On macOS the data and config folders are the same folder. Before deleting
it, note that `hook-backups/` inside holds copies of the files
`install-hooks` changed.

**Preview limits.** The daemon stops when its tab closes and does not start
at login. There is no tray icon, and logs go to the terminal. Phase 0 needs
no macOS privacy permissions, because window titles and input counts are
off.

### Windows and Linux

- **Windows:** build with `go build -o bin\burnout.exe .\cmd\burnout` and
  install hooks with `burnout install-hooks --zsh=false`. Data lives in
  `%LOCALAPPDATA%\burnout`, config in `%APPDATA%\burnout`.
- **Linux:** there is no desktop collector yet, so only shell and Claude
  Code hooks are recorded. Data lives in `~/.local/share/burnout`, config
  in `~/.config/burnout`.

The other steps are the same as on macOS.

## Status

Phase 0 (spike). Design docs:

- [System design](docs/system-design.md)
- [Privacy & Security](docs/privacy-and-security.md): the threat model and
  data inventory. This is the source of truth for what is collected.
- ADRs:
  - [0001 local-first Go agent](docs/adr/0001-local-first-go-agent.md)
  - [0002 attention model](docs/adr/0002-attention-model-over-active-window.md)
  - [0003 hooks instrumentation](docs/adr/0003-instrumentation-via-hooks.md)
  - [0004 modelling strategy](docs/adr/0004-modelling-strategy.md)
  - [0005 packaging via GitHub Releases](docs/adr/0005-packaging-and-distribution.md)
  - [0006 Laya/Jev for classification](docs/adr/0006-decision-model-for-classification.md)
  - [0007 data protection at rest](docs/adr/0007-data-protection-at-rest.md)
- [Design review 2026-10-20](docs/reviews/2026-10-20-design-review.md):
  findings and how v0.2 resolves them.

## Licence

[Apache-2.0](LICENSE). The GNOME Shell extension (when added) is
GPL-2.0-or-later, as GNOME requires, and carries its own licence file.
