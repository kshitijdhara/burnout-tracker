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

**1. Build and install to a stable path.** The hooks and the service record
the binary's absolute path, so install it somewhere it will stay, in a
folder only you can write to.

```sh
xcode-select --install          # skip if already installed
git clone https://github.com/kshitijdhara/burnout-tracker
cd burnout-tracker
go test ./...
go build -o bin/burnout ./cmd/burnout
mkdir -p ~/.local/bin && install -m 0755 bin/burnout ~/.local/bin/burnout
```

**2. Set up and start.**

```sh
~/.local/bin/burnout setup            # shows what is collected, asks for consent
~/.local/bin/burnout install-hooks    # shell hooks + Claude Code hooks (backs up files first)
~/.local/bin/burnout doctor           # first run: answer the keychain prompt (below)
~/.local/bin/burnout service install  # runs the daemon in the background, from login
```

`setup` must run in an interactive terminal. burnout keeps its key in your
keychain through macOS's own `security` tool, so the first `doctor` run may
show a keychain prompt that names `security` rather than burnout. Choose
**Always Allow**, and answer within a few seconds: if the keychain doesn't
respond in about 3 s, burnout keeps its key in an owner-only file in the
data folder instead, and stays on that choice. Running `doctor` before
`service install` makes the prompt appear while you're looking.

The service starts the daemon at every login and restarts it if it
crashes. Its log is `logs/daemon.log` in the data folder (three files of at
most 10 MB); crash output goes to `logs/stderr.log`. To update, rebuild,
run the same `install -m 0755` line (it replaces the file, so the running
daemon isn't disturbed), then `service install` again to restart on the
new binary. To watch the daemon's log in a terminal instead,
skip the service and run `~/.local/bin/burnout daemon`.

**3. Pick up the hooks.** Open a new terminal tab so your shell loads the
hook, and start a new Claude Code session (`/hooks` inside Claude Code
lists them). Sessions that were already open are not tracked.

`install-hooks` sets up zsh, plus bash and fish if you use them (their
config exists, or one is your login shell). Force either with `--bash` or
`--fish`, or skip one with `--zsh=false`.
- **bash:** a block in `~/.bashrc`. It loads bash-preexec, the library
  starship, atuin and iTerm2 also use, unless one of them already has. It
  keeps your `HISTCONTROL` as it is.
- **fish:** a file in `~/.config/fish/conf.d`, so your `config.fish` isn't
  touched.

On macOS, Terminal starts bash as a login shell, which reads
`~/.bash_profile`. If yours doesn't already source `~/.bashrc`, add
`[ -r ~/.bashrc ] && . ~/.bashrc` to it. PowerShell isn't supported yet.

**4. Check it works.**

```sh
~/.local/bin/burnout doctor     # permissions, disk encryption, daemon health, calls/video right now
~/.local/bin/burnout status     # strain score and today's summary
~/.local/bin/burnout timeline   # today minute by minute
```

`status` starts with a **strain score** (0–100) for the last completed
day, the top reasons behind it, and the last 7 days. It compares each day
with your own usual weekdays or weekends, so it needs 14 days with enough
data first; until then it says how far calibration has come. Days the
computer was barely used count as missing, never as rest. The score is a
signal for reflection, not a diagnosis. On Linux, where there is no
desktop collector yet, every day counts as barely used, so there is no
score.

**Weekly check-in and review.** Once a week, `burnout checkin` asks the
13 questions of the Copenhagen Burnout Inventory (about a minute); `status`
reminds you when it's due. `burnout checkin --daily` asks two quick
questions about today. `burnout review` compares the last 7 days with the
7 before: strain, what kept coming up, limits crossed, your check-ins, and
one or two things to try. Check-in answers stay on this device. If a
result is high, the check-in shows support resources for your region
(from your locale) and a worldwide directory.

Minutes appear in `status` after a minute or two of use. Time you spend
reading without touching anything shows up once you next type, scroll or
move the pointer (up to 8 minutes later), because only then is it clear
you were there.

**5. Things worth trying.**

- **Measure a day.** Keep a rough log of what you do (meetings, reading,
  coding, breaks, with times), then compare it with `burnout timeline`.
  Phase 0 aims to be within about 10%. Note where they disagree.
- **A meeting.** During a Google Meet, Zoom or Slack huddle, run
  `burnout doctor`: "right now" should say you're in a call, and the
  meeting should count even while you only listen. Run it again while
  muted: calls are found from the microphone being in use, so this checks
  your call app keeps it open. If it says "no call or video detected",
  please report which app and browser you used, and whether you were muted.
- **Reading.** Read an article for a few minutes without scrolling, then
  scroll or type: the reading time should appear. Walk away and let the
  screen lock instead: it shouldn't.
- **Video.** Watch a video in a visible window without touching anything:
  it should count.
- Give Claude Code a task, switch to another window while it works, then
  come back and read its output. Reading time should count as yours, and
  the agent's working time as the agent's.
- Lock the screen or walk away: nothing should be recorded while you are
  absent.
- Note whether attention follows the window you actually use. On macOS
  focus is inferred from window order, which has not yet been checked on
  many real machines, so reports are very welcome.

**6. Remove everything.** On macOS the data and config folders are the same
folder. Deleting it also deletes `hook-backups/`, the copies of the files
`install-hooks` changed; copy that folder out first if you want to keep them.

```sh
~/.local/bin/burnout uninstall-hooks     # removes exactly what install-hooks added
~/.local/bin/burnout service uninstall   # stops the daemon and removes the service
rm -rf ~/Library/Application\ Support/burnout
security delete-generic-password -s burnout -a hmac   # if the keychain was used
rm ~/.local/bin/burnout
```

**Stopping things.**

- The daemon: `burnout service uninstall` stops it for good;
  `pkill -INT -x burnout` stops it until the next login. If you run it in
  a terminal instead, press Ctrl-C. Either way it saves what it has and
  exits cleanly.
- A stuck test run: press Ctrl-C. If it ignores that, run
  `pkill -f 'go-build.*/(burnout|shell)\.test'` from another tab; the pattern
  matches only this project's tests that start a shell, not other Go test
  runs. If the terminal looks garbled afterwards, type `reset`.
- To check nothing is left running: `pgrep -lx burnout` should print
  nothing.

**Preview limits.** There is no installer or tray icon yet, and the
background service is macOS and Linux only (on Windows, run `burnout
daemon` in a terminal). Phase 0 needs no macOS privacy permissions,
because window titles and input counts are off. Call and video detection
is macOS-only for now.

### Windows and Linux

Steps 2–5 are the same as on macOS, except as noted below. Building and removing differ:

- **Linux:** there is no desktop collector yet, so only shell and Claude
  Code hooks are recorded. Build as on macOS without `xcode-select`.
  The background service is a systemd user unit. Its log is
  `~/.local/share/burnout/logs/daemon.log`; `journalctl --user -u
  burnout.service` shows only startup errors and crashes. To remove
  everything:

  ```sh
  ~/.local/bin/burnout uninstall-hooks
  ~/.local/bin/burnout service uninstall
  rm -rf ~/.local/share/burnout ~/.config/burnout   # data (incl. hook-backups/) and config
  secret-tool clear service burnout username hmac    # if a keyring was used
  rm ~/.local/bin/burnout
  ```

- **Windows:** build with `go build -o bin\burnout.exe .\cmd\burnout` and
  install hooks with `burnout install-hooks --zsh=false` (Git Bash works
  with `--bash`). There is no
  background service on Windows yet: run `burnout daemon` in a terminal and
  stop it with Ctrl-C (`pkill` and `pgrep` don't exist on Windows).
  To remove everything, in Command Prompt:

  ```bat
  burnout uninstall-hooks
  rmdir /s /q "%LOCALAPPDATA%\burnout"
  rmdir /s /q "%APPDATA%\burnout"
  cmdkey /delete:burnout:hmac
  ```

  The first folder holds the data (and `hook-backups\`), the second the
  config; the last line removes the key from Credential Manager.

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
  - [0008 deciding idle stretches after they end](docs/adr/0008-retroactive-idle-resolution.md)
  - [0009 deferring the Laya spike to Phase 2](docs/adr/0009-defer-laya-spike.md)
- [Design review 2026-10-20](docs/reviews/2026-10-20-design-review.md):
  findings and how v0.2 resolves them.

## Licence

[Apache-2.0](LICENSE). The GNOME Shell extension (when added) is
GPL-2.0-or-later, as GNOME requires, and carries its own licence file.
