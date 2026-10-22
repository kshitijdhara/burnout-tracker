package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/identity"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/ingest"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/claude"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/keystore"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/lock"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/logfile"
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

// serviceFlag makes the daemon run as the background service does.
const serviceFlag = "service"

// daemonServiceArgs are the arguments the background service starts the
// binary with.
var daemonServiceArgs = []string{"daemon", "--" + serviceFlag}

// lockRetry is how often a service daemon checks whether a daemon running
// in a terminal has stopped.
const lockRetry = 30 * time.Second

// crashLogMax caps launchd's crash log (stderr.log) between starts.
const crashLogMax = 1 << 20

func runDaemon(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	asService := fs.Bool(serviceFlag, false, "run as the background service: log to a rotating file in the data folder, "+
		"wait for a daemon running in a terminal to stop, and exit cleanly when restarting can't help")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(defaultMemoryLimit)
	}
	if *asService {
		return runServiceDaemon(ctx)
	}

	if !hasConsent() {
		return errors.New("no consent recorded: run `burnout setup` first")
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	if err := paths.EnsurePrivateDir(dataDir); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	release, err := lock.Acquire(dataDir)
	if errors.Is(err, lock.ErrLocked) {
		return errors.New("another burnout daemon is already running")
	}
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	return serveDaemon(ctx, log, dataDir)
}

// runServiceDaemon is the daemon as launchd or systemd runs it. A failure
// a restart may fix returns an error, so the service manager retries in
// 30 s. One it can't fix (no consent, a database from a newer version, an
// unusable data folder) is logged and exits 0, which the service treats as
// done until the next login, instead of failing every 30 s forever. What
// it writes shows the home folder as ~ (privacy & security §10).
func runServiceDaemon(ctx context.Context) error {
	home, _ := os.UserHomeDir()
	stderr := homeRedactor{w: os.Stderr, home: home}
	dataDir, err := paths.DataDir()
	if err == nil {
		err = paths.EnsurePrivateDir(dataDir)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "burnout: not starting: the data folder is unusable: %v\n", err)
		return nil
	}
	capCrashLog(paths.LogDir(dataDir))
	w, err := logfile.Open(filepath.Join(paths.LogDir(dataDir), paths.DaemonLogFile), logfile.DefaultMaxBytes, logfile.DefaultFiles)
	if err != nil {
		return stderr.redactErr(err)
	}
	defer func() { _ = w.Close() }()
	log := slog.New(slog.NewTextHandler(homeRedactor{w: w, home: home}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if !hasConsent() {
		log.Error("not starting: no consent recorded; run `burnout setup`, then `burnout service install`")
		return nil
	}
	release, err := waitForLock(ctx, log, dataDir)
	if err != nil || release == nil {
		return stderr.redactErr(err)
	}
	defer func() { _ = release() }()
	err = serveDaemon(ctx, log, dataDir)
	if errors.Is(err, sqlite.ErrSchemaTooNew) {
		log.Error("not starting: the database was written by a newer burnout; install that version again", "err", err)
		return nil
	}
	if err != nil {
		log.Error("daemon stopped", "err", err)
	}
	return stderr.redactErr(err)
}

// waitForLock takes the daemon lock, waiting while a daemon started in a
// terminal holds it. It returns a nil release when ctx ends first.
func waitForLock(ctx context.Context, log *slog.Logger, dataDir string) (func() error, error) {
	for waiting := false; ; waiting = true {
		release, err := lock.Acquire(dataDir)
		if !errors.Is(err, lock.ErrLocked) {
			return release, err
		}
		if !waiting {
			log.Info("another daemon is running, probably in a terminal; taking over when it stops")
		}
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(lockRetry):
		}
	}
}

// capCrashLog keeps launchd's crash log small across restarts: past
// crashLogMax it becomes stderr.log.1, replacing the older one, and an
// empty owner-only stderr.log waits for launchd's next start. This run's
// own crash output still goes to the renamed file.
func capCrashLog(logDir string) {
	path := filepath.Join(logDir, paths.CrashLogFile)
	if fi, err := os.Stat(path); err != nil || fi.Size() <= crashLogMax {
		return
	}
	if err := os.Rename(path, path+".1"); err != nil {
		return
	}
	if f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil { //nolint:gosec // our own log directory
		_ = f.Close()
	}
}

// homeRedactor writes through to w with the home folder shown as ~.
type homeRedactor struct {
	w    io.Writer
	home string
}

func (h homeRedactor) Write(p []byte) (int, error) {
	if len(h.home) > 1 {
		if _, err := h.w.Write(bytes.ReplaceAll(p, []byte(h.home), []byte("~"))); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return h.w.Write(p)
}

func (h homeRedactor) redactErr(err error) error {
	if err == nil || len(h.home) <= 1 {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), h.home, "~"))
}

// serveDaemon runs the daemon once it holds the lock.
func serveDaemon(ctx context.Context, log *slog.Logger, dataDir string) error {
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
