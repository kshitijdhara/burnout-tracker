// Package service installs the daemon as a per-user background service
// that starts at login and restarts after a crash (ADR-0005, system design
// §11.2): a launchd LaunchAgent on macOS, a systemd --user unit on Linux.
// Rendering the definitions is pure; changing the system goes through a
// Runner, so tests never touch launchctl or systemctl.
package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/fileedit"
)

// Names of the service in each system.
const (
	Label    = "io.github.kshitijdhara.burnout-tracker" // launchd
	UnitName = "burnout.service"                        // systemd
)

// DaemonArgs is how the service runs the daemon: logging to a rotating file
// in the data folder instead of a terminal.
var DaemonArgs = []string{"daemon", "--log-file"}

// ErrUnsupported is returned on platforms without a service yet (Windows).
var ErrUnsupported = errors.New("service: not available on this platform yet; run `burnout daemon` in a terminal")

// Runner runs a system command and returns its combined output.
type Runner interface {
	Run(name string, args ...string) (string, error)
}

// ExecRunner runs commands for real.
type ExecRunner struct{}

// Run executes name with args (no shell) and returns its combined output.
func (ExecRunner) Run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput() //nolint:gosec // fixed launchctl/systemctl invocations
	return string(out), err
}

// Manager installs, removes and inspects the service for one user.
type Manager struct {
	GOOS   string
	Bin    string // absolute path of the burnout binary
	Home   string // the user's home directory
	Config string // XDG config directory on Linux (os.UserConfigDir)
	LogDir string // where the service's crash output goes
	UID    int
	Run    Runner
}

// Status is what the system reports about the service.
type Status struct {
	Installed bool   // the definition file exists
	Running   bool   // the daemon process is up
	Detail    string // e.g. "pid 812" or the system's state
}

// Path returns where the service definition lives.
func (m Manager) Path() (string, error) {
	switch m.GOOS {
	case "darwin":
		return filepath.Join(m.Home, "Library", "LaunchAgents", Label+".plist"), nil
	case "linux":
		return filepath.Join(m.Config, "systemd", "user", UnitName), nil
	default:
		return "", ErrUnsupported
	}
}

// Install writes the definition and (re)starts the service, so running it
// again after replacing the binary picks up the new one. If the system
// refuses, the definition is removed again, so nothing is left half set up.
func (m Manager) Install() error {
	path, err := m.Path()
	if err != nil {
		return err
	}
	var def []byte
	switch m.GOOS {
	case "darwin":
		def, err = RenderPlist(m.Bin, m.LogDir)
	default:
		def, err = RenderUnit(m.Bin)
	}
	if err != nil {
		return err
	}
	if err := fileedit.WriteAtomic(path, def, 0o644); err != nil {
		return fmt.Errorf("service: write %s: %w", path, err)
	}
	if err := m.start(path); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func (m Manager) start(path string) error {
	if m.GOOS == "darwin" {
		target := m.launchdTarget()
		_, _ = m.Run.Run("launchctl", "bootout", target) // not loaded yet is fine
		return m.steps(
			[]string{"launchctl", "enable", target},
			[]string{"launchctl", "bootstrap", m.launchdDomain(), path},
		)
	}
	return m.steps(
		[]string{"systemctl", "--user", "daemon-reload"},
		[]string{"systemctl", "--user", "enable", UnitName},
		[]string{"systemctl", "--user", "restart", UnitName},
	)
}

// LogHint says where to look when the service doesn't stay up: the crash
// output launchd keeps, or systemd's journal.
func (m Manager) LogHint() string {
	if m.GOOS == "darwin" {
		return filepath.Join(m.LogDir, "stderr.log")
	}
	return "journalctl --user -u " + UnitName
}

// Uninstall stops the service and removes its definition. It is a no-op
// when the service isn't installed.
func (m Manager) Uninstall() error {
	path, err := m.Path()
	if err != nil {
		return err
	}
	if m.GOOS == "darwin" {
		_, _ = m.Run.Run("launchctl", "bootout", m.launchdTarget()) // not loaded is fine
	} else {
		_, _ = m.Run.Run("systemctl", "--user", "disable", "--now", UnitName)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("service: remove %s: %w", path, err)
	}
	if m.GOOS == "linux" {
		_, _ = m.Run.Run("systemctl", "--user", "daemon-reload")
	}
	return nil
}

var launchdPID = regexp.MustCompile(`(?m)^\s*pid = (\d+)`)

// Status reports whether the service is installed and running.
func (m Manager) Status() (Status, error) {
	path, err := m.Path()
	if err != nil {
		return Status{}, err
	}
	var st Status
	if _, err := os.Stat(path); err == nil {
		st.Installed = true
	}
	if m.GOOS == "darwin" {
		out, err := m.Run.Run("launchctl", "print", m.launchdTarget())
		if err != nil {
			return st, nil // not loaded
		}
		if p := launchdPID.FindStringSubmatch(out); p != nil {
			st.Running, st.Detail = true, "pid "+p[1]
		} else {
			st.Detail = "loaded, not running"
		}
		return st, nil
	}
	out, _ := m.Run.Run("systemctl", "--user", "is-active", UnitName)
	st.Detail = strings.TrimSpace(out)
	st.Running = st.Detail == "active"
	return st, nil
}

func (m Manager) launchdDomain() string { return fmt.Sprintf("gui/%d", m.UID) }
func (m Manager) launchdTarget() string { return m.launchdDomain() + "/" + Label }

// steps runs commands in order and stops at the first failure.
func (m Manager) steps(cmds ...[]string) error {
	for _, c := range cmds {
		if out, err := m.Run.Run(c[0], c[1:]...); err != nil {
			return fmt.Errorf("service: %s: %w: %s", strings.Join(c, " "), err, strings.TrimSpace(out))
		}
	}
	return nil
}

// checkPath rejects paths a service definition can't hold safely: control
// characters anywhere, and for systemd the characters its quoting gives
// meaning to.
func checkPath(p, reserved string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("service: path %q is not absolute", p)
	}
	for _, r := range p {
		if unicode.IsControl(r) || strings.ContainsRune(reserved, r) {
			return fmt.Errorf("service: path %q contains %q, which the service definition can't hold; move the binary", p, r)
		}
	}
	return nil
}

// RenderPlist returns the LaunchAgent: run at login, restart after a crash
// (not after a clean exit), at most every 30 s, only in a GUI session.
// Crash output (a Go panic) goes to logDir; logs go to the rotating file.
func RenderPlist(bin, logDir string) ([]byte, error) {
	if err := checkPath(bin, ""); err != nil {
		return nil, err
	}
	if err := checkPath(logDir, ""); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	esc := func(s string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(s))
		return e.String()
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + esc(bin) + `</string>
`)
	for _, a := range DaemonArgs {
		b.WriteString("\t\t<string>" + esc(a) + "</string>\n")
	}
	b.WriteString(`	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
	<key>StandardOutPath</key>
	<string>/dev/null</string>
	<key>StandardErrorPath</key>
	<string>` + esc(filepath.Join(logDir, "stderr.log")) + `</string>
</dict>
</plist>
`)
	return b.Bytes(), nil
}

// RenderUnit returns the systemd user unit: started with the user session,
// restarted after a crash, at most 5 times in 10 minutes.
func RenderUnit(bin string) ([]byte, error) {
	if err := checkPath(bin, "\"\\%$`"); err != nil {
		return nil, err
	}
	return []byte(`[Unit]
Description=burnout-tracker daemon (local only; nothing leaves this device)
StartLimitIntervalSec=600
StartLimitBurst=5

[Service]
ExecStart="` + bin + `" ` + strings.Join(DaemonArgs, " ") + `
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
`), nil
}
