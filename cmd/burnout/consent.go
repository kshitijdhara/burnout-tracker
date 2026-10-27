package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
)

const consentFile = "consent"

// consentVersion numbers the data summary the user consents to. Bump it
// whenever summary's text changes what is collected (a new category, a new
// opt-in), so the daemon stops until `burnout setup` is run again and the
// new summary is consented to. TestConsentVersion fails until you do.
const consentVersion = 1

// consentState is what the consent file says.
type consentState int

const (
	consentNone    consentState = iota // no consent file
	consentStale                       // consent to an older data summary
	consentCurrent                     // consent to this summary
)

// consentStamp is the consent file's single line. consentedAt reads the
// time from the second field, so new fields go at the end.
func consentStamp(at time.Time) string {
	return fmt.Sprintf("consented %s version %s summary %d\n", at.Format(time.RFC3339), version, consentVersion)
}

func writeConsent(at time.Time) error {
	dir, err := paths.ConfigDir()
	if err != nil {
		return err
	}
	if err := paths.EnsurePrivateDir(dir); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, consentFile), []byte(consentStamp(at)), 0o600)
}

func readConsent() ([]string, bool) {
	dir, err := paths.ConfigDir()
	if err != nil {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(dir, consentFile)) //nolint:gosec // our own config folder
	if err != nil {
		return nil, false
	}
	return strings.Fields(string(b)), true
}

// consentStatus reads the consent file. One without a summary version
// predates versioning, so it is stale.
func consentStatus() consentState {
	f, ok := readConsent()
	if !ok {
		return consentNone
	}
	for i := 0; i+1 < len(f); i++ {
		if f[i] == "summary" {
			if n, err := strconv.Atoi(f[i+1]); err == nil && n >= consentVersion {
				return consentCurrent
			}
		}
	}
	return consentStale
}

// consentedAt returns when consent was given, from the consent file's
// "consented <RFC3339> version <v> summary <n>" line.
func consentedAt() (time.Time, bool) {
	f, ok := readConsent()
	if !ok || len(f) < 2 || f[0] != "consented" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, f[1])
	return t, err == nil
}

// hasConsent reports whether the user consented to the current summary.
func hasConsent() bool { return consentStatus() == consentCurrent }

// consentError says why collection can't start, or nil if it can.
func consentError() error {
	switch consentStatus() {
	case consentNone:
		return errors.New("no consent recorded: run `burnout setup` first")
	case consentStale:
		return errors.New("what burnout collects has changed since you consented: run `burnout setup` again to review it")
	}
	return nil
}
