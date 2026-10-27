//go:build !windows

package service

import (
	"encoding/xml"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeRunner records commands and answers from a script.
type fakeRunner struct {
	calls []string
	out   map[string]string // command → output
	fail  map[string]bool   // command → always fails
	failN map[string]int    // command → fails this many more times
}

func (f *fakeRunner) Run(name string, args ...string) (string, error) {
	c := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, c)
	if f.fail[c] {
		return f.out[c], errors.New("exit status 1")
	}
	if f.failN[c] > 0 {
		f.failN[c]--
		return f.out[c], errors.New("exit status 1")
	}
	return f.out[c], nil
}

func (f *fakeRunner) count(c string) int {
	n := 0
	for _, call := range f.calls {
		if call == c {
			n++
		}
	}
	return n
}

var testArgs = []string{"daemon", "--service"}

func manager(t *testing.T, goos string, r *fakeRunner) Manager {
	home := t.TempDir()
	return Manager{
		GOOS: goos, Bin: "/Users/me/.local/bin/burnout", Args: testArgs,
		Env:  map[string]string{"BURNOUT_DATA_DIR": "/Users/me/data"},
		Home: home, Config: filepath.Join(home, ".config"), LogDir: filepath.Join(home, "data", "logs"),
		UID: 501, Run: r, Sleep: func(time.Duration) {},
	}
}

const (
	lcTarget    = "gui/501/" + Label
	lcBootout   = "launchctl bootout " + lcTarget
	lcPrint     = "launchctl print " + lcTarget
	lcEnable    = "launchctl enable " + lcTarget
	scIsActive  = "systemctl --user is-active " + UnitName
	scReload    = "systemctl --user daemon-reload"
	scDisable   = "systemctl --user disable --now " + UnitName
	busFailure  = "Failed to connect to bus: No medium found"
	notLoadedLC = "Boot-out failed: 3: No such process"
)

func lcBootstrap(path string) string { return "launchctl bootstrap gui/501 " + path }

// launchdRunner answers like launchd with the agent not loaded.
func launchdRunner() *fakeRunner {
	return &fakeRunner{fail: map[string]bool{lcPrint: true}, failN: map[string]int{}, out: map[string]string{}}
}

func TestLaunchdInstallUninstall(t *testing.T) {
	r := launchdRunner()
	m := manager(t, "darwin", r)
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	path, _ := m.Path()
	want := []string{lcBootout, lcPrint, lcEnable, lcBootstrap(path)}
	if !slices.Equal(r.calls, want) {
		t.Fatalf("calls = %q, want %q", r.calls, want)
	}
	def, err := os.ReadFile(path) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"<string>/Users/me/.local/bin/burnout</string>\n\t\t<string>daemon</string>\n\t\t<string>--service</string>",
		"<key>BURNOUT_DATA_DIR</key>\n\t\t<string>/Users/me/data</string>",
	} {
		if !strings.Contains(string(def), s) {
			t.Fatalf("plist lacks %q:\n%s", s, def)
		}
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm()&0o022 != 0 {
		t.Fatalf("plist mode %v: launchd refuses group- or world-writable agents", fi.Mode().Perm())
	}
	// launchd neither creates the log folder nor makes the crash log private.
	if fi, err := os.Stat(m.LogDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("log dir: %v %v", fi, err)
	}
	if fi, err := os.Stat(m.LogHint()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("crash log: %v %v", fi, err)
	}

	r.calls = nil
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plist left behind")
	}
	r.fail[lcBootout], r.out[lcBootout] = true, notLoadedLC
	if err := m.Uninstall(); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
}

func TestLaunchdWaitsForTheOldJob(t *testing.T) {
	r := launchdRunner()
	delete(r.fail, lcPrint)
	r.failN[lcPrint] = 0
	var slept []time.Duration
	m := manager(t, "darwin", r)
	m.Sleep = func(d time.Duration) {
		slept = append(slept, d)
		if len(slept) == 2 {
			r.fail[lcPrint] = true // the old job is gone
		}
	}
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	if n := r.count(lcPrint); n != 3 {
		t.Fatalf("print called %d times, want 3: %q", n, r.calls)
	}
	if i, j := slices.Index(r.calls, lcEnable), slices.Index(r.calls, lcPrint); i < j {
		t.Fatalf("enabled before the old job was gone: %q", r.calls)
	}
}

func TestLaunchdRetriesBootstrap(t *testing.T) {
	r := launchdRunner()
	m := manager(t, "darwin", r)
	path, _ := m.Path()
	r.failN[lcBootstrap(path)] = 2
	r.out[lcBootstrap(path)] = "Bootstrap failed: 5: Input/output error"
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	if n := r.count(lcBootstrap(path)); n != 3 {
		t.Fatalf("bootstrap called %d times, want 3", n)
	}
}

func TestFailedUpgradeRestoresThePreviousService(t *testing.T) {
	r := launchdRunner()
	m := manager(t, "darwin", r)
	path, _ := m.Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.failN[lcBootstrap(path)] = 3 // the new definition never starts
	err := m.Install()
	if err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "previous" { //nolint:gosec // test file
		t.Fatalf("definition = %q, want the previous one back", b)
	}
	if n := r.count(lcBootstrap(path)); n != 4 {
		t.Fatalf("bootstrap called %d times, want 3 tries and the restart", n)
	}
}

func TestFailedNewInstallCleansUp(t *testing.T) {
	r := &fakeRunner{
		fail: map[string]bool{scReload: true, scIsActive: true},
		out:  map[string]string{scReload: busFailure, scIsActive: busFailure},
	}
	m := manager(t, "linux", r)
	err := m.Install()
	if err == nil || !strings.Contains(err.Error(), "No medium found") || !strings.Contains(err.Error(), "desktop session") {
		t.Fatalf("err = %v", err)
	}
	path, _ := m.Path()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unit file left behind after a failed install")
	}
	if !slices.Contains(r.calls, scDisable) {
		t.Fatalf("not stopped: %q", r.calls)
	}
	// Status can't ask either, and says so rather than "not installed".
	if _, err := m.Status(); err == nil || !strings.Contains(err.Error(), "No medium found") {
		t.Fatalf("status err = %v", err)
	}
}

func TestUninstallKeepsAServiceItCannotStop(t *testing.T) {
	r := launchdRunner()
	m := manager(t, "darwin", r)
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	r.fail[lcBootout], r.out[lcBootout] = true, "Boot-out failed: 1: Operation not permitted"
	if err := m.Uninstall(); err == nil || !strings.Contains(err.Error(), "Operation not permitted") {
		t.Fatalf("err = %v", err)
	}
	path, _ := m.Path()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("definition removed while the service still runs: %v", err)
	}
}

func TestLaunchdStatus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		r       *fakeRunner
		running bool
		detail  string
	}{
		{"running", &fakeRunner{out: map[string]string{lcPrint: "{\n\tstate = running\n\tpid = 812\n}"}}, true, "pid 812"},
		{"loaded, exited", &fakeRunner{out: map[string]string{lcPrint: "{\n\tstate = not running\n}"}}, false, "loaded, not running"},
		{"not loaded", &fakeRunner{fail: map[string]bool{lcPrint: true}}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := manager(t, "darwin", tc.r).Status()
			if err != nil || st.Running != tc.running || st.Detail != tc.detail {
				t.Fatalf("status = %+v, %v", st, err)
			}
		})
	}
}

func TestSystemdInstallUninstall(t *testing.T) {
	r := &fakeRunner{}
	m := manager(t, "linux", r)
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		scReload,
		"systemctl --user enable " + UnitName,
		"systemctl --user reset-failed " + UnitName,
		"systemctl --user restart " + UnitName,
	}
	if !slices.Equal(r.calls, want) {
		t.Fatalf("calls = %q", r.calls)
	}
	path, _ := m.Path()
	def, _ := os.ReadFile(path) //nolint:gosec // test file
	for _, s := range []string{
		`ExecStart="/Users/me/.local/bin/burnout" daemon --service`,
		`Environment="BURNOUT_DATA_DIR=/Users/me/data"`,
		"StartLimitIntervalSec=0", "Restart=on-failure", "RestartSec=30",
		"Type=notify", "WatchdogSec=60",
	} {
		if !strings.Contains(string(def), s) {
			t.Fatalf("unit lacks %q:\n%s", s, def)
		}
	}
	r.calls = nil
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.calls, []string{scDisable, scReload}) {
		t.Fatalf("calls = %q", r.calls)
	}
}

func TestSystemdUninstallWhenNotInstalled(t *testing.T) {
	r := &fakeRunner{
		fail: map[string]bool{scDisable: true},
		out:  map[string]string{scDisable: "Failed to disable unit: Unit file burnout.service does not exist."},
	}
	if err := manager(t, "linux", r).Uninstall(); err != nil {
		t.Fatal(err)
	}
}

func TestSystemdStatus(t *testing.T) {
	for _, tc := range []struct {
		out     string
		fail    bool
		running bool
		wantErr bool
	}{
		{"active\n", false, true, false},
		{"inactive\n", true, false, false},
		{"failed\n", true, false, false},
		{busFailure + "\n", true, false, true},
	} {
		r := &fakeRunner{out: map[string]string{scIsActive: tc.out}, fail: map[string]bool{scIsActive: tc.fail}}
		st, err := manager(t, "linux", r).Status()
		if (err != nil) != tc.wantErr || st.Running != tc.running {
			t.Errorf("is-active %q: status = %+v, err = %v", tc.out, st, err)
		}
	}
}

func TestLogHint(t *testing.T) {
	if h := manager(t, "linux", &fakeRunner{}).LogHint(); h != "journalctl --user -u "+UnitName {
		t.Errorf("linux hint %q", h)
	}
	if h := manager(t, "darwin", &fakeRunner{}).LogHint(); !strings.HasSuffix(h, "stderr.log") {
		t.Errorf("macOS hint %q", h)
	}
}

func TestPlistIsValidAndEscaped(t *testing.T) {
	def, err := RenderPlist("/Applications/R&D <tools>/burnout", testArgs,
		map[string]string{"BURNOUT_DATA_DIR": "/Users/me/A&B"}, "/Users/me/Library/Application Support/burnout/logs")
	if err != nil {
		t.Fatal(err)
	}
	var v struct{}
	if err := xml.Unmarshal(def, &v); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, def)
	}
	s := string(def)
	for _, want := range []string{
		"<string>/Applications/R&amp;D &lt;tools&gt;/burnout</string>",
		"<string>/Users/me/A&amp;B</string>",
		"<key>KeepAlive</key>", "<key>SuccessfulExit</key>", "<false/>",
		"<string>Aqua</string>",
		"<string>/Users/me/Library/Application Support/burnout/logs/stderr.log</string>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		return
	}
	path := filepath.Join(t.TempDir(), "agent.plist")
	if err := os.WriteFile(path, def, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(plutil, "-lint", path).CombinedOutput(); err != nil { //nolint:gosec // test tool
		t.Fatalf("plutil -lint: %v\n%s", err, out)
	}
}

// TestUnitVerifies runs systemd's own checker on the unit where it exists.
func TestUnitVerifies(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("no systemd-analyze")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "My Tools", "burnout")
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // a test executable
		t.Fatal(err)
	}
	def, err := RenderUnit(bin, testArgs, map[string]string{"BURNOUT_DATA_DIR": "/home/me/my data"})
	if err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(dir, UnitName)
	if err := os.WriteFile(unit, def, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(analyze, "verify", unit).CombinedOutput(); err != nil { //nolint:gosec // test tool
		t.Fatalf("systemd-analyze verify: %v\n%s\n%s", err, out, def)
	}
}

func TestRejectsUnsafeValues(t *testing.T) {
	env := func(v string) map[string]string { return map[string]string{"BURNOUT_DATA_DIR": v} }
	for name, f := range map[string]func() error{
		"plist relative bin":   func() error { _, err := RenderPlist("relative/burnout", testArgs, nil, "/tmp/logs"); return err },
		"plist newline in bin": func() error { _, err := RenderPlist("/tmp/a\nb/burnout", testArgs, nil, "/tmp/logs"); return err },
		"plist relative logs":  func() error { _, err := RenderPlist("/opt/burnout", testArgs, nil, "logs"); return err },
		"plist control in env": func() error { _, err := RenderPlist("/opt/burnout", testArgs, env("/a\x00b"), "/tmp/logs"); return err },
		"unit space in arg":    func() error { _, err := RenderUnit("/opt/burnout", []string{"daemon", "a b"}, nil); return err },
		"unit $ in env":        func() error { _, err := RenderUnit("/opt/burnout", testArgs, env("/home/$USER")); return err },
		"unit newline in env":  func() error { _, err := RenderUnit("/opt/burnout", testArgs, env("/a\nExecStart=/x")); return err },
	} {
		if f() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, bin := range []string{`/opt/a"b/burnout`, "/opt/100%/burnout", "/opt/$HOME/burnout", `/opt/a\b/burnout`, "/opt/it's/burnout", "/opt/a`b`/burnout"} {
		if _, err := RenderUnit(bin, testArgs, nil); err == nil {
			t.Errorf("unit accepted %q", bin)
		}
	}
	if _, err := RenderUnit("/home/me/My Tools/burnout", testArgs, env("/home/me/my data")); err != nil {
		t.Errorf("unit rejected a path with a space: %v", err)
	}
}

func TestUnsupportedPlatform(t *testing.T) {
	m := manager(t, "windows", &fakeRunner{})
	if err := m.Install(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}
