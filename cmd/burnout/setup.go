package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/fde"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/features"
)

const consentFile = "consent"

// summary is the first-run data summary (privacy & security §3, §5.2 AC-3).
const summary = `burnout-tracker collects, on this device only:

  • which apps and windows hold your attention, per minute (window titles: off)
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
// deliberately no flag to skip this (AC-2).
func runSetup(ctx context.Context, _ []string) error {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return errors.New("setup must be run interactively in a terminal")
	}
	fmt.Print(summary)
	warnIfUnencrypted(ctx)
	fmt.Print("\nType \"yes\" to consent and start collecting: ")
	in := bufio.NewReader(os.Stdin)
	line, err := in.ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(line) != "yes" {
		fmt.Println("No consent given; nothing will be collected.")
		return nil
	}
	dir, err := paths.ConfigDir()
	if err != nil {
		return err
	}
	if err := paths.EnsurePrivateDir(dir); err != nil {
		return err
	}
	stamp := fmt.Sprintf("consented %s version %s\n", time.Now().Format(time.RFC3339), version)
	if err := os.WriteFile(filepath.Join(dir, consentFile), []byte(stamp), 0o600); err != nil {
		return err
	}
	setupProfile(in)
	fmt.Println("Thanks. Next: `burnout install-hooks`, then `burnout service install` to run the daemon in the background (or `burnout daemon` in a terminal).")
	return nil
}

// setupProfile asks for the user's role and core hours (system design
// §8.1 step 2) and saves them. Enter keeps what's there. An unreadable
// config file is left alone: `burnout config` can set them later.
func setupProfile(in *bufio.Reader) {
	c, path, err := loadConfig()
	if err != nil {
		fmt.Println("\nSkipping your role and core hours: the config file is invalid (see `burnout config validate`).")
		return
	}
	c = askProfile(in, os.Stdout, c)
	if err := config.Save(path, c); err != nil {
		fmt.Printf("Couldn't save your role and core hours: %v\n", err)
		return
	}
	fmt.Printf("Saved: %s, core hours %s. Change them any time with `burnout config set profile.role|profile.core_hours`.\n\n",
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

func hasConsent() bool {
	dir, err := paths.ConfigDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, consentFile))
	return err == nil
}

// warnIfUnencrypted explains the risk when disk encryption is off
// (privacy & security §7.1: onboarding MUST check it). Install isn't blocked.
func warnIfUnencrypted(ctx context.Context) {
	dir, err := paths.DataDir()
	if err != nil {
		return
	}
	state, _, _ := fde.Status(ctx, filepath.Dir(dir))
	switch state {
	case fde.Disabled:
		fmt.Println("\n⚠  Disk encryption is OFF. This app relies on FileVault, BitLocker or")
		fmt.Println("   LUKS to protect your data if this device is lost or stolen. Please")
		fmt.Println("   turn it on. You can continue, and `burnout doctor` will keep reminding you.")
	case fde.Unknown:
		fmt.Println("\nCouldn't tell whether disk encryption is on. Please check that FileVault,")
		fmt.Println("BitLocker or LUKS is enabled; this app relies on it to protect your data.")
	}
}
