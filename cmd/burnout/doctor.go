package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/crashloop"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/fde"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/keystore"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/classify"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// runDoctor prints what is and isn't working. It doesn't change data or
// settings (it may create the data directory and record the keystore
// choice, as the daemon would) and makes no network calls. With --bundle
// it writes the same report, and more, to a diagnostics archive instead.
func runDoctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	bundle := fs.String("bundle", "", "write a redacted diagnostics archive (zip) to this new `file`, after showing everything in it")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return errors.New("usage: burnout doctor [--bundle FILE]")
	}
	if *bundle != "" {
		return runBundle(ctx, *bundle)
	}
	return doctor(ctx, os.Stdout, false)
}

// doctor writes the report to w. forBundle leaves out what can't be
// redacted: a config file that doesn't parse may be quoted in its error,
// and its excluded apps aren't known.
func doctor(ctx context.Context, w io.Writer, forBundle bool) error {
	_, _ = fmt.Fprintln(w, "burnout", version)
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}

	if consentStatus() == consentStale {
		checkFix(w, "consent is for the current data summary", false, "what is collected changed: run `burnout setup` again")
	} else {
		checkFix(w, "consent recorded", hasConsent(), "run `burnout setup`")
	}
	switch err := paths.CheckPrivate(dataDir); {
	case errors.Is(err, paths.ErrUnverified):
		_, _ = fmt.Fprintln(w, "  ?  data dir is owner-only: not verified on this platform")
	case err != nil:
		check(w, "data dir is owner-only", false, err.Error())
	default:
		check(w, "data dir is owner-only", true, "")
	}

	running := daemonAnswers(ctx)
	checkFix(w, "daemon is running", running, "start it with `burnout service install`, or `burnout daemon` in a terminal")
	reportService(w)
	checkFix(w, "not in safe mode", !crashloop.Load(dataDir).Safe, "the desktop collector is off after 3 starts in 10 minutes; "+
		"hooks still record, and it comes back after an hour without a crash (see the daemon log)")
	cfg := reportConfig(w, forBundle)

	state, detail, _ := fde.Status(ctx, dataDir)
	switch state {
	case fde.Enabled:
		check(w, "disk encryption", true, detail)
	case fde.Disabled:
		check(w, "disk encryption", false, "OFF — your data is unprotected if this device is lost or stolen. Turn on FileVault, BitLocker or LUKS. ("+detail+")")
	default:
		_, _ = fmt.Fprintf(w, "  ?  disk encryption: unknown (%s)\n", detail)
	}

	if keys, err := keystore.New(dataDir, keystoreOptions()...); err == nil {
		note := keys.Backend()
		if note == "file" {
			note += " (no OS keystore found; key file is owner-only)"
		}
		check(w, "keystore", true, note)
	} else {
		check(w, "keystore", false, err.Error())
	}

	_, _ = fmt.Fprintf(w, "  -  title classification: %s\n", titlesLine(cfg, daemonReadsTitles(running, dataDir)))
	reportDesktopBridge(ctx, w, cfg.Privacy.TitleClassification)
	if c, err := newCollector(collectorOptions{Titles: cfg.Privacy.TitleClassification, Now: time.Now}); err != nil {
		check(w, "desktop collector", false, err.Error())
	} else {
		caps := c.Capabilities()
		snap, snapErr := c.Snapshot(ctx)
		_ = c.Close()
		check(w, "desktop collector", true, caps.Name)
		for _, p := range caps.MissingPermits {
			_, _ = fmt.Fprintf(w, "  !  missing permission: %s\n", p)
		}
		for _, l := range caps.Limitations {
			_, _ = fmt.Fprintf(w, "  -  limitation: %s\n", l)
		}
		if snapErr == nil {
			_, _ = fmt.Fprintf(w, "  *  right now: %s\n", mediaNow(snap, classify.New(cfg.Exclusions(), cfg.Overrides())))
		}
	}

	if store, err := sqlite.OpenReadOnly(ctx, dataDir); err == nil {
		defer func() { _ = store.Close() }()
		day := model.LogicalDay(time.Now(), rollover)
		if h, err := store.Health(ctx, day); err == nil && h != "" {
			_, _ = fmt.Fprintln(w, "  today's health:", h)
		}
		check(w, "database", store.QuickCheck(ctx) == nil, filepath.Join(dataDir, sqlite.FileName))
	} else {
		check(w, "database", false, err.Error())
	}

	if n := updateNotice(dataDir, cfg); n != "" {
		_, _ = fmt.Fprintf(w, "  !  update: %s\n", n)
	}

	_, _ = fmt.Fprintln(w, "\n"+networkSummary(dataDir, cfg))
	return nil
}

// reportConfig checks the config file and returns it, or the defaults if
// it is invalid. forBundle leaves out the error, which can quote the file.
func reportConfig(w io.Writer, forBundle bool) config.Config {
	cfg, cfgPath, cfgErr := loadConfig()
	switch {
	case cfgErr != nil && forBundle:
		check(w, "config file is valid", false, "run `burnout config validate` to see why")
	case cfgErr != nil && cfgPath == "":
		check(w, "config file", false, cfgErr.Error())
	case cfgErr != nil:
		check(w, "config file is valid", false, printable(cfgPath)+": "+cfgErr.Error()+
			"\n     A running daemon keeps its last good settings; a new one won't start until this is fixed.")
	default:
		check(w, "config file is valid", true, printable(cfgPath))
		return cfg
	}
	return config.Default()
}

func check(w io.Writer, name string, ok bool, detail string) {
	mark := " ok "
	if !ok {
		mark = "FAIL"
	}
	if detail = strings.TrimSpace(detail); detail != "" {
		name += ": " + detail
	}
	_, _ = fmt.Fprintf(w, "  %s %s\n", mark, name)
}

// checkFix is check for a condition whose detail is only a fix, shown when
// the condition fails.
func checkFix(w io.Writer, name string, ok bool, fix string) {
	if ok {
		fix = ""
	}
	check(w, name, ok, fix)
}

// mediaNow describes the calls and video the collector sees at this
// moment, so detection can be checked during a meeting or a video. Apps
// classified as private are not named, as everywhere else.
func mediaNow(snap model.Snapshot, rules *classify.Rules) string {
	var calls, video []string
	for _, w := range snap.Windows {
		name := printable(w.AppID)
		if rules.App(w.AppID) == model.CategoryPrivate {
			name = "a private app"
		}
		if w.InCall && !slices.Contains(calls, name) {
			calls = append(calls, name)
		}
		if w.HasVideo && !slices.Contains(video, name) {
			video = append(video, name)
		}
	}
	var parts []string
	switch {
	case len(calls) > 0:
		parts = append(parts, "in a call in "+strings.Join(calls, ", "))
	case snap.CallActive:
		parts = append(parts, "in a call (its window isn't visible)")
	}
	if len(video) > 0 {
		parts = append(parts, "video playing in "+strings.Join(video, ", "))
	}
	if len(parts) == 0 {
		return "no call or video detected"
	}
	return strings.Join(parts, "; ")
}

// reportService shows whether the background service is set up. Platforms
// without one yet say nothing.
func reportService(w io.Writer) {
	m, _, err := serviceManager()
	if err != nil {
		return
	}
	st, err := m.Status()
	switch {
	case err != nil:
		return
	case st.Running:
		check(w, "background service", true, st.Detail)
	case st.Installed:
		check(w, "background service", false, describeService(st, m.LogHint()))
	default:
		_, _ = fmt.Fprintf(w, "  -  background service: %s\n", describeService(st, m.LogHint()))
	}
}
