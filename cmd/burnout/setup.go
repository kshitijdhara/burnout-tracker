package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/fde"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
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
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
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
	fmt.Println("Thanks. Next: `burnout install-hooks`, then `burnout service install` to run the daemon in the background (or `burnout daemon` in a terminal).")
	return nil
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
