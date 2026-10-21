package main

import (
	"context"
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

// runDoctor prints what is and isn't working. It never changes anything
// and makes no network calls.
func runDoctor(ctx context.Context, _ []string) error {
	fmt.Println("burnout", version)
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}

	check("consent recorded", hasConsent(), "run `burnout setup`")
	if err := paths.CheckPrivate(dataDir); err != nil {
		check("data dir is owner-only", false, err.Error())
	} else {
		check("data dir is owner-only", true, dataDir)
	}

	pctx, cancel := context.WithTimeout(ctx, time.Second)
	err = ingest.Ping(pctx)
	cancel()
	check("daemon is running", err == nil, "start it with `burnout daemon`")

	state, detail, _ := fde.Status(ctx, dataDir)
	switch state {
	case fde.Enabled:
		check("disk encryption", true, detail)
	case fde.Disabled:
		check("disk encryption", false, "OFF — your data is unprotected if this device is lost or stolen. Turn on FileVault, BitLocker or LUKS. ("+detail+")")
	default:
		fmt.Printf("  ?  disk encryption: unknown (%s)\n", detail)
	}

	if keys, err := keystore.New(dataDir); err == nil {
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
		_ = c.Close()
		check("desktop collector", true, caps.Name)
		for _, p := range caps.MissingPermits {
			fmt.Printf("  !  missing permission: %s\n", p)
		}
		for _, l := range caps.Limitations {
			fmt.Printf("  -  limitation: %s\n", l)
		}
	}

	if store, err := sqlite.Open(ctx, dataDir); err == nil {
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
	fmt.Printf("  %s %s: %s\n", mark, name, strings.TrimSpace(detail))
}
