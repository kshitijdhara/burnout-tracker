//go:build !windows

package service

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeRunner records commands and answers from a script.
type fakeRunner struct {
	calls []string
	out   map[string]string // command → output
	fail  map[string]bool   // command → fails
}

func (f *fakeRunner) Run(name string, args ...string) (string, error) {
	c := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, c)
	if f.fail[c] {
		return f.out[c], errors.New("exit status 1")
	}
	return f.out[c], nil
}

func manager(t *testing.T, goos string, r *fakeRunner) Manager {
	home := t.TempDir()
	return Manager{
		GOOS: goos, Bin: "/Users/me/.local/bin/burnout", Home: home,
		Config: filepath.Join(home, ".config"), LogDir: filepath.Join(home, "data", "logs"),
		UID: 501, Run: r,
	}
}

func TestLaunchdInstallUninstall(t *testing.T) {
	r := &fakeRunner{fail: map[string]bool{"launchctl bootout gui/501/" + Label: true}} // not loaded yet
	m := manager(t, "darwin", r)
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	path, _ := m.Path()
	want := []string{
		"launchctl bootout gui/501/" + Label,
		"launchctl enable gui/501/" + Label,
		"launchctl bootstrap gui/501 " + path,
	}
	if !slices.Equal(r.calls, want) {
		t.Fatalf("calls = %q, want %q", r.calls, want)
	}
	def, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(def), "<string>/Users/me/.local/bin/burnout</string>") {
		t.Fatalf("plist %s: %v\n%s", path, err, def)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm()&0o022 != 0 {
		t.Fatalf("plist mode %v: launchd refuses group- or world-writable agents", fi.Mode().Perm())
	}

	r.calls = nil
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plist left behind")
	}
	if err := m.Uninstall(); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
}

func TestLaunchdStatus(t *testing.T) {
	target := "launchctl print gui/501/" + Label
	for _, tc := range []struct {
		name    string
		r       *fakeRunner
		running bool
		detail  string
	}{
		{"running", &fakeRunner{out: map[string]string{target: "{\n\tstate = running\n\tpid = 812\n}"}}, true, "pid 812"},
		{"loaded, exited", &fakeRunner{out: map[string]string{target: "{\n\tstate = not running\n}"}}, false, "loaded, not running"},
		{"not loaded", &fakeRunner{fail: map[string]bool{target: true}}, false, ""},
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
		"systemctl --user daemon-reload",
		"systemctl --user enable " + UnitName,
		"systemctl --user restart " + UnitName,
	}
	if !slices.Equal(r.calls, want) {
		t.Fatalf("calls = %q", r.calls)
	}
	path, _ := m.Path()
	if def, _ := os.ReadFile(path); !strings.Contains(string(def), `ExecStart="/Users/me/.local/bin/burnout" daemon --log-file`) {
		t.Fatalf("unit:\n%s", def)
	}
	r.calls = nil
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.calls, []string{"systemctl --user disable --now " + UnitName, "systemctl --user daemon-reload"}) {
		t.Fatalf("calls = %q", r.calls)
	}
}

func TestInstallReportsFailure(t *testing.T) {
	r := &fakeRunner{
		fail: map[string]bool{"systemctl --user daemon-reload": true},
		out:  map[string]string{"systemctl --user daemon-reload": "Failed to connect to bus"},
	}
	m := manager(t, "linux", r)
	err := m.Install()
	if err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Fatalf("err = %v", err)
	}
	// Nothing half set up: the definition is gone, and status says so.
	path, _ := m.Path()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unit file left behind after a failed install")
	}
	if st, _ := m.Status(); st.Installed {
		t.Fatalf("status = %+v", st)
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
	def, err := RenderPlist("/Applications/R&D <tools>/burnout", "/Users/me/Library/Application Support/burnout/logs")
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
		"<key>KeepAlive</key>", "<key>SuccessfulExit</key>", "<false/>",
		"<string>Aqua</string>",
		"<string>/Users/me/Library/Application Support/burnout/logs/stderr.log</string>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
}

func TestRejectsUnsafePaths(t *testing.T) {
	for _, bin := range []string{"relative/burnout", "/tmp/a\nb/burnout"} {
		if _, err := RenderPlist(bin, "/tmp/logs"); err == nil {
			t.Errorf("plist accepted %q", bin)
		}
	}
	for _, bin := range []string{`/opt/a"b/burnout`, "/opt/100%/burnout", "/opt/$HOME/burnout", `/opt/a\b/burnout`} {
		if _, err := RenderUnit(bin); err == nil {
			t.Errorf("unit accepted %q", bin)
		}
	}
	if _, err := RenderUnit("/home/me/My Tools/burnout"); err != nil {
		t.Errorf("unit rejected a path with a space: %v", err)
	}
}

func TestUnsupportedPlatform(t *testing.T) {
	m := manager(t, "windows", &fakeRunner{})
	if err := m.Install(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}
