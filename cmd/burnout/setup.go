package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/egress/ics"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/fde"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/keystore"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/service"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/features"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/strain"
)

// summary is the first-run data summary (privacy & security §3, §5.2
// AC-3). titles says whether title classification is on.
func summary(titles bool) string {
	window := "window titles: off"
	if titles {
		window = "browser titles are read to tell sites apart, in memory only; never stored"
	}
	return fmt.Sprintf(summaryFormat, window)
}

const summaryFormat = `burnout-tracker collects, on this device only:

  • which apps and windows hold your attention, per minute (%s)
  • whether you are present (idle time, screen lock, calls, visible video)
  • shell commands: the command name and a known subcommand only — never
    arguments — plus exit code, duration and a keyed hash of the project
  • Claude Code session timing: when it works, waits for you, and stops —
    never your prompts or its responses

Nothing leaves this device. There is no server and no telemetry.
It is for self-use only: do not install it to monitor anyone else.
Details: docs/privacy-and-security.md
`

// runSetup is the interactive first run. Collection does not start until
// the logged-in user has read the summary and typed "yes" (AC-3). There is
// deliberately no flag to skip this or any later question (AC-2).
func runSetup(ctx context.Context, _ []string) error {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return errors.New("setup must be run interactively in a terminal")
	}
	return setupFlow(ctx, bufio.NewReader(os.Stdin), os.Stdout, realSetupSteps())
}

// setupSteps are setup's side effects outside the config folder, so
// tests can run the whole flow without touching the disk-encryption check,
// dotfiles, the keystore, the service manager, a running daemon or System
// Settings.
type setupSteps struct {
	checkDisk      func(context.Context, io.Writer)
	daemonRunning  func(context.Context) bool
	installHooks   func(context.Context) error
	addCalendar    func(context.Context, ics.Source) error
	installService func(context.Context) error
	openTitlesPane func() error // nil where titles need no OS permission
}

func realSetupSteps() setupSteps {
	s := setupSteps{
		checkDisk:     warnIfUnencrypted,
		daemonRunning: daemonAnswers,
		installHooks:  func(ctx context.Context) error { return runInstallHooks(ctx, nil) },
		addCalendar: func(ctx context.Context, src ics.Source) error {
			dataDir, err := paths.DataDir()
			if err != nil {
				return err
			}
			keys, err := keystore.New(dataDir, keystoreOptions()...)
			if err != nil {
				return err
			}
			return addCalendarSource(ctx, dataDir, keys, src)
		},
		installService: func(ctx context.Context) error {
			m, dataDir, err := serviceManager()
			if err != nil {
				return err
			}
			return installService(ctx, m, dataDir)
		},
	}
	if runtime.GOOS == "darwin" {
		s.openTitlesPane = func() error { return openBrowser(screenRecordingPane) }
	}
	return s
}

// screenRecordingPane opens System Settings at Privacy & Security >
// Screen & System Audio Recording.
const screenRecordingPane = "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"

// setupFlow is system design §8.1: the summary and consent, role and core
// hours, the title opt-in, the integrations and the service, and what the
// first weeks look like. Every question after consent defaults to no, and
// each no says how to do it later.
func setupFlow(ctx context.Context, in *bufio.Reader, out io.Writer, steps setupSteps) error {
	_, _ = fmt.Fprint(out, summary(titlesOn()))
	steps.checkDisk(ctx, out)
	_, _ = fmt.Fprint(out, "\nType \"yes\" to consent and start collecting: ")
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return err
	}
	if strings.TrimSpace(line) != "yes" {
		_, _ = fmt.Fprintln(out, "No consent given; nothing will be collected.")
		return nil
	}
	if err := writeConsent(time.Now()); err != nil {
		return err
	}
	setupProfile(in, out)
	titlesTurnedOn := offerTitles(in, out, steps.openTitlesPane)

	_, _ = fmt.Fprintln(out, "\nOptional integrations. Each is off until you say yes.")
	if askYesNo(in, out, "Install the shell and Claude Code hooks? They record command names and Claude Code session timing.") {
		reportStep(out, steps.installHooks(ctx), "burnout install-hooks")
	} else {
		laterHint(out, "burnout install-hooks")
	}
	offerCalendar(ctx, in, out, steps.addCalendar)
	restarted := false
	if askYesNo(in, out, "Run burnout in the background from login?") {
		err := steps.installService(ctx)
		restarted = err == nil
		if errors.Is(err, service.ErrUnsupported) {
			_, _ = fmt.Fprintln(out, "This system has no supported service manager: run `burnout daemon` in a terminal instead.")
		} else {
			reportStep(out, err, "burnout service install")
		}
	} else {
		_, _ = fmt.Fprintln(out, "Not now. To do it later: `burnout service install`, or `burnout daemon` in a terminal.")
	}
	if titlesTurnedOn && !restarted && steps.daemonRunning(ctx) {
		_, _ = fmt.Fprintln(out, "A daemon is running and reads title classification only when it starts: run `burnout service install`\n"+
			"to restart it now, or if it runs in a terminal, stop it with Ctrl-C and start `burnout daemon` again.")
	}
	_, _ = fmt.Fprint(out, firstWeeks(strain.DefaultParams()))
	return nil
}

// askYesNo asks a y/N question. Only y or yes is a yes; Enter, anything
// else and the end of input are "not now".
func askYesNo(in *bufio.Reader, out io.Writer, question string) bool {
	_, _ = fmt.Fprintf(out, "%s [y/N] ", question)
	line, _ := in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func laterHint(out io.Writer, cmd string) {
	_, _ = fmt.Fprintf(out, "Not now. To do it later: `%s`.\n", cmd)
}

func reportStep(out io.Writer, err error, cmd string) {
	if err != nil {
		_, _ = fmt.Fprintf(out, "That didn't work: %v\nTry again later with `%s`.\n", err, cmd)
	}
}

// offerTitles is §8.1 step 3: burnout starts in zero-permission mode, and
// title classification is offered with what it adds. openPane, if set,
// opens the OS permission it needs, only after a yes. It reports whether
// it turned title classification on.
func offerTitles(in *bufio.Reader, out io.Writer, openPane func() error) bool {
	_, _ = fmt.Fprintln(out, `
burnout starts in zero-permission mode: the app in front, idle time, screen
lock and calls, and what the hooks send. Keyboard and mouse counts stay off.`)
	c, path, err := loadConfig()
	if err != nil {
		_, _ = fmt.Fprintln(out, "Skipping title classification: the config file is invalid (see `burnout config validate`).")
		return false
	}
	if c.Privacy.TitleClassification {
		_, _ = fmt.Fprintln(out, "Title classification is on. `burnout config set privacy.title_classification false` turns it off.")
		return false
	}
	_, _ = fmt.Fprintln(out, `
Optional: title classification.
  What this adds: browser time is split by site (docs, chat, video, social)
  instead of counting as one app. Titles are redacted and classified in
  memory and never stored; only the category is kept.`)
	if openPane != nil {
		_, _ = fmt.Fprintln(out, `  On macOS it needs the Screen Recording permission. If you say yes,
  System Settings opens at that page: turn burnout on there.`)
	}
	if !askYesNo(in, out, "Turn it on?") {
		laterHint(out, "burnout config set privacy.title_classification true")
		return false
	}
	c.Privacy.TitleClassification = true
	if err := config.Save(path, c); err != nil {
		_, _ = fmt.Fprintf(out, "Couldn't save it: %v\n", err)
		return false
	}
	_, _ = fmt.Fprintln(out, "Title classification is on.")
	if openPane != nil {
		if err := openPane(); err != nil {
			_, _ = fmt.Fprintln(out, "Couldn't open System Settings: open Privacy & Security > Screen & System Audio Recording and turn burnout on.")
		}
		_, _ = fmt.Fprintln(out, "Until you allow it, browser time counts as one app. `burnout doctor` shows whether it is granted.")
	}
	return true
}

// offerCalendar is §8.1 step 4's ICS feed. A remote feed's URL is a
// secret, so it is read here rather than taken as an argument, and the
// same confirmation as `burnout calendar add` explains why.
func offerCalendar(ctx context.Context, in *bufio.Reader, out io.Writer, add func(context.Context, ics.Source) error) {
	const later = "burnout calendar add <URL>"
	if !askYesNo(in, out, "Add a calendar feed (an ICS link), so meeting time counts? Only meeting times are kept.") {
		laterHint(out, later)
		return
	}
	_, _ = fmt.Fprint(out, "Paste the ICS link or file path (Enter to skip): ")
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		laterHint(out, later)
		return
	}
	src, err := ics.Parse(model.NewSensitive(line))
	if err != nil {
		reportStep(out, err, later)
		return
	}
	if src.Remote() && !confirmCalendarURL(in, out, src) {
		laterHint(out, later)
		return
	}
	reportStep(out, add(ctx, src), later)
}

// firstWeeks is §8.1 step 5: calibration and what makes a day count.
func firstWeeks(p strain.Params) string {
	return fmt.Sprintf(`
What to expect
  For the first %d days with enough data, burnout is calibrating: it learns
  what your usual days look like and shows no strain score yet. `+"`burnout status`"+`
  counts the days so far. After that each day is compared with your own
  last %d days, never with other people.

  Data quality: a day counts only if burnout saw at least %.0f%% of the time
  you were at the computer, and you were there for %d minutes or more. A day
  with less is shown as missing, with how much was seen, instead of being
  guessed at or counted as rest. Gaps usually mean the daemon wasn't running
  or a hook isn't installed: `+"`burnout doctor`"+` checks.
`, p.CalibrationDays, p.BaselineDays, p.MinCoverage*100, int(p.MinPresent.Minutes()))
}

// setupProfile asks for the user's role and core hours (system design
// §8.1 step 2) and saves them. Enter keeps what's there. An unreadable
// config file is left alone: `burnout config` can set them later.
func setupProfile(in *bufio.Reader, out io.Writer) {
	c, path, err := loadConfig()
	if err != nil {
		_, _ = fmt.Fprintln(out, "\nSkipping your role and core hours: the config file is invalid (see `burnout config validate`).")
		return
	}
	c = askProfile(in, out, c)
	if err := config.Save(path, c); err != nil {
		_, _ = fmt.Fprintf(out, "Couldn't save your role and core hours: %v\n", err)
		return
	}
	_, _ = fmt.Fprintf(out, "Saved: %s, core hours %s. Change them any time with `burnout config set profile.role|profile.core_hours`.\n",
		c.Profile.Role, c.Profile.CoreHours)
}

// askProfile asks for a role and core hours, re-asking on bad answers.
// Enter (or the end of input) keeps the current value.
func askProfile(in *bufio.Reader, out io.Writer, c config.Config) config.Config {
	roles := features.Roles()
	_, _ = fmt.Fprintln(out, "\nWhat best describes your work? It sets what the strain score weighs most.")
	current := 1
	for i, r := range roles {
		_, _ = fmt.Fprintf(out, "  %d. %s\n", i+1, r.Describe())
		if string(r) == c.Profile.Role {
			current = i + 1
		}
	}
	for {
		_, _ = fmt.Fprintf(out, "Choose 1-%d [%d]: ", len(roles), current)
		line, err := in.ReadString('\n')
		answer := strings.TrimSpace(line)
		if answer == "" {
			c.Profile.Role = string(roles[current-1])
			break
		}
		if n, convErr := strconv.Atoi(answer); convErr == nil && n >= 1 && n <= len(roles) {
			c.Profile.Role = string(roles[n-1])
			break
		}
		if err != nil {
			break
		}
		_, _ = fmt.Fprintln(out, "Type a number from the list.")
	}
	_, _ = fmt.Fprintln(out, "\nYour usual weekday working hours? Work outside them counts as after-hours.")
	for {
		_, _ = fmt.Fprintf(out, "Core hours, like 09:00-18:00 [%s]: ", c.Profile.CoreHours)
		line, err := in.ReadString('\n')
		answer := strings.TrimSpace(line)
		if answer == "" {
			break
		}
		next := c
		if setErr := next.Set("profile.core_hours", []string{answer}); setErr == nil && next.Validate() == nil {
			c = next
			break
		} else if vErr := next.Validate(); vErr != nil {
			_, _ = fmt.Fprintln(out, strings.TrimPrefix(vErr.Error(), "profile.core_hours: "))
		}
		if err != nil {
			break
		}
	}
	return c
}

// warnIfUnencrypted explains the risk when disk encryption is off
// (privacy & security §7.1: onboarding MUST check it). Install isn't blocked.
func warnIfUnencrypted(ctx context.Context, out io.Writer) {
	dir, err := paths.DataDir()
	if err != nil {
		return
	}
	state, _, _ := fde.Status(ctx, filepath.Dir(dir))
	switch state {
	case fde.Disabled:
		_, _ = fmt.Fprintln(out, "\n⚠  Disk encryption is OFF. This app relies on FileVault, BitLocker or")
		_, _ = fmt.Fprintln(out, "   LUKS to protect your data if this device is lost or stolen. Please")
		_, _ = fmt.Fprintln(out, "   turn it on. You can continue, and `burnout doctor` will keep reminding you.")
	case fde.Unknown:
		_, _ = fmt.Fprintln(out, "\nCouldn't tell whether disk encryption is on. Please check that FileVault,")
		_, _ = fmt.Fprintln(out, "BitLocker or LUKS is enabled; this app relies on it to protect your data.")
	}
}
