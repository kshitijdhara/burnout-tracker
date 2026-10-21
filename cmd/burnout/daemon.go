package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/identity"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/ingest"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/claude"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/keystore"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/lock"
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

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// projectRootTimeout bounds project-root lookups, so a command run in a
// hung network mount can't stall hook handling.
const projectRootTimeout = 200 * time.Millisecond

// defaultMemoryLimit is the soft heap limit (system design §2.2: < 60 MB
// RSS) unless GOMEMLIMIT is set in the environment.
const defaultMemoryLimit = 48 << 20

func runDaemon(ctx context.Context, _ []string) error {
	if !hasConsent() {
		return errors.New("no consent recorded: run `burnout setup` first")
	}
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(defaultMemoryLimit)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	if err := paths.EnsurePrivateDir(dataDir); err != nil {
		return err
	}
	release, err := lock.Acquire(dataDir)
	if errors.Is(err, lock.ErrLocked) {
		return errors.New("another burnout daemon is already running")
	}
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	if err := paths.ExcludeFromBackups(dataDir); err != nil {
		log.Warn("could not exclude the data directory from backups", "err", err)
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

	collector, err := newCollector(collectorOptions{Now: time.Now})
	if err != nil {
		log.Warn("no desktop collector; recording hook data only", "err", err)
		collector = nil // drop a typed nil so the daemon sees "no collector"
	} else {
		defer func() { _ = collector.Close() }()
		caps := collector.Capabilities()
		log.Info("collector ready", "name", caps.Name, "missing_permissions", caps.MissingPermits, "limitations", caps.Limitations)
	}

	server, err := ingest.Listen(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = server.Close() }()

	roots := identity.NewRoots()
	d := app.New(app.DefaultConfig(), app.Deps{
		Clock:       systemClock{},
		Collector:   collector,
		Sink:        store,
		Hasher:      identity.NewHasher(key),
		ProjectRoot: func(dir string) string { return roots.Resolve(dir, projectRootTimeout) },
		Procs:       procinfo.Info{},
		Rules:       classify.New(nil, nil),
		AgentKind:   agentKind,
		Log:         log,
		Maintain: func(ctx context.Context, now time.Time) error {
			if _, err := store.RunRetention(ctx, now, sqlite.DefaultRetention()); err != nil {
				return err
			}
			return store.Backup(ctx, now)
		},
		LoadHealth:  store.Health,
		IngestStats: func() map[string]uint64 { return ingestStatsMap(server.Stats()) },
	})

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(ctx, func(m wire.Message) { d.Submit(m) }) }()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		// Ingest only returns on shutdown or a fatal error; either way, stop.
		if err := <-serveErr; err != nil {
			log.Error("ingest stopped", "err", err)
		}
		cancel()
	}()

	log.Info("daemon started", "version", version, "keystore", keys.Backend())
	return d.Run(runCtx)
}

func agentKind(agent, event string) (model.AgentEventKind, bool) {
	if agent != claude.AgentName {
		return "", false
	}
	return claude.KindFor(event)
}

// ingestStatsMap flattens the transport counters for the health record.
func ingestStatsMap(s ingest.Stats) map[string]uint64 {
	return map[string]uint64{
		"accepted":       s.Accepted,
		"pings":          s.Pings,
		"too_large":      s.TooLarge,
		"bad_version":    s.BadVersion,
		"malformed":      s.Malformed,
		"invalid_data":   s.InvalidData,
		"peer_rejected":  s.PeerRejected,
		"rate_limited":   s.RateLimited,
		"source_limited": s.SourceLimited,
		"source_busy":    s.SourceBusy,
		"accept_errors":  s.AcceptErrors,
	}
}
