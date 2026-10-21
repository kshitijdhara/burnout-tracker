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
func runSetup(_ context.Context, _ []string) error {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return errors.New("setup must be run interactively in a terminal")
	}
	fmt.Print(summary, "\nType \"yes\" to consent and start collecting: ")
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
	fmt.Println("Thanks. Next: `burnout install-hooks`, then start the daemon (`burnout daemon`).")
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
