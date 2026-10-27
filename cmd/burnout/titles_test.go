package main

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/identity"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/app"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

// titleCanaries are planted in browser titles; none may reach the store,
// the log, an export or a diagnostics bundle. The excluded site is also
// what the user asked to keep private.
var titleCanaries = []string{"canary.person@example.com", "CANARYtitleQ9", "CANARYsite.example", "73519846", "CANARYaB3dE5fG7hJ9kL1mN3pQ5rS7"}

type canaryClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *canaryClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

// titledCollector shows one browser window whose page changes every 20
// simulated seconds, one simulated second per snapshot.
type titledCollector struct {
	clock *canaryClock
	start time.Time
	n     int
}

func (f *titledCollector) Snapshot(context.Context) (model.Snapshot, error) {
	at := f.start.Add(time.Duration(f.n) * time.Second)
	pages := []string{
		"Inbox - canary.person@example.com - Gmail",
		"CANARYtitleQ9 · Pull Request #73519846 · owner/repo · GitHub",
		"Account CANARYtitleQ9 | CANARYsite.example",
		"Token CANARYaB3dE5fG7hJ9kL1mN3pQ5rS7 - CANARYtitleQ9",
	}
	title := pages[f.n/20%len(pages)]
	f.n++
	f.clock.mu.Lock()
	f.clock.now = at
	f.clock.mu.Unlock()
	return model.Snapshot{At: at, Windows: []model.Window{{
		ID: 7, AppID: "com.google.Chrome", Title: model.NewSensitive(title), Bounds: model.Rect{W: 800, H: 600}, Visible: 1, Focused: true,
	}}, Monitors: []model.Rect{{W: 1920, H: 1080}}, PointerWindow: 7, Keys: 2, InputCountsOn: true}, nil
}
func (f *titledCollector) Capabilities() ports.Capabilities {
	return ports.Capabilities{Name: "canary", Titles: true}
}
func (f *titledCollector) Close() error { return nil }

// TestTitleCanary is the window-title privacy canary (privacy & security
// §4.1, §10; ADR-0006 items 5–6): with title classification on, the
// daemon records categories from titles into a real store, and the
// database files, the daemon log, a JSON export and the diagnostics bundle
// hold no part of any title.
func TestTitleCanary(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	dataDir, configDir := filepath.Join(home, "data"), filepath.Join(home, "config")
	for _, d := range []string{paths.LogDir(dataDir), configDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(paths.EnvDataDir, dataDir)
	t.Setenv(paths.EnvConfigDir, configDir)
	t.Setenv(envKeystore, "file")

	cfg := config.Default()
	cfg.Privacy.TitleClassification = true
	cfg.Privacy.ExcludeDomains = []string{"canarysite.example"}
	if err := config.Save(config.Path(configDir), cfg); err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	store, err := sqlite.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(paths.LogDir(dataDir), paths.DaemonLogFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))

	start := time.Now().Add(-3 * time.Hour).Truncate(time.Minute)
	clock := &canaryClock{now: start}
	dcfg := app.DefaultConfig()
	dcfg.TickInterval = time.Millisecond
	dcfg.FlushInterval = 5 * time.Millisecond
	dcfg.Settings = settingsFrom(cfg)
	d := app.New(dcfg, app.Deps{
		Clock:       clock,
		Collector:   &titledCollector{clock: clock, start: start},
		Sink:        store,
		Hasher:      identity.NewHasher(model.NewSensitive([]byte("0123456789abcdef0123456789abcdef"))),
		ProjectRoot: func(dir string) string { return dir },
		Procs:       noProcs{},
		AgentKind:   agentKind,
		Log:         log,
	})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- d.Run(runCtx) }()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && clock.Now().Sub(start) < 5*time.Minute; {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_ = logFile.Close()

	exportPath := filepath.Join(tmp, "export.json")
	if err := runExport(ctx, []string{"--out", exportPath}); err != nil {
		t.Fatal(err)
	}
	exported, err := os.ReadFile(exportPath) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	for _, cat := range []string{`"comms"`, `"code"`, `"private"`} {
		if !bytes.Contains(exported, []byte(cat)) {
			t.Errorf("the export has no %s minutes: titles weren't classified", cat)
		}
	}

	// The parts of doctor's report that read the config; the rest probes
	// the machine (service, disk encryption, the live desktop).
	var report bytes.Buffer
	shown := reportConfig(&report, true)
	report.WriteString("  -  title classification: " + titlesLine(shown, false) + "\n")
	if !strings.HasPrefix(titlesLine(shown, false), "on:") || !strings.HasPrefix(titlesLine(config.Default(), false), "off") {
		t.Errorf("titles line: %q", report.String())
	}
	files, err := collectBundle(ctx, report.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	scanned := map[string][]byte{"export": exported, "doctor": report.Bytes()}
	for _, f := range files {
		scanned["bundle "+f.Name] = f.Data
		if f.Name == "config.toml" && !bytes.Contains(f.Data, []byte(excludedSite)) {
			t.Errorf("the bundle's config names the excluded site:\n%s", f.Data)
		}
	}
	err = filepath.WalkDir(dataDir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := os.ReadFile(p) //nolint:gosec // test file
		scanned[p] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scanned) < 5 {
		t.Fatalf("too little to scan: %d files", len(scanned))
	}
	for name, b := range scanned {
		lower := bytes.ToLower(b)
		for _, c := range append(titleCanaries, "Inbox", "Pull Request") {
			if bytes.Contains(lower, bytes.ToLower([]byte(c))) {
				t.Errorf("%q found in %s", c, name)
			}
		}
	}
}

type noProcs struct{}

func (noProcs) Ancestors(int, int) ([]int, error) { return nil, nil }
