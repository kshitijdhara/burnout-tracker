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

## Trying the alpha

burnout is in alpha (Phase 1): everything is built, and it is being tried
by a small group before a wider release. Expect rough edges, and please
report them. Install a signed release, or build from source as below.
Desktop collection works on macOS, Windows, Linux X11 sessions and GNOME
on Wayland (through a GNOME Shell extension). On other Wayland desktops
only shell and Claude Code hooks are recorded for now.

> **Where it doesn't run.** burnout records only while its tray icon is
> visible, hooks included ([AC-1](docs/privacy-and-security.md)). Where no
> icon can be shown, the daemon records nothing and exits:
>
> - over ssh, or on a Mac before anyone logs in at the screen;
> - on servers and other machines without a desktop;
> - on Linux desktops without a tray, including GNOME without the
>   [AppIndicator extension](https://extensions.gnome.org/extension/615/appindicator-support/).
>
> Hook-only use on those machines isn't supported.

### Install a release

The install scripts verify the release's signature against keys pinned in
the script, then the download's checksum, and install to `~/.local/bin`
(Windows: `%LOCALAPPDATA%\Programs\burnout`). They record nothing and
start nothing. Read them before you run them:

```sh
curl -fsSLO https://raw.githubusercontent.com/kshitijdhara/burnout-tracker/main/packaging/install.sh
less install.sh
sh install.sh
```

On Windows, in PowerShell:

```powershell
Invoke-WebRequest -UseBasicParsing https://raw.githubusercontent.com/kshitijdhara/burnout-tracker/main/packaging/install.ps1 -OutFile install.ps1
notepad install.ps1
powershell -ExecutionPolicy Bypass -File install.ps1
```

Then continue with step 2 below (`burnout setup`). Later versions:
`burnout update` (verified the same way). Verifying a download by hand,
and the `.dmg`, `.deb` and `.rpm` packages: [packaging/README.md](packaging/README.md).

> **Upgrading from an earlier build?** Consent is now versioned: run
> `burnout setup` once more before the daemon will start again.

### macOS (build from source)

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
~/.local/bin/burnout setup            # what is collected, consent, then the questions below
~/.local/bin/burnout doctor           # first run: answer the keychain prompt (below)
```

`setup` must run in an interactive terminal, and has no flag to skip any
question. After you type "yes" it asks, in order:
- your role and core hours;
- whether to turn on title classification, which splits browser time by
  site. It is off unless you say yes; on macOS a yes opens System Settings
  at Screen Recording, which it needs;
- whether to install the shell and Claude Code hooks (as
  `burnout install-hooks`, which backs up files first);
- whether to add a calendar feed. Paste its ICS link at the prompt, not
  on the command line: the link is a key to your calendar;
- whether to run the daemon in the background from login (as
  `burnout service install`).

Every question defaults to no, and each no prints the command to do it
later. Setup ends by explaining the first 14 days of calibration, and
which days count. If a later version changes what burnout collects, the
daemon stops until you run `burnout setup` again and agree to the new
summary.

burnout keeps its key in your keychain through macOS's own `security`
tool, so the first thing that uses it (adding a calendar or starting the
service during setup, or the first `doctor` run) may show a keychain
prompt that names `security` rather than burnout. Choose **Always Allow**,
and answer within a few seconds: if the keychain doesn't respond in about
3 s, burnout keeps its key in an owner-only file in the data folder
instead, and stays on that choice. If you said no to the service in setup,
running `doctor` before `service install` makes the prompt appear while
you're looking.

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
~/.local/bin/burnout dashboard  # the same, and more, in your browser
~/.local/bin/burnout timeline   # today minute by minute
```

If something looks wrong, `burnout doctor --bundle burnout-diagnostics.zip`
writes the same report, today's health, your settings (excluded apps
removed) and the end of the daemon log to one file you can attach to an
issue. It shows you everything in it first and writes nothing until you
type `yes`. Nothing is uploaded.

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
7 before: strain, what kept coming up, limits crossed, your check-ins, how
your boundaries went, and one or two things to try. In a terminal it then
asks a few skippable questions: set the boundary a suggestion offers, mark
the week (vacation, on call, …), and pick a category for up to three apps
it couldn't place. Check-in answers stay on this device. If a
result is high, the check-in shows support resources for your region
(from your locale) and a worldwide directory.

**Boundaries.** Rules you set for your own work:
`burnout boundary add shutdown 21:30` (no work after 21:30 on workdays),
`add no-weekend`, `add meetings-max 4` (hours of meetings on a workday)
and `add focus-block 90` (a focus block of at least 90 minutes on
workdays). The review shows on how many days each was kept, counting only
days with enough data and leaving out vacation and sick days; 15 minutes
of work past the line doesn't count against you. A rule applies from the
next day. `boundary list` shows them (`--history` for every change) and
`boundary remove shutdown` ends one.

**Privacy controls.**
- `burnout privacy` shows what's collected, your current settings and
  your controls.
- `burnout pause 1h` (or `pause tomorrow`, or `pause` alone until
  `burnout resume`) stops recording. Hook events sent meanwhile are
  discarded.
- `burnout forget --last 1h`, `--today`, `--since 2026-10-20` or `--all`
  deletes recorded data for good, including recovery copies made since
  then. Your annotations, boundaries, consent and hooks stay.
- `burnout export --out burnout.json` writes your data out as one JSON
  file; `--format csv --out DIR` writes a new folder with one CSV per kind
  of data; `--since` and `--until` take dates. The format is described in
  [`schemas/v1/export.schema.json`](schemas/v1/export.schema.json).
  Exports are plaintext and `forget` can't delete them: `forget` and
  `privacy` list the ones still on disk.
- `burnout annotate vacation 2026-11-02 2026-11-06` marks time off. Sick
  days work the same way: such days are left out of your baseline and get
  no score. `on_call`, `release` and `exam` mark busy stretches, which are
  still scored. `annotate list` and `annotate remove ID` manage them.

**Your profile.** `setup` asks what best describes your work (developer,
student, researcher or project/product manager) and your usual weekday
hours. The role decides what the strain score weighs most: for a PM, a
day full of meetings and switching is the job, not a warning sign. Core
hours decide what counts as after-hours work. Change either later with
`burnout config set profile.role pm` or
`burnout config set profile.core_hours 08:00-16:30`. New core hours
recompute the days whose detail is still kept (180 days by default).

**The dashboard.** `burnout dashboard` opens a page in your browser with:
- your strain over the last 90 days, with what stood out and the limits
  crossed;
- today, minute by minute: categories, apps, meetings, terminal and AI
  agents;
- the weekly review, your boundaries and the days you marked (add or
  remove them), and the weekly check-in;
- a privacy page with pause, resume and "delete the last hour or today"
  buttons, what is collected and your settings.

"Blur numbers" hides the figures while you share your screen.

It runs only while the command does (Ctrl-C stops it), on 127.0.0.1. Its
buttons do what the matching commands do (`burnout pause`, `forget`,
`annotate`, `boundary`, `checkin`), and deleting asks first. The link it opens works
once, within a minute; after that the tab uses a cookie that only this
browser holds. A website you visit can't read it or press its buttons: the
page refuses other sites and host names, every change needs a token only
the page can read, and nothing is ever fetched from the internet. If the browser
doesn't open, `burnout dashboard --no-open` prints the link.

**The tray icon.** While the daemon runs, a slice of burnt toast sits in
your menu bar (macOS) or notification area (Windows, Linux). Its menu says
whether anything is being recorded, and pauses, resumes, deletes the last
hour or today, and opens the dashboard. It has no hide option: while the
daemon records anything, the icon shows. If it can't show (over ssh, or a
Linux desktop without a tray), the daemon records nothing and exits. On
Linux the desktop needs a tray; GNOME needs the AppIndicator extension.

**Nudges.** At most once a day the tray menu offers a gentle nudge, for
example after a long week or past the shutdown time you set. On macOS
a short title also shows beside the icon, unless the monthly
"Review what's collected" reminder is due. A nudge never names a number. It
waits out calls, video, pauses and private hours, and only shows between
`nudges.waking_hours` (07:00–23:00 by default). Click it to snooze it until
tomorrow or turn that kind off for good. `burnout config set nudges.enabled
false` turns them all off, and `nudges.shutdown`, `nudges.weekly_hours` and
the other kinds turn off one at a time. macOS can't tell burnout about
Focus or a screen share without a new permission, so it waits for calls
instead. Windows and Linux can't see calls yet; `burnout doctor` lists
these gaps.

**Your calendar (optional).** Meetings away from the screen count too if
you add your calendar's ICS feed:
`burnout calendar add "https://calendar.google.com/calendar/ical/…/basic.ics"`.
In Google Calendar that is Settings, your calendar, then "Secret address
in iCal format"; Outlook publishes one under Settings, Calendar, Shared
calendars. A local `.ics` file works too.
- Only meeting start and end times and busy or free are kept. Titles,
  attendees, places and notes are dropped as the feed is read.
- The URL is a key to your calendar, so it stays in your OS keystore, and
  it is fetched over HTTPS about every 3 hours. Apart from update checks
  you ask for (below), this is the only network call burnout makes.
- `burnout calendar status` shows the last sync, and
  `burnout calendar remove [--forget]` stops it.
- This adds back-to-back and after-hours meetings to what the strain score
  weighs.

**Updates.** `burnout update` installs the newest release from GitHub. It
checks the release's signature against the keys built into burnout and the
download's checksum before changing anything, refuses older versions
unless you add `--allow-downgrade`, and keeps the previous binary as
`burnout.old` (`burnout.exe.old` on Windows; `--to v1.2.3
--allow-downgrade` rolls back). A copy
installed by the install script is replaced in place. A `.deb`, `.rpm` or
`Burnout.app`, or a copy in a folder you can't write, is only downloaded
and verified, and burnout tells you how to install it. `--check` only looks, and `--channel beta` includes
pre-releases. burnout never checks on its own unless you run
`burnout config set update.check weekly`; then it asks GitHub once a week
and shows a newer release in the tray and `burnout doctor`, without
installing it.

**Settings** live in one file, `config.toml` (`burnout config path` shows
where). `burnout config get` shows them all with comments, and
`burnout config set|add|remove|unset KEY …` changes one; a running
daemon picks changes up within a few seconds. You can also edit the file
and check it with `burnout config validate`. A file with a typo or an
unknown key is rejected, never half-applied: the daemon keeps its current
settings, and won't start on it.
- `burnout config set privacy.private_hours "Mon-Fri 19:00-08:00" "Sat,Sun"`:
  nothing is recorded at those times, as if paused.
- `burnout config add privacy.exclude_apps com.spotify.client`: that
  app's time counts, under `private`, without its name. Password
  managers, finance apps, crypto wallets and a few health apps are always
  private.
- `burnout config set privacy.title_classification true`, then restart
  the daemon (`burnout service install`): browser titles tell sites
  apart, so GitHub counts as code and Gmail as comms. Titles are
  redacted and classified in memory, and never stored; only the category
  is kept. Banking, payment, health, dating and password-manager sites
  count as `private`. On macOS this needs the Screen Recording
  permission.
- `burnout config add privacy.exclude_domains example.com`: that site
  (and its subdomains) counts as `private` when title classification is
  on and a page's title names it ("Example" or "example.com").
- `burnout config add classify.overrides com.spotify.client=other
  domain:example.com=docs`: your own category for an app or a site.
  Overrides never beat exclusions or the sensitive lists.
- `burnout config set privacy.aggregate_only true`: keep only time per
  category (code, comms, browser, …), with no app names or per-window
  rows. Days recorded before you turned it on are reduced overnight.
- `burnout config set retention.minutes_days 30`: keep less. Every
  retention period can be lowered (not raised); the nightly clean-up
  applies it.

Minutes appear in `status` after a minute or two of use. Time you spend
reading without touching anything shows up once you next type, scroll or
move the pointer (up to 8 minutes later), because only then is it clear
you were there.

**5. Things worth trying.**

- **Measure a day.** Keep a rough log of what you do (meetings, reading,
  coding, breaks, with times), then compare it with `burnout timeline`.
  The aim is to be within about 10%. Note where they disagree.
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
~/.local/bin/burnout uninstall --purge   # hooks, service, data, config and keys; asks first
rm ~/.local/bin/burnout
```

`burnout uninstall` without `--purge` removes the hooks and the service but
keeps your data. By hand, the same is:

```sh
~/.local/bin/burnout uninstall-hooks     # removes exactly what install-hooks added
~/.local/bin/burnout service uninstall   # stops the daemon and removes the service
rm -rf ~/Library/Application\ Support/burnout
security delete-generic-password -s burnout -a hmac   # if the keychain was used
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

**Alpha limits.** burnout needs no macOS privacy permissions,
because window titles (unless you turn on title classification) and
input counts are off. Call and video detection
is macOS-only for now.

### Windows and Linux

Steps 2–5 are the same as on macOS, except as noted below. Building and removing differ:

- **Linux:** desktop collection works in X11 sessions, and only on a
  desktop with a tray (on GNOME, install the AppIndicator extension
  first). It reads the window list, pointer, idle time (XScreenSaver) and
  lock state (logind, or GNOME's screensaver) and needs no permissions.
  **GNOME on Wayland** needs the burnout GNOME Shell extension
  (`extensions/gnome-shell`, GNOME 45 or newer). It hands window geometry,
  stacking, focus, monitors and the pointer to the agent over a private
  owner-only socket. It sends window titles only while title
  classification is on. Idle time comes from Mutter and lock state from
  logind. The `.deb` and `.rpm` install the extension but leave it off. To
  turn it on, log out and back in, then run:

  ```sh
  gnome-extensions enable burnout-bridge@kshitijdhara.github.io
  ```

  From a source build, copy `extensions/gnome-shell/` to
  `~/.local/share/gnome-shell/extensions/burnout-bridge@kshitijdhara.github.io/`
  first. `burnout doctor` reports whether the extension is installed,
  enabled and answering. While the screen is locked, GNOME turns
  extensions off and burnout records the time as locked. On other Wayland
  desktops (KDE, Sway) there is no desktop collector yet, so only shell
  and Claude Code hooks are recorded; to collect there, choose an X11
  session at the login screen. The service reads `DISPLAY`,
  `WAYLAND_DISPLAY` and `XDG_CURRENT_DESKTOP` from the systemd user
  manager, which GNOME, KDE and most desktops fill in. If `burnout doctor`
  says `DISPLAY is not set`, run
  `systemctl --user import-environment DISPLAY WAYLAND_DISPLAY XDG_SESSION_TYPE XDG_CURRENT_DESKTOP`
  from the desktop and restart the service. Build as on macOS without
  `xcode-select`.
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
  with `--bash`). Copy `burnout.exe` somewhere stable first, for example
  `%LOCALAPPDATA%\Programs\burnout`.
  The background service is a logon task in Task Scheduler for your
  account only, so it needs no admin rights: `burnout service install`
  registers it and starts the daemon. It runs only while you're signed in
  to the desktop, and Task Scheduler is documented to restart it a minute
  after it exits with an error (not yet verified on a real machine); a
  console window may flash briefly at sign-in. It
  can't carry `BURNOUT_DATA_DIR` or the other folder overrides, so
  install refuses while one is set. Its log is
  `%LOCALAPPDATA%\burnout\logs\daemon.log`, and Go's crash output goes
  to `stderr.log` next to it. Without the service, run `burnout daemon` in
  a terminal and stop it with Ctrl-C (`pkill` and `pgrep` don't exist on
  Windows). To remove everything, in Command Prompt:

  ```bat
  burnout uninstall-hooks
  burnout service uninstall
  rmdir /s /q "%LOCALAPPDATA%\burnout"
  rmdir /s /q "%APPDATA%\burnout"
  cmdkey /delete:burnout:hmac
  ```

  The first folder holds the data (and `hook-backups\`), the second the
  config; the last line removes the key from Credential Manager.

## Status

Phase 1 alpha: the Phase 1 scope in the [roadmap](docs/system-design.md#16-roadmap)
is built, and the alpha is under way. Design docs:

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
  - [0010 GNOME Wayland bridge over a private socket](docs/adr/0010-gnome-wayland-bridge.md)
- [Design review 2026-10-20](docs/reviews/2026-10-20-design-review.md):
  findings and how v0.2 resolves them.

## Licence

[Apache-2.0](LICENSE). The GNOME Shell extension (when added) is
GPL-2.0-or-later, as GNOME requires, and carries its own licence file.
