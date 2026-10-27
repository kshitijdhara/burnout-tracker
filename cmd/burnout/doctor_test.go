package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/classify"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// TestBundleCanary is the privacy canary for `doctor --bundle` (privacy &
// security §10): the home folder and an excluded app, planted in every
// file the bundle draws on, and a window title and command arguments the
// collector saw, must not appear in the archive.
func TestBundleCanary(t *testing.T) {
	const (
		titleCanary = "CANARYtitle31"
		argvCanary  = "CANARYargv31"
		privateApp  = "com.Canary.Private31"
	)
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home-CANARYhome31")
	dataDir, configDir := filepath.Join(home, "data"), filepath.Join(home, "config")
	for _, d := range []string{paths.LogDir(dataDir), configDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows' home folder
	t.Setenv(paths.EnvDataDir, dataDir)
	t.Setenv(paths.EnvConfigDir, configDir)

	cfg := config.Default()
	cfg.Privacy.ExcludeApps = []string{privateApp}
	if err := os.WriteFile(config.Path(configDir), config.Render(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	for i := range bundleLogLines + 50 {
		if i == 0 {
			log.WriteString("too old to be in the bundle " + home + "\n")
			continue
		}
		log.WriteString("level=INFO msg=tick app=" + strings.ToUpper(privateApp) + " dir=" + home + "\n")
	}
	if err := os.WriteFile(filepath.Join(paths.LogDir(dataDir), paths.DaemonLogFile), []byte(log.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	// What doctor reports about the screen: the excluded app in a call,
	// with a title and arguments the collector should never pass on.
	snap := model.Snapshot{Windows: []model.Window{
		{AppID: strings.ToLower(privateApp), InCall: true, Title: model.NewSensitive(titleCanary + " " + argvCanary)},
		{AppID: "com.example.player", HasVideo: true, Title: model.NewSensitive(titleCanary)},
	}}
	report := "  ok  config file is valid: " + config.Path(configDir) + "\n" +
		"  *  right now: " + mediaNow(snap, classify.New(cfg.Privacy.ExcludeApps, nil)) + "\n" +
		"  !  " + privateApp + " seen\n"

	files, err := collectBundle(t.Context(), []byte(report))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tmp, "bundle.zip")
	if err := writeBundle(path, files); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("bundle mode: %v %v", fi.Mode(), err)
		}
	}
	if err := writeBundle(path, files[:1]); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("writeBundle over an existing file: %v", err)
	}

	got := readBundle(t, path)
	for _, name := range []string{"doctor.txt", "config.toml", paths.DaemonLogFile} {
		if _, ok := got[name]; !ok {
			t.Errorf("bundle has no %s", name)
		}
	}
	if n := strings.Count(got[paths.DaemonLogFile], "\n"); n != bundleLogLines {
		t.Errorf("bundle has %d log lines, want %d", n, bundleLogLines)
	}
	if !strings.Contains(got["config.toml"], `exclude_apps = ["[excluded app]"]`) {
		t.Errorf("config doesn't show one excluded app:\n%s", got["config.toml"])
	}
	if !strings.Contains(got["doctor.txt"], "a private app") || !strings.Contains(got["doctor.txt"], "~") {
		t.Errorf("doctor report:\n%s", got["doctor.txt"])
	}
	raw, err := os.ReadFile(path) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	got["(raw zip)"] = string(raw)
	for name, data := range got {
		for _, c := range []string{titleCanary, argvCanary, "CANARYhome31", "canary.private31"} {
			if strings.Contains(strings.ToLower(data), strings.ToLower(c)) {
				t.Errorf("%s in %s:\n%s", c, name, data)
			}
		}
	}
}

// TestBundleInvalidConfig: a config file that doesn't parse may be quoted
// in its error, and its excluded apps aren't known, so neither the file
// nor the error goes in the bundle.
func TestBundleInvalidConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv(paths.EnvDataDir, filepath.Join(tmp, "data"))
	t.Setenv(paths.EnvConfigDir, tmp)
	t.Setenv(envKeystore, "file")
	bad := "[privacy]\nexclude_apps = [\"CANARYbad\\u0001app\"]\n"
	if err := os.WriteFile(config.Path(tmp), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(config.Path(tmp)); err == nil || !strings.Contains(err.Error(), "CANARYbad") {
		t.Fatalf("the test needs an error that quotes the file: %v", err)
	}
	var report bytes.Buffer
	if err := doctor(t.Context(), &report, true); err != nil {
		t.Fatal(err)
	}
	files, err := collectBundle(t.Context(), report.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report.String(), "burnout config validate") {
		t.Errorf("doctor doesn't point at config validate:\n%s", report.String())
	}
	for _, f := range files {
		if strings.Contains(string(f.Data), "CANARYbad") {
			t.Errorf("%s quotes the invalid config:\n%s", f.Name, f.Data)
		}
	}
}

func TestTailLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		n    int
		max  int64
		want string
	}{
		{2, 1 << 10, "three\nfour"},
		{10, 1 << 10, "one\ntwo\nthree\nfour"},
		{10, 12, "three\nfour"}, // "o\nthree\nfour": the cut-off line is dropped
	} {
		if got, err := tailLines(path, tc.n, tc.max); err != nil || string(got) != tc.want {
			t.Errorf("tailLines(%d, %d) = %q, %v; want %q", tc.n, tc.max, got, err, tc.want)
		}
	}
}

func TestConfirmed(t *testing.T) {
	for in, want := range map[string]bool{"yes\n": true, " yes \r\n": true, "y\n": false, "yes": false, "": false} {
		if got := confirmed(strings.NewReader(in)); got != want {
			t.Errorf("confirmed(%q) = %v", in, got)
		}
	}
}

// readBundle returns each file in the zip at path.
func readBundle(t *testing.T, path string) map[string]string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = zr.Close() }()
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		got[f.Name] = string(b)
	}
	return got
}
