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
	"sync"
	"sync/atomic"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/control"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/crashloop"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/identity"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/ingest"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/claude"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/keystore"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/lock"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/logfile"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/procinfo"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/sdnotify"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/app"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
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
	return serveDaemon(ctx, log, dataDir, false)
}

// runServiceDaemon is the daemon as launchd, systemd or Task Scheduler
// runs it. A failure
// a restart may fix returns an error, so the service manager retries in
// 30 s. One it can't fix (no consent, a database from a newer version, an
// unusable data folder) is logged and exits 0, which the service treats as
// done until the next login, instead of failing every 30 s forever. What
// it writes shows the home folder as ~ (privacy & security §10).
func runServiceDaemon(ctx context.Context) (err error) {
	defer func() {
		if err == nil {
			// Under systemd's Type=notify, exiting 0 before READY=1 counts
			// as a failed start.
			_ = sdnotify.Ready()
		}
	}()
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
	serviceStdio(paths.LogDir(dataDir))
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
	history, started, recErr := crashloop.Record(dataDir, time.Now())
	if recErr != nil {
		log.Warn("could not record this start for crash-loop detection", "err", recErr)
	}
	err = serveDaemon(ctx, log, dataDir, history.Safe)
	if !errors.Is(err, errSafeModeOver) {
		// It returned, so it didn't crash: a stop, a logout or an error
		// a restart may fix doesn't count towards safe mode. A panic, a
		// native fault or the watchdog's kill never gets here.
		_ = crashloop.Forget(dataDir, started)
	}
	if errors.Is(err, errSafeModeOver) {
		// Exit with an error so launchd or systemd starts a normal daemon.
		log.Info("leaving safe mode after an hour without a crash; restarting with the desktop collector")
		return err
	}
	if errors.Is(err, errBadConfig) {
		log.Error("not starting: the config file is invalid; see `burnout config validate`", "err", err)
		return nil
	}
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
			_ = sdnotify.Watchdog() // a fresh ping for the start that follows
			return release, err
		}
		if !waiting {
			log.Info("another daemon is running, probably in a terminal; taking over when it stops")
			// The wait has no end: tell systemd the start is done, so it
			// doesn't time out, and keep its watchdog fed below.
			_ = sdnotify.Ready()
		}
		_ = sdnotify.Watchdog()
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(lockRetry):
		}
	}
}

// capCrashLog keeps the service's crash log small across restarts: past
// crashLogMax it becomes stderr.log.1, replacing the older one, and an
// empty owner-only stderr.log waits for the next start. This run's
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

// errBadConfig is an invalid config file at start. The daemon doesn't run
// on defaults instead: that could quietly drop the user's private hours or
// exclusions.
var errBadConfig = errors.New("invalid config")

// safeMode is whether this daemon started in safe mode, for the tray.
var safeMode atomic.Bool

// errSafeModeOver ends a safe-mode daemon after an hour without a crash,
// so the service starts again with the desktop collector.
var errSafeModeOver = errors.New("safe mode is over; restart to use the desktop collector")

// serveDaemon runs the daemon once it holds the lock. In safe mode, after
// repeated crashes (system design §11.2), it records hook data only.
func serveDaemon(ctx context.Context, log *slog.Logger, dataDir string, safe bool) error {
	safeMode.Store(safe)
	cfgPath, err := configPath()
	if err != nil {
		return err
	}
	settings, err := newLiveConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("%w: %w", errBadConfig, err)
	}
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

	dash := &trayDashboard{dataDir: dataDir, log: log}
	defer dash.close()
	// The tray icon shows before anything is collected, and nothing is
	// collected, hooks included, unless it does (privacy & security §5.2
	// AC-1).
	return runTray(ctx, trayMenu(log, dataDir, settings, dash), func(ctx context.Context, shown bool) error {
		if !shown {
			return errNoTray
		}
		return collect(ctx, log, dataDir, safe, settings, store, keys, key)
	})
}

// errNoTray means no tray icon could be shown, so nothing may be recorded
// (privacy & security §5.2 AC-1). The tray's log line says why.
var errNoTray = errors.New("no tray icon could be shown, so nothing is recorded (AC-1); see the log line above for why")

// collect records until ctx ends. Call it only while the tray icon shows.
func collect(ctx context.Context, log *slog.Logger, dataDir string, safe bool, settings *liveConfig, store *sqlite.Store,
	keys *keystore.Store, key model.Sensitive[[]byte],
) error {
	collector := screenCollector(log, safe, settings.current().Privacy.TitleClassification)
	if collector != nil {
		defer func() { _ = collector.Close() }()
	}

	server, err := ingest.Listen(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = server.Close() }()

	roots := identity.NewRoots()
	cfg := app.DefaultConfig()
	cfg.Settings = settingsFrom(settings.current())
	d := app.New(cfg, app.Deps{
		Clock:       systemClock{},
		Collector:   collector,
		Sink:        store,
		Hasher:      identity.NewHasher(key),
		ProjectRoot: func(dir string) string { return roots.Resolve(dir, projectRootTimeout) },
		Procs:       procinfo.Info{},
		AgentKind:   agentKind,
		Log:         log,
		Maintain: func(ctx context.Context, now time.Time) error {
			c := settings.current()
			if _, err := store.RunRetention(ctx, now, retentionFrom(c)); err != nil {
				return err
			}
			if err := saveScores(ctx, store, now, c); err != nil {
				log.Warn("could not update daily features and scores", "err", err)
			}
			if c.Privacy.AggregateOnly {
				// After scoring, so closed days' features come from full detail.
				if _, err := store.Aggregate(ctx, model.LogicalDay(now, cfg.Rollover)); err != nil {
					return err
				}
			}
			return store.Backup(ctx, now)
		},
		Controls: controlFiles{dataDir: dataDir},
		Forget: func(ctx context.Context, since time.Time) error {
			return store.Forget(ctx, since, model.LogicalDay(since, app.DefaultConfig().Rollover))
		},
		LoadHealth:  store.Health,
		IngestStats: func() map[string]uint64 { return ingestStatsMap(server.Stats()) },
		Reload: func() (app.Settings, bool) {
			c, changed, err := settings.check()
			if err != nil {
				log.Warn("the config file changed but is invalid; keeping the current settings (see `burnout config validate`)")
			}
			return settingsFrom(c), changed
		},
		Heartbeat: func() { _ = sdnotify.Watchdog() },
	})

	var loopsDone sync.WaitGroup
	defer loopsDone.Wait() // before the store closes
	runLoops := func(ctx context.Context) {
		loopsDone.Add(2)
		go func() { defer loopsDone.Done(); calendarLoop(ctx, log, store, dataDir, keys, settings) }()
		go func() { defer loopsDone.Done(); updateLoop(ctx, log, dataDir, settings) }()
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(ctx, func(m wire.Message) { d.Submit(m) }) }()
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go func() {
		// Ingest only returns on shutdown or a fatal error; either way, stop.
		if err := <-serveErr; err != nil {
			log.Error("ingest stopped", "err", err)
		}
		cancel(nil)
	}()
	if safe {
		t := time.AfterFunc(crashloop.Clean, func() {
			if err := crashloop.Clear(dataDir); err != nil {
				log.Warn("could not end safe mode", "err", err)
				return
			}
			cancel(errSafeModeOver)
		})
		defer t.Stop()
	}

	runLoops(runCtx)
	log.Info("daemon started", "version", version, "keystore", keys.Backend(), "safe_mode", safe)
	_ = sdnotify.Send("READY=1\nWATCHDOG=1")
	if err := d.Run(runCtx); err != nil {
		return err
	}
	if cause := context.Cause(runCtx); errors.Is(cause, errSafeModeOver) {
		return cause
	}
	return nil
}

// screenCollector is the desktop collector, or nil when this platform has
// none or the daemon is in safe mode. titles is read once: the collector
// decides at start whether it reads titles (on macOS, only with the Screen
// Recording permission).
func screenCollector(log *slog.Logger, safe, titles bool) ports.Collector {
	if safe {
		log.Warn("safe mode after 3 starts in 10 minutes: no desktop collector; recording hook data only until an hour passes without a crash")
		return nil
	}
	c, err := newCollector(collectorOptions{Titles: titles, Now: time.Now})
	if err != nil {
		log.Warn("no desktop collector; recording hook data only", "err", err)
		return nil
	}
	caps := c.Capabilities()
	log.Info("collector ready", "name", caps.Name, "titles", caps.Titles, "missing_permissions", caps.MissingPermits, "limitations", caps.Limitations)
	return presenceCollector{Collector: c, blind: !caps.Calls}
}

// liveConfig is the config file as the daemon last read it. The loop
// checks it for changes; maintenance reads it from its own goroutine.
type liveConfig struct {
	watcher *config.Watcher
	mu      sync.Mutex
	cfg     config.Config
}

func newLiveConfig(path string) (*liveConfig, error) {
	w := config.NewWatcher(path)
	c, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return &liveConfig{watcher: w, cfg: c}, nil
}

func (l *liveConfig) current() config.Config {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cfg
}

// check reports a valid change to the file. An invalid file reports its
// error once, and the current settings stay.
func (l *liveConfig) check() (config.Config, bool, error) {
	c, changed, err := l.watcher.Check()
	if !changed || err != nil {
		return config.Config{}, false, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg = c
	return c, true, nil
}

// controlFiles reads the user's pause and forget requests from the data
// folder (internal/adapters/control).
type controlFiles struct{ dataDir string }

func (c controlFiles) Paused(now time.Time) bool {
	paused, _ := control.PauseState(c.dataDir, now)
	return paused
}

func (c controlFiles) TakeForget() (time.Time, func(error), bool) {
	r, ok := control.TakeForget(c.dataDir)
	if !ok {
		return time.Time{}, nil, false
	}
	return r.Since(), func(err error) { _ = control.ForgetDone(c.dataDir, r, err) }, true
}

// saveScores computes the daily features and strain scores of completed
// days and stores them, so they outlive the raw data (two years vs. 180
// days).
func saveScores(ctx context.Context, store *sqlite.Store, now time.Time, c config.Config) error {
	opts := scoreOptions(c)
	sc, err := app.ScoreDays(ctx, store, now, opts)
	if err != nil {
		return err
	}
	if err := store.SaveFeatureDays(ctx, sc.Computed, opts.Features.Key()); err != nil {
		return err
	}
	publishGuardrails(sc.Scores)
	return store.SaveScores(ctx, sc.Scores)
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
