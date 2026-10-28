// Package service installs the daemon as a per-user background service
// that starts at login and restarts after a crash (ADR-0005, system design
// §11.2): a launchd LaunchAgent on macOS, a systemd --user unit on Linux,
// a logon task in Task Scheduler on Windows. Rendering the definitions is
// pure; changing the system goes through a Runner, so tests never touch
// launchctl, systemctl or schtasks.
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

	"github.com/kshitijdhara/burntout/internal/adapters/integrations/fileedit"
	"github.com/kshitijdhara/burntout/internal/adapters/paths"
)

// Names of the service in each system.
const (
	Label    = "io.github.kshitijdhara.burntout" // launchd
	UnitName = "burntout.service"                // systemd
)

// The names the service had before the app was renamed to BurntOut, which
// a Legacy manager removes.
const (
	LegacyLabel    = "io.github.kshitijdhara.burnout-tracker"
	LegacyUnitName = "burnout.service"
	legacyTaskName = "burnout-tracker-"
)

// ErrUnsupported is returned on platforms without a service (the BSDs).
var ErrUnsupported = errors.New("service: not available on this platform yet")

// Runner runs a system command and returns its combined output.
type Runner interface {
	Run(name string, args ...string) (string, error)
}

// commandTimeout bounds one launchctl or systemctl call: a stuck launchd
// or user bus must not hang an install, an uninstall or doctor.
const commandTimeout = 30 * time.Second

// ExecRunner runs commands for real, with a timeout, and launchctl,
// schtasks and powershell by their absolute paths, so a same-named program
// earlier in PATH or in the current folder can't stand in for them.
type ExecRunner struct{}

// Run executes name with args (no shell) and returns its combined output.
func (ExecRunner) Run(name string, args ...string) (string, error) {
	switch name {
	case "launchctl":
		name = "/bin/launchctl"
	case "schtasks":
		name = filepath.Join(systemRoot(), "System32", "schtasks.exe")
	case "powershell":
		name = filepath.Join(systemRoot(), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // fixed launchctl/systemctl invocations
	return string(out), err
}

func systemRoot() string {
	if r := os.Getenv("SystemRoot"); r != "" {
		return r
	}
	return `C:\Windows`
}

// Manager installs, removes and inspects the service for one user.
type Manager struct {
	GOOS string
	Bin  string   // absolute path of the BurntOut binary
	Args []string // the daemon's arguments, e.g. daemon --service
	// Env pins the daemon's locations to the ones the installing shell
	// resolved, so the service and the CLI always agree.
	Env    map[string]string
	Home   string // the user's home directory
	Config string // XDG config directory on Linux (os.UserConfigDir)
	LogDir string // where launchd writes the daemon's crash output
	UID    int
	SID    string // the user's SID on Windows, which the logon task runs as
	Run    Runner
	// Sleep waits between retries; nil means time.Sleep. Tests stub it.
	Sleep func(time.Duration)
	// legacy points the manager at the pre-rename service; see Legacy.
	legacy bool
}

// Legacy returns m pointed at the service as it was named before the
// rename (burnout-tracker), so Status and Uninstall find and remove it.
func (m Manager) Legacy() Manager {
	m.legacy = true
	return m
}

func (m Manager) label() string {
	if m.legacy {
		return LegacyLabel
	}
	return Label
}

func (m Manager) unit() string {
	if m.legacy {
		return LegacyUnitName
	}
	return UnitName
}

func (m Manager) appDir() string {
	if m.legacy {
		return paths.LegacyAppName
	}
	return paths.AppName
}

func (m Manager) taskName() string {
	if m.legacy {
		return legacyTaskName + m.SID
	}
	return TaskName(m.SID)
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
	case "windows":
		return taskScheduler{}, nil
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
	return filepath.Join(m.Home, "Library", "LaunchAgents", m.label()+".plist")
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
func (l launchd) target(m Manager) string { return l.domain(m) + "/" + m.label() }

// systemd is the Linux backend: a unit in the user's systemd instance.
type systemd struct{}

func (systemd) path(m Manager) string {
	return filepath.Join(m.Config, "systemd", "user", m.unit())
}

func (systemd) render(m Manager) ([]byte, error) { return RenderUnit(m.Bin, m.Args, m.Env) }

func (systemd) prepare(Manager) error { return nil } // output goes to the journal

func (systemd) start(m Manager, _ string) error {
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", m.unit()},
	} {
		if err := m.run("systemctl", args...); err != nil {
			return err
		}
	}
	// A unit that hit its start limit refuses manual starts until reset.
	_, _ = m.Run.Run("systemctl", "--user", "reset-failed", m.unit())
	return m.run("systemctl", "--user", "restart", m.unit())
}

func (systemd) stop(m Manager) error {
	args := []string{"--user", "disable", "--now", m.unit()}
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
	args := []string{"--user", "is-active", m.unit()}
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

func (systemd) logHint(m Manager) string { return "journalctl --user -u " + m.unit() }

// taskScheduler is the Windows backend: a logon task for the current user,
// which needs no admin rights. The XML it was imported from stays next to
// the config, so a failed upgrade can import the previous one again.
// schtasks' messages are translated, so only exit codes are relied on.
type taskScheduler struct{}

func (taskScheduler) path(m Manager) string {
	return filepath.Join(m.Config, m.appDir(), "service-task.xml")
}

// render refuses folder overrides: a task's action has no environment, so
// the daemon would use the default folders while the CLI uses others.
func (taskScheduler) render(m Manager) ([]byte, error) {
	if len(m.Env) > 0 {
		return nil, fmt.Errorf("service: the logon task can't carry %s; unset it, or run `burntout daemon` in a terminal instead",
			strings.Join(sortedKeys(m.Env), ", "))
	}
	return RenderTask(m.Bin, m.Args, m.SID)
}

// prepare creates the crash log the daemon sends Go's crash output to:
// Task Scheduler keeps no output.
func (taskScheduler) prepare(m Manager) error { return launchd{}.prepare(m) }

func (taskScheduler) start(m Manager, path string) error {
	name := m.taskName()
	if err := m.run("schtasks", "/Create", "/XML", path, "/TN", name, "/F"); err != nil {
		return err
	}
	// The task stops a running instance before starting the new one, so
	// there's no /End here: Task Scheduler could still list the old one
	// when /Run arrives.
	return m.run("schtasks", "/Run", "/TN", name)
}

func (t taskScheduler) stop(m Manager) error {
	name := m.taskName()
	if !t.exists(m) {
		return nil
	}
	_, _ = m.Run.Run("schtasks", "/End", "/TN", name)
	return m.run("schtasks", "/Delete", "/TN", name, "/F")
}

func (taskScheduler) afterRemove(Manager) {}

func (taskScheduler) exists(m Manager) bool {
	_, err := m.Run.Run("schtasks", "/Query", "/TN", m.taskName())
	return err == nil
}

// taskStateScript prints the task's state by its enum name (Ready,
// Running, Disabled…), which unlike schtasks' status column isn't
// translated.
const taskStateScript = "(Get-ScheduledTask -TaskPath '\\' -TaskName '%s' -ErrorAction Stop).State"

// taskStates are the answers Get-ScheduledTask gives.
var taskStates = []string{"Unknown", "Disabled", "Queued", "Ready", "Running"}

func (t taskScheduler) status(m Manager, st Status) (Status, error) {
	// A task deleted in Task Scheduler's window is gone, whatever the
	// XML file says.
	if st.Installed = t.exists(m); !st.Installed {
		return st, nil
	}
	args := []string{"-NoProfile", "-NonInteractive", "-Command", fmt.Sprintf(taskStateScript, m.taskName())}
	out, err := m.Run.Run("powershell", args...)
	state := strings.TrimSpace(out)
	if err != nil || !slices.Contains(taskStates, state) {
		if err == nil {
			err = errors.New("unexpected answer")
		}
		return st, cmdError(out, err, "powershell", args...)
	}
	st.Detail, st.Running = "task "+strings.ToLower(state), state == "Running"
	return st, nil
}

func (taskScheduler) logHint(m Manager) string { return launchd{}.logHint(m) }

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
