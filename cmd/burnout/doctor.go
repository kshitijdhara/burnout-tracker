package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/fde"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/keystore"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/app"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/classify"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// runDoctor prints what is and isn't working. It doesn't change data or
// settings (it may create the data directory and record the keystore
// choice, as the daemon would) and makes no network calls.
func runDoctor(ctx context.Context, _ []string) error {
	fmt.Println("burnout", version)
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}

	checkFix("consent recorded", hasConsent(), "run `burnout setup`")
	switch err := paths.CheckPrivate(dataDir); {
	case errors.Is(err, paths.ErrUnverified):
		fmt.Println("  ?  data dir is owner-only: not verified on this platform")
	case err != nil:
		check("data dir is owner-only", false, err.Error())
	default:
		check("data dir is owner-only", true, "")
	}

	checkFix("daemon is running", daemonAnswers(ctx), "start it with `burnout service install`, or `burnout daemon` in a terminal")
	reportService()
	cfg, cfgPath, cfgErr := loadConfig()
	switch {
	case cfgErr != nil && cfgPath == "":
		check("config file", false, cfgErr.Error())
	case cfgErr != nil:
		check("config file is valid", false, printable(cfgPath)+": "+cfgErr.Error()+
			"\n     A running daemon keeps its last good settings; a new one won't start until this is fixed.")
		cfg = config.Default()
	default:
		check("config file is valid", true, printable(cfgPath))
	}

	state, detail, _ := fde.Status(ctx, dataDir)
	switch state {
	case fde.Enabled:
		check("disk encryption", true, detail)
	case fde.Disabled:
		check("disk encryption", false, "OFF — your data is unprotected if this device is lost or stolen. Turn on FileVault, BitLocker or LUKS. ("+detail+")")
	default:
		fmt.Printf("  ?  disk encryption: unknown (%s)\n", detail)
	}

	if keys, err := keystore.New(dataDir, keystoreOptions()...); err == nil {
		note := keys.Backend()
		if note == "file" {
			note += " (no OS keystore found; key file is owner-only)"
		}
		check("keystore", true, note)
	} else {
		check("keystore", false, err.Error())
	}

	if c, err := newCollector(collectorOptions{Now: time.Now}); err != nil {
		check("desktop collector", false, err.Error())
	} else {
		caps := c.Capabilities()
		snap, snapErr := c.Snapshot(ctx)
		_ = c.Close()
		check("desktop collector", true, caps.Name)
		for _, p := range caps.MissingPermits {
			fmt.Printf("  !  missing permission: %s\n", p)
		}
		for _, l := range caps.Limitations {
			fmt.Printf("  -  limitation: %s\n", l)
		}
		if snapErr == nil {
			fmt.Printf("  *  right now: %s\n", mediaNow(snap, classify.New(cfg.Privacy.ExcludeApps, nil)))
		}
	}

	if store, err := sqlite.OpenReadOnly(ctx, dataDir); err == nil {
		defer func() { _ = store.Close() }()
		day := model.LogicalDay(time.Now(), app.DefaultConfig().Rollover)
		if h, err := store.Health(ctx, day); err == nil && h != "" {
			fmt.Println("  today's health:", h)
		}
		check("database", store.QuickCheck(ctx) == nil, filepath.Join(dataDir, sqlite.FileName))
	} else {
		check("database", false, err.Error())
	}

	fmt.Println("\nNetwork: no outbound calls are configured (calendar, update checks and model download are all off).")
	return nil
}

func check(name string, ok bool, detail string) {
	mark := " ok "
	if !ok {
		mark = "FAIL"
	}
	if detail = strings.TrimSpace(detail); detail != "" {
		name += ": " + detail
	}
	fmt.Printf("  %s %s\n", mark, name)
}

// checkFix is check for a condition whose detail is only a fix, shown when
// the condition fails.
func checkFix(name string, ok bool, fix string) {
	if ok {
		fix = ""
	}
	check(name, ok, fix)
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
func reportService() {
	m, _, err := serviceManager()
	if err != nil {
		return
	}
	st, err := m.Status()
	switch {
	case err != nil:
		return
	case st.Running:
		check("background service", true, st.Detail)
	case st.Installed:
		check("background service", false, describeService(st, m.LogHint()))
	default:
		fmt.Printf("  -  background service: %s\n", describeService(st, m.LogHint()))
	}
}
