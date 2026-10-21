package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/identity"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/ingest"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/claude"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/keystore"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/procinfo"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/app"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/classify"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/wire"
)

// hmacKeyName is the keystore entry for identity hashing.
const hmacKeyName = "hmac"

// envKeystore set to "file" forces the file keystore. Tests use it so they
// never write into the developer's real OS keychain.
const envKeystore = "BURNOUT_KEYSTORE"

func keystoreOptions() []keystore.Option {
	if os.Getenv(envKeystore) == keystore.BackendFile {
		return []keystore.Option{keystore.WithFileOnly()}
	}
	return nil
}

// messageBuffer bounds hook messages waiting for the main loop.
const messageBuffer = 4096

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func runDaemon(ctx context.Context, _ []string) error {
	if !hasConsent() {
		return errors.New("no consent recorded: run `burnout setup` first")
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	keys, err := keystore.New(dataDir, keystoreOptions()...)
	if err != nil {
		return err
	}
	key, err := keys.GetOrCreate(hmacKeyName)
	if err != nil {
		return fmt.Errorf("hmac key: %w", err)
	}

	server, err := ingest.Listen(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = server.Close() }()
	msgs := make(chan wire.Message, messageBuffer)
	go func() {
		_ = server.Serve(ctx, func(m wire.Message) {
			select {
			case msgs <- m:
			default: // the loop is far behind; drop rather than block a hook
			}
		})
	}()

	collector, err := newCollector(collectorOptions{Now: time.Now})
	if err != nil {
		log.Warn("no desktop collector; recording hook data only", "err", err)
		collector = nil
	} else {
		defer func() { _ = collector.Close() }()
		caps := collector.Capabilities()
		log.Info("collector ready", "name", caps.Name, "missing_permissions", caps.MissingPermits, "limitations", caps.Limitations)
	}

	roots := identity.NewRoots()
	d := app.New(app.DefaultConfig(), app.Deps{
		Clock:       systemClock{},
		Collector:   collector,
		Sink:        store,
		Hasher:      identity.NewHasher(key),
		ProjectRoot: roots.ProjectRoot,
		Procs:       procinfo.Info{},
		Rules:       classify.New(nil, nil),
		AgentKind:   agentKind,
		Messages:    msgs,
		Log:         log,
		Maintain: func(ctx context.Context, now time.Time) error {
			if _, err := store.RunRetention(ctx, now, sqlite.DefaultRetention()); err != nil {
				return err
			}
			return store.Backup(ctx, now)
		},
	})
	log.Info("daemon started", "version", version, "data_dir", dataDir, "keystore", keys.Backend())
	return d.Run(ctx)
}

func agentKind(agent, event string) (model.AgentEventKind, bool) {
	if agent != claude.AgentName {
		return "", false
	}
	return claude.KindFor(event)
}
