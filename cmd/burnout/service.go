package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
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

// serviceStopWait bounds how long uninstall waits for the daemon to stop
// answering before warning that one still runs.
const serviceStopWait = 3 * time.Second

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
		err = installService(ctx, m, dataDir)
	case "uninstall":
		err = uninstallService(ctx, m)
	case "status":
		var st service.Status
		if st, err = m.Status(); err == nil {
			fmt.Println(describeService(st, m.LogHint()))
		}
	default:
		return fmt.Errorf("unknown service command %q: want install, uninstall or status", args[0])
	}
	if errors.Is(err, service.ErrUnsupported) {
		return fmt.Errorf("%w; run `burnout daemon` in a terminal instead", err)
	}
	return err
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
	env, err := serviceEnv()
	if err != nil {
		return service.Manager{}, "", err
	}
	dataDir := env[paths.EnvDataDir]
	var sid string
	if runtime.GOOS == "windows" {
		u, err := user.Current()
		if err != nil {
			return service.Manager{}, "", err
		}
		sid = u.Uid // the SID on Windows
		env = windowsServiceEnv()
	}
	return service.Manager{
		GOOS: runtime.GOOS, Bin: bin, Args: daemonServiceArgs, Env: env,
		Home: home, Config: cfg, LogDir: paths.LogDir(dataDir),
		UID: os.Getuid(), SID: sid, Run: service.ExecRunner{},
	}, dataDir, nil
}

// windowsServiceEnv returns the overrides set in this shell. A logon task
// can't carry them, so install refuses while any is set; the defaults
// (%LOCALAPPDATA%, %APPDATA%) resolve the same way at logon.
func windowsServiceEnv() map[string]string {
	env := map[string]string{}
	for _, name := range []string{paths.EnvDataDir, paths.EnvConfigDir, paths.EnvRuntimeDir, envKeystore} {
		if v := os.Getenv(name); v != "" {
			env[name] = v
		}
	}
	return env
}

// serviceEnv pins the service to the folders this shell resolves, so the
// daemon finds the same consent, database and socket as the CLI even when
// overrides or XDG variables aren't set in the login session.
func serviceEnv() (map[string]string, error) {
	env := map[string]string{}
	for name, dir := range map[string]func() (string, error){
		paths.EnvDataDir: paths.DataDir, paths.EnvConfigDir: paths.ConfigDir, paths.EnvRuntimeDir: paths.RuntimeDir,
	} {
		d, err := dir()
		if err != nil {
			return nil, err
		}
		if env[name], err = filepath.Abs(d); err != nil {
			return nil, err
		}
	}
	if v := os.Getenv(envKeystore); v != "" {
		env[envKeystore] = v
	}
	return env, nil
}

func installService(ctx context.Context, m service.Manager, dataDir string) error {
	if !hasConsent() {
		return errors.New("no consent recorded: run `burnout setup` first")
	}
	if unstableBinary(m.Bin) {
		return fmt.Errorf("the service would run %s, which is a temporary build; copy the binary somewhere stable first (for example ~/.local/bin) and run service install from there", m.Bin)
	}
	if err := paths.CheckTrustedExecutable(m.Bin); err != nil {
		return err
	}
	st, err := m.Status()
	if err != nil {
		return err
	}
	if !st.Running && daemonAnswers(ctx) {
		return errors.New("a daemon is already running, probably in a terminal: stop it with Ctrl-C, then run `burnout service install` again")
	}
	if err := m.Install(); err != nil {
		return err
	}
	daemonLog := filepath.Join(paths.LogDir(dataDir), paths.DaemonLogFile)
	wctx, cancel := context.WithTimeout(ctx, serviceStartWait)
	defer cancel()
	for !daemonAnswers(wctx) {
		select {
		case <-wctx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Slow to open its socket (a keychain prompt, a migration),
			// or not running at all.
			if st, err := m.Status(); err == nil && st.Running {
				fmt.Printf("Background service started (%s) and still starting up; check it with `burnout doctor` in a minute.\nLogs: %s\n", st.Detail, daemonLog)
				return nil
			}
			return fmt.Errorf("the service is installed but the daemon isn't running; see %s and %s, and run `burnout doctor`", daemonLog, m.LogHint())
		case <-time.After(200 * time.Millisecond):
		}
	}
	fmt.Printf("Background service running: it starts at login and restarts if it crashes.\nLogs: %s\n", daemonLog)
	return nil
}

func uninstallService(ctx context.Context, m service.Manager) error {
	if err := m.Uninstall(); err != nil {
		return err
	}
	fmt.Println("Background service removed. Your data is untouched; `burnout daemon` still runs it in a terminal.")
	deadline := time.Now().Add(serviceStopWait)
	for daemonAnswers(ctx) {
		if time.Now().After(deadline) {
			stop := "`pkill -INT -x burnout`"
			if runtime.GOOS == "windows" {
				stop = "closing its terminal"
			}
			fmt.Printf("A daemon is still running, probably one started in a terminal: stop it with Ctrl-C or %s.\n", stop)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
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
