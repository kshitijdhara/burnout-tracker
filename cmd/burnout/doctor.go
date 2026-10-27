package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/fde"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/ingest"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/keystore"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/app"
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

	pctx, cancel := context.WithTimeout(ctx, time.Second)
	err = ingest.Ping(pctx)
	cancel()
	checkFix("daemon is running", err == nil, "start it with `burnout daemon`")

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

	if _, err := newIndicator(); err != nil {
		check("tray indicator", false, err.Error())
	} else {
		check("tray indicator", true, "")
	}

	if c, err := newCollector(collectorOptions{Now: time.Now}); err != nil {
		check("desktop collector", false, err.Error())
	} else {
		caps := c.Capabilities()
		_ = c.Close()
		check("desktop collector", true, caps.Name)
		for _, p := range caps.MissingPermits {
			fmt.Printf("  !  missing permission: %s\n", p)
		}
		for _, l := range caps.Limitations {
			fmt.Printf("  -  limitation: %s\n", l)
		}
	}

	if store, err := sqlite.OpenReadOnly(ctx, dataDir); err == nil {
		defer func() { _ = store.Close() }()
		day := model.LogicalDay(time.Now(), app.DefaultConfig().Rollover)
		if h, err := store.Health(ctx, day); err == nil && h != "" {
			fmt.Println("  today's health:", h)
			printNoIndicator(h)
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

// printNoIndicator explains drops made because no tray icon was visible
// (privacy & security §5.2, AC-1), so "nothing recorded" has a reason.
func printNoIndicator(health string) {
	var h struct {
		NoIndicatorTicks int               `json:"no_indicator_ticks"`
		Hooks            map[string]uint64 `json:"hooks"`
	}
	if json.Unmarshal([]byte(health), &h) != nil {
		return
	}
	if n := h.Hooks["no_indicator"]; n > 0 || h.NoIndicatorTicks > 0 {
		fmt.Printf("  !  not recording while the tray icon is hidden: %d hook events dropped, %d collector ticks skipped\n", n, h.NoIndicatorTicks)
	}
}
