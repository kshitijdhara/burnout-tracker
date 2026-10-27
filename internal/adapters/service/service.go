// Package service installs the daemon as a per-user background service
// that starts at login and restarts after a crash (ADR-0005, system design
// §11.2): a launchd LaunchAgent on macOS, a systemd --user unit on Linux.
// Rendering the definitions is pure; changing the system goes through a
// Runner, so tests never touch launchctl or systemctl.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/fileedit"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
)

// Names of the service in each system.
const (
	Label    = "io.github.kshitijdhara.burnout-tracker" // launchd
	UnitName = "burnout.service"                        // systemd
)

// ErrUnsupported is returned on platforms without a service yet (Windows).
var ErrUnsupported = errors.New("service: not available on this platform yet")

// Runner runs a system command and returns its combined output.
type Runner interface {
	Run(name string, args ...string) (string, error)
}

// commandTimeout bounds one launchctl or systemctl call: a stuck launchd
// or user bus must not hang an install, an uninstall or doctor.
const commandTimeout = 30 * time.Second

// ExecRunner runs commands for real, with a timeout, and launchctl by its
// absolute path (it lives in /bin on every macOS).
type ExecRunner struct{}

// Run executes name with args (no shell) and returns its combined output.
func (ExecRunner) Run(name string, args ...string) (string, error) {
	if name == "launchctl" {
		name = "/bin/launchctl"
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // fixed launchctl/systemctl invocations
	return string(out), err
}

// Manager installs, removes and inspects the service for one user.
type Manager struct {
	GOOS string
	Bin  string   // absolute path of the burnout binary
	Args []string // the daemon's arguments, e.g. daemon --service
	// Env pins the daemon's locations to the ones the installing shell
	// resolved, so the service and the CLI always agree.
	Env    map[string]string
	Home   string // the user's home directory
	Config string // XDG config directory on Linux (os.UserConfigDir)
	LogDir string // where launchd writes the daemon's crash output
	UID    int
	Run    Runner
	// Sleep waits between retries; nil means time.Sleep. Tests stub it.
	Sleep func(time.Duration)
}

// Status is what the system reports about the service.
type Status struct {
	Installed bool   // the definition file exists
	Running   bool   // the daemon process is up
	Detail    string // e.g. "pid 812" or the system's state
}

// backend is one platform's service manager.
type backend interface {
	path(m Manager) string
	render(m Manager) ([]byte, error)
	prepare(m Manager) error // before the first start
	start(m Manager, path string) error
	stop(m Manager) error // succeeds when the service isn't loaded
	afterRemove(m Manager)
	status(m Manager, st Status) (Status, error)
	logHint(m Manager) string
}

func (m Manager) backend() (backend, error) {
	switch m.GOOS {
	case "darwin":
		return launchd{}, nil
	case "linux":
		return systemd{}, nil
	default:
		return nil, ErrUnsupported
	}
}

// Path returns where the service definition lives.
func (m Manager) Path() (string, error) {
	b, err := m.backend()
	if err != nil {
		return "", err
	}
	return b.path(m), nil
}

// Install writes the definition and (re)starts the service, so running it
// again after replacing the binary picks up the new one. If the start
// fails, the previous definition is put back and restarted; a definition
// this call created is removed with everything it set up.
func (m Manager) Install() error {
	b, err := m.backend()
	if err != nil {
		return err
	}
	def, err := b.render(m)
	if err != nil {
		return err
	}
	if err := b.prepare(m); err != nil {
		return err
	}
	path := b.path(m)
	prev, readErr := os.ReadFile(path) //nolint:gosec // our own service definition
	hadPrev := readErr == nil
	if err := fileedit.WriteAtomic(path, def, 0o644); err != nil {
		return fmt.Errorf("service: write %s: %w", path, err)
	}
	startErr := b.start(m, path)
	if startErr == nil {
		return nil
	}
	if hadPrev {
		if err := fileedit.WriteAtomic(path, prev, 0o644); err == nil {
			_ = b.start(m, path) // best effort: back to what worked before
		}
		return startErr
	}
	_ = b.stop(m)
	_ = os.Remove(path)
	b.afterRemove(m)
	return startErr
}

// Uninstall stops the service and removes its definition. It is a no-op
// when the service isn't installed, and fails, leaving the definition in
// place, when the service can't be stopped: the user must not be told
// collection stopped while a daemon still runs.
func (m Manager) Uninstall() error {
	b, err := m.backend()
	if err != nil {
		return err
	}
	if err := b.stop(m); err != nil {
		return err
	}
	path := b.path(m)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("service: remove %s: %w", path, err)
	}
	b.afterRemove(m)
	return nil
}

// Status reports whether the service is installed and running. It returns
// an error when the system can't be asked (no user bus, for example).
func (m Manager) Status() (Status, error) {
	b, err := m.backend()
	if err != nil {
		return Status{}, err
	}
	var st Status
	if _, err := os.Stat(b.path(m)); err == nil {
		st.Installed = true
	}
	return b.status(m, st)
}

// LogHint says where to look when the service doesn't stay up.
func (m Manager) LogHint() string {
	b, err := m.backend()
	if err != nil {
		return ""
	}
	return b.logHint(m)
}

func (m Manager) sleep(d time.Duration) {
	if m.Sleep != nil {
		m.Sleep(d)
		return
	}
	time.Sleep(d)
}

// run runs one command and turns a failure into an error carrying its
// output.
func (m Manager) run(name string, args ...string) error {
	if out, err := m.Run.Run(name, args...); err != nil {
		return cmdError(out, err, name, args...)
	}
	return nil
}

// cmdError describes a failed command with its output, and a hint for the
// common no-session case.
func cmdError(out string, err error, name string, args ...string) error {
	msg := fmt.Sprintf("service: %s %s: %v", name, strings.Join(args, " "), err)
	if o := strings.TrimSpace(out); o != "" {
		msg += ": " + o
	}
	if strings.Contains(out, "Failed to connect to bus") {
		msg += " (run this from your desktop session; su, sudo and plain SSH sessions have no user bus)"
	}
	return errors.New(msg)
}

// notLoaded reports output saying the service wasn't loaded or doesn't
// exist, which stopping treats as already stopped.
func notLoaded(out string) bool {
	o := strings.ToLower(out)
	for _, s := range []string{"no such process", "could not find", "not loaded", "does not exist", "not found"} {
		if strings.Contains(o, s) {
			return true
		}
	}
	return false
}

// launchd is the macOS backend: a LaunchAgent in the user's GUI domain.
type launchd struct{}

func (launchd) path(m Manager) string {
	return filepath.Join(m.Home, "Library", "LaunchAgents", Label+".plist")
}

func (launchd) render(m Manager) ([]byte, error) { return RenderPlist(m.Bin, m.Args, m.Env, m.LogDir) }

// prepare creates the log folder and the crash log, owner-only, before
// launchd needs them: it won't create a missing folder for StandardErrorPath
// and would create the file world-readable.
func (launchd) prepare(m Manager) error {
	if err := paths.EnsurePrivateDir(m.LogDir); err != nil {
		return fmt.Errorf("service: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(m.LogDir, paths.CrashLogFile), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) //nolint:gosec // our own log directory
	if err != nil {
		return fmt.Errorf("service: %w", err)
	}
	return f.Close()
}

func (l launchd) start(m Manager, path string) error {
	target := l.target(m)
	_, _ = m.Run.Run("launchctl", "bootout", target) // not loaded is fine
	// bootout returns before the old job is gone; bootstrapping too early
	// fails with "Input/output error".
	for range 50 {
		if _, err := m.Run.Run("launchctl", "print", target); err != nil {
			break
		}
		m.sleep(100 * time.Millisecond)
	}
	if err := m.run("launchctl", "enable", target); err != nil {
		return err
	}
	var err error
	for attempt := range 3 {
		if attempt > 0 {
			m.sleep(time.Second)
		}
		if err = m.run("launchctl", "bootstrap", l.domain(m), path); err == nil {
			return nil
		}
	}
	return err
}

func (l launchd) stop(m Manager) error {
	out, err := m.Run.Run("launchctl", "bootout", l.target(m))
	if err == nil || notLoaded(out) {
		return nil
	}
	return cmdError(out, err, "launchctl", "bootout", l.target(m))
}

func (launchd) afterRemove(Manager) {}

func (l launchd) status(m Manager, st Status) (Status, error) {
	out, err := m.Run.Run("launchctl", "print", l.target(m))
	if err != nil {
		return st, nil // not loaded
	}
	for _, line := range strings.Split(out, "\n") {
		if pid, ok := strings.CutPrefix(strings.TrimSpace(line), "pid = "); ok {
			st.Running, st.Detail = true, "pid "+pid
			return st, nil
		}
	}
	st.Detail = "loaded, not running"
	return st, nil
}

func (launchd) logHint(m Manager) string { return filepath.Join(m.LogDir, paths.CrashLogFile) }

func (launchd) domain(m Manager) string   { return fmt.Sprintf("gui/%d", m.UID) }
func (l launchd) target(m Manager) string { return l.domain(m) + "/" + Label }

// systemd is the Linux backend: a unit in the user's systemd instance.
type systemd struct{}

func (systemd) path(m Manager) string {
	return filepath.Join(m.Config, "systemd", "user", UnitName)
}

func (systemd) render(m Manager) ([]byte, error) { return RenderUnit(m.Bin, m.Args, m.Env) }

func (systemd) prepare(Manager) error { return nil } // output goes to the journal

func (systemd) start(m Manager, _ string) error {
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", UnitName},
	} {
		if err := m.run("systemctl", args...); err != nil {
			return err
		}
	}
	// A unit that hit its start limit refuses manual starts until reset.
	_, _ = m.Run.Run("systemctl", "--user", "reset-failed", UnitName)
	return m.run("systemctl", "--user", "restart", UnitName)
}

func (systemd) stop(m Manager) error {
	args := []string{"--user", "disable", "--now", UnitName}
	out, err := m.Run.Run("systemctl", args...)
	if err == nil || notLoaded(out) {
		return nil
	}
	return cmdError(out, err, "systemctl", args...)
}

func (systemd) afterRemove(m Manager) { _, _ = m.Run.Run("systemctl", "--user", "daemon-reload") }

// unitStates are the answers `systemctl is-active` gives when it could
// ask; anything else means it couldn't.
var unitStates = []string{"active", "reloading", "inactive", "failed", "activating", "deactivating", "unknown"}

func (systemd) status(m Manager, st Status) (Status, error) {
	args := []string{"--user", "is-active", UnitName}
	out, err := m.Run.Run("systemctl", args...)
	state := strings.TrimSpace(out)
	if !slices.Contains(unitStates, state) {
		if err == nil {
			err = errors.New("unexpected answer")
		}
		return st, cmdError(out, err, "systemctl", args...)
	}
	st.Detail, st.Running = state, state == "active"
	return st, nil
}

func (systemd) logHint(Manager) string { return "journalctl --user -u " + UnitName }

// checkValue rejects text a service definition can't hold safely: control
// characters anywhere, and characters the definition's quoting gives
// meaning to (reserved).
func checkValue(what, v, reserved string) error {
	for _, r := range v {
		if unicode.IsControl(r) || strings.ContainsRune(reserved, r) {
			return fmt.Errorf("service: %s %q contains %q, which the service definition can't hold; move it", what, v, r)
		}
	}
	return nil
}

func checkPath(what, p, reserved string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("service: %s %q is not absolute", what, p)
	}
	return checkValue(what, p, reserved)
}

// sortedKeys returns m's keys in order, so definitions render the same way
// every time.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
