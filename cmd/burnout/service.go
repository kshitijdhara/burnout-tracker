package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/ingest"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/service"
)

// serviceStartWait bounds how long install waits for the new daemon to
// answer before reporting.
const serviceStartWait = 5 * time.Second

func runService(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: burnout service install|uninstall|status")
	}
	m, dataDir, err := serviceManager()
	if err != nil {
		return err
	}
	switch args[0] {
	case "install":
		return installService(ctx, m, dataDir)
	case "uninstall":
		if err := m.Uninstall(); err != nil {
			return err
		}
		fmt.Println("Background service removed. Your data is untouched; `burnout daemon` still runs it in a terminal.")
		return nil
	case "status":
		st, err := m.Status()
		if err != nil {
			return err
		}
		fmt.Println(describeService(st, m.LogHint()))
		return nil
	default:
		return fmt.Errorf("unknown service command %q: want install, uninstall or status", args[0])
	}
}

func serviceManager() (service.Manager, string, error) {
	bin, err := binaryPath()
	if err != nil {
		return service.Manager{}, "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return service.Manager{}, "", err
	}
	// Not paths.ConfigDir: systemd looks for user units in the real XDG
	// config directory, whatever burnout's own overrides say.
	cfg, err := os.UserConfigDir()
	if err != nil {
		return service.Manager{}, "", err
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		return service.Manager{}, "", err
	}
	return service.Manager{
		GOOS: runtime.GOOS, Bin: bin, Home: home, Config: cfg, LogDir: logDir(dataDir),
		UID: os.Getuid(), Run: service.ExecRunner{},
	}, dataDir, nil
}

func installService(ctx context.Context, m service.Manager, dataDir string) error {
	if !hasConsent() {
		return errors.New("no consent recorded: run `burnout setup` first")
	}
	if unstableBinary(m.Bin) {
		return fmt.Errorf("the service would run %s, which is a temporary build; copy the binary somewhere stable first (for example ~/.local/bin) and run service install from there", m.Bin)
	}
	st, err := m.Status()
	if err != nil {
		return err
	}
	if !st.Running && daemonAnswers(ctx) {
		return errors.New("a daemon is already running, probably in a terminal: stop it with Ctrl-C, then run `burnout service install` again")
	}
	for _, env := range []string{"BURNOUT_DATA_DIR", "BURNOUT_CONFIG_DIR", "BURNOUT_RUNTIME_DIR", envKeystore} {
		if os.Getenv(env) != "" {
			fmt.Printf("Note: %s is set in this shell, but the service doesn't see it and uses the default location.\n", env)
		}
	}
	if err := m.Install(); err != nil {
		return err
	}
	deadline := time.Now().Add(serviceStartWait)
	for !daemonAnswers(ctx) {
		if time.Now().After(deadline) {
			return fmt.Errorf("the service is installed but the daemon isn't answering; see %s and run `burnout doctor`", m.LogHint())
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Printf("Background service running: it starts at login and restarts if it crashes.\nLogs: %s\n",
		filepath.Join(logDir(dataDir), "daemon.log"))
	return nil
}

// unstableBinary reports a binary that `go run` or a test built in a
// temporary directory, which the service couldn't start after it is gone.
func unstableBinary(bin string) bool {
	return strings.Contains(bin, string(filepath.Separator)+"go-build") ||
		strings.HasPrefix(bin, filepath.Clean(os.TempDir())+string(filepath.Separator))
}

func daemonAnswers(ctx context.Context) bool {
	pctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return ingest.Ping(pctx) == nil
}

func describeService(st service.Status, logHint string) string {
	switch {
	case st.Running:
		return "running (" + st.Detail + ")"
	case st.Installed:
		return "installed but not running; see " + logHint
	default:
		return "not installed: run `burnout service install` to run in the background from login"
	}
}
