package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/egress/ics"
	"github.com/kshitijdhara/burntout/internal/adapters/paths"
	"github.com/kshitijdhara/burntout/internal/config"
)

func TestAskProfile(t *testing.T) {
	for _, tc := range []struct {
		name, input, role, hours, says string
	}{
		{"defaults on enter", "\n\n", "developer", "09:00-18:00", "[1]"},
		{"pick and type", "4\n8:30-17:00\n", "pm", "08:30-17:00", "project or product manager"},
		{"re-asks bad answers", "9\nmanager\n2\n18:00-09:00\n10:00-19:00\n", "student", "10:00-19:00", "past midnight"},
		{"end of input keeps defaults", "", "developer", "09:00-18:00", "Core hours"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			c := askProfile(bufio.NewReader(strings.NewReader(tc.input)), &out, config.Default())
			if c.Profile.Role != tc.role || c.Profile.CoreHours != tc.hours {
				t.Errorf("got %+v\n%s", c.Profile, out.String())
			}
			if !strings.Contains(out.String(), tc.says) {
				t.Errorf("output lacks %q:\n%s", tc.says, out.String())
			}
			if err := c.Validate(); err != nil {
				t.Errorf("invalid result: %v", err)
			}
		})
	}
	// The current role is the default.
	c := config.Default()
	c.Profile.Role = "researcher"
	if got := askProfile(bufio.NewReader(strings.NewReader("\n\n")), &bytes.Buffer{}, c); got.Profile.Role != "researcher" {
		t.Errorf("current role not kept: %+v", got.Profile)
	}
}

// summaryHashes are the data summaries users consented to, by
// consentVersion. If this test fails you changed what the summary says:
// bump consentVersion, so everyone reviews the new summary before the
// daemon collects again, and add the new hash here.
var summaryHashes = map[int]string{
	// Version 1, renamed from burnout-tracker to BurntOut. Only the name
	// changed, not what is collected, so consent stays valid.
	1: "e28eee3abed79324d9cb9fe01982f1b97f8da79aa5af77099b0afb28d762e4b5",
}

func TestConsentVersion(t *testing.T) {
	got := fmt.Sprintf("%x", sha256.Sum256([]byte(summary(false)+summary(true))))
	if want := summaryHashes[consentVersion]; got != want {
		t.Fatalf("the data summary changed (sha256 %s) but consentVersion is still %d with hash %q: bump consentVersion and add the new hash to summaryHashes", got, consentVersion, want)
	}
	for v, h := range summaryHashes {
		if v != consentVersion && h == got {
			t.Errorf("consentVersion %d has the same summary as version %d", consentVersion, v)
		}
	}
}

func TestConsentStatus(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(paths.EnvConfigDir, dir)
	if consentStatus() != consentNone || consentError() == nil {
		t.Fatal("no file: want no consent")
	}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, stale := range []string{
		"consented 2026-10-01T09:00:00Z version v0.1.0\n",           // before versioning
		"consented 2026-10-01T09:00:00Z version v0.1.0 summary 0\n", // an older summary
		"consented 2026-10-01T09:00:00Z version v0.1.0 summary x\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, consentFile), []byte(stale), 0o600); err != nil {
			t.Fatal(err)
		}
		if consentStatus() != consentStale || hasConsent() {
			t.Errorf("%q: want stale", stale)
		}
		if err := consentError(); err == nil || !strings.Contains(err.Error(), "setup` again") {
			t.Errorf("%q: error %v", stale, err)
		}
		if got, ok := consentedAt(); !ok || !got.Equal(at) {
			t.Errorf("%q: consentedAt = %v, %v", stale, got, ok)
		}
	}
	if err := writeConsent(at); err != nil {
		t.Fatal(err)
	}
	if !hasConsent() || consentError() != nil {
		t.Fatal("fresh stamp: want current consent")
	}
	if got, ok := consentedAt(); !ok || !got.Equal(at) {
		t.Errorf("consentedAt = %v, %v", got, ok)
	}
}

// fakeSteps records which of setup's side effects ran.
type fakeSteps struct {
	ran     []string
	src     ics.Source
	err     error // returned by every step
	running bool  // a daemon answers
}

func (f *fakeSteps) steps(pane bool) setupSteps {
	s := setupSteps{
		checkDisk:     func(context.Context, io.Writer) { f.ran = append(f.ran, "disk") },
		daemonRunning: func(context.Context) bool { return f.running },
		installHooks:  func(context.Context) error { f.ran = append(f.ran, "hooks"); return f.err },
		addCalendar: func(_ context.Context, src ics.Source) error {
			f.ran, f.src = append(f.ran, "calendar"), src
			return f.err
		},
		installService: func(context.Context) error { f.ran = append(f.ran, "service"); return f.err },
	}
	if pane {
		s.openTitlesPane = func() error { f.ran = append(f.ran, "pane"); return f.err }
	}
	return s
}

func TestSetupFlow(t *testing.T) {
	const secret = "https://cal.example.com/private/s3cr3t-token/basic.ics"
	for _, tc := range []struct {
		name, input string
		pane        bool
		running     bool
		err         error
		ran         []string
		titles      bool
		says, never []string
	}{
		{
			name:  "no consent stops",
			input: "no\n",
			ran:   []string{"disk"},
			says:  []string{"No consent given"},
			never: []string{"core hours", "hooks"},
		},
		{
			name:   "yes to everything",
			input:  "yes\n\n\ny\ny\ny\n" + secret + "\nyes\ny\n",
			pane:   true,
			ran:    []string{"disk", "pane", "hooks", "calendar", "service"},
			titles: true,
			says:   []string{"zero-permission", "What this adds", "Screen Recording", "key to your whole calendar", "cal.example.com", "calibrating", "Data quality"},
			never:  []string{"s3cr3t", "Not now"},
		},
		{
			name:  "no to everything",
			input: "yes\n\n\n\nn\nno\nN\n",
			ran:   []string{"disk"},
			says: []string{
				"`burntout config set privacy.title_classification true`", "`burntout install-hooks`",
				"`burntout calendar add <URL>`", "`burntout service install`", "first 14 days",
			},
			never: []string{"Screen Recording"},
		},
		{
			name:  "end of input is not now",
			input: "yes\n4\n",
			ran:   []string{"disk"},
			says:  []string{"`burntout install-hooks`", "`burntout service install`", "calibrating"},
		},
		{
			name:  "declining the calendar URL warning adds nothing",
			input: "yes\n\n\n\n\ny\n" + secret + "\nno\n\n",
			ran:   []string{"disk"},
			says:  []string{"Not added.", "`burntout calendar add <URL>`"},
			never: []string{"s3cr3t"},
		},
		{
			name:  "a failed step says how to retry",
			input: "yes\n\n\n\ny\n\ny\n",
			err:   errors.New("boom"),
			ran:   []string{"disk", "hooks", "service"},
			says:  []string{"That didn't work: boom\nTry again later with `burntout install-hooks`", "later with `burntout service install`"},
		},
		{
			name:    "titles on with a daemon running says to restart it",
			input:   "yes\n\n\ny\n\n\n\n",
			running: true,
			ran:     []string{"disk"},
			titles:  true,
			says:    []string{"reads title classification only when it starts", "Ctrl-C"},
		},
		{
			name:    "installing the service restarts the daemon",
			input:   "yes\n\n\ny\n\n\ny\n",
			running: true,
			ran:     []string{"disk", "service"},
			titles:  true,
			never:   []string{"only when it starts"},
		},
		{
			name:    "no restart hint when titles stay off",
			input:   "yes\n\n\n\n\n\n\n",
			running: true,
			ran:     []string{"disk"},
			never:   []string{"only when it starts"},
		},
		{
			name:    "a failed service install still says to restart",
			input:   "yes\n\n\ny\n\n\ny\n",
			running: true,
			err:     errors.New("boom"),
			ran:     []string{"disk", "service"},
			titles:  true,
			says:    []string{"only when it starts"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv(paths.EnvConfigDir, dir)
			f := &fakeSteps{err: tc.err, running: tc.running}
			var out bytes.Buffer
			if err := setupFlow(context.Background(), bufio.NewReader(strings.NewReader(tc.input)), &out, f.steps(tc.pane)); err != nil {
				t.Fatal(err)
			}
			if strings.Join(f.ran, ",") != strings.Join(tc.ran, ",") {
				t.Errorf("ran %v, want %v\n%s", f.ran, tc.ran, out.String())
			}
			for _, s := range tc.says {
				if !strings.Contains(out.String(), s) {
					t.Errorf("output lacks %q:\n%s", s, out.String())
				}
			}
			for _, s := range tc.never {
				if strings.Contains(out.String(), s) {
					t.Errorf("output has %q:\n%s", s, out.String())
				}
			}
			if tc.titles != titlesOn() {
				t.Errorf("title classification on = %v, want %v", titlesOn(), tc.titles)
			}
			if consented := len(tc.ran) > 1 || strings.HasPrefix(tc.input, "yes"); consented != hasConsent() {
				t.Errorf("consent recorded = %v, want %v", hasConsent(), consented)
			}
			if slices.Contains(f.ran, "calendar") && f.src.Value().Reveal() != secret {
				t.Errorf("calendar got %q", f.src.Describe())
			}
		})
	}
}
