package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/lock"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/service"
)

func TestUnstableBinary(t *testing.T) {
	sep := string(filepath.Separator)
	for bin, want := range map[string]bool{
		filepath.Join(os.TempDir(), "go-build123", "b001", "exe", "burnout"): true,
		filepath.Join(os.TempDir(), "burnout"):                               true,
		sep + filepath.Join("home", "me", ".local", "bin", "burnout"):        false,
	} {
		if got := unstableBinary(bin); got != want {
			t.Errorf("unstableBinary(%q) = %v, want %v", bin, got, want)
		}
	}
}

func TestDescribeService(t *testing.T) {
	for _, tc := range []struct {
		st   service.Status
		want string
	}{
		{service.Status{Installed: true, Running: true, Detail: "pid 812"}, "running (pid 812)"},
		{service.Status{Installed: true}, "installed but not running"},
		{service.Status{}, "not installed"},
	} {
		if got := describeService(tc.st, "journalctl --user -u burnout.service"); !strings.Contains(got, tc.want) {
			t.Errorf("describeService(%+v) = %q, want it to mention %q", tc.st, got, tc.want)
		}
	}
}

func TestServiceEnvPinsTheFolders(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(paths.EnvDataDir, filepath.Join(dir, "data"))
	t.Setenv(paths.EnvConfigDir, filepath.Join(dir, "config"))
	t.Setenv(paths.EnvRuntimeDir, filepath.Join(dir, "run"))
	t.Setenv(envKeystore, "file")
	env, err := serviceEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		paths.EnvDataDir: filepath.Join(dir, "data"), paths.EnvConfigDir: filepath.Join(dir, "config"),
		paths.EnvRuntimeDir: filepath.Join(dir, "run"), envKeystore: "file",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

// The service and the e2e test must start the daemon the same way.
func TestDaemonServiceArgsParse(t *testing.T) {
	if !slices.Equal(daemonServiceArgs, []string{"daemon", "--service"}) {
		t.Fatalf("daemonServiceArgs = %q", daemonServiceArgs)
	}
	if err := runDaemon(t.Context(), []string{"-h"}); err != nil {
		t.Fatalf("daemon -h: %v", err)
	}
}

func TestBinaryPathKeepsTheInvokedLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need unix")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "burnout")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	saved := os.Args[0]
	t.Cleanup(func() { os.Args[0] = saved })
	os.Args[0] = link
	if got, err := binaryPath(); err != nil || got != link {
		t.Fatalf("binaryPath() = %q, %v; want the link %q", got, err, link)
	}
	os.Args[0] = filepath.Join(t.TempDir(), "elsewhere")
	if got, err := binaryPath(); err != nil || got == os.Args[0] {
		t.Fatalf("binaryPath() = %q, %v; want the real binary", got, err)
	}
}

func TestHomeRedactor(t *testing.T) {
	var b bytes.Buffer
	h := homeRedactor{w: &b, home: "/Users/me"}
	line := `level=ERROR msg=x err="open /Users/me/Library/Application Support/burnout/x: denied"` + "\n"
	if n, err := h.Write([]byte(line)); err != nil || n != len(line) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got := b.String(); strings.Contains(got, "/Users/me") || !strings.Contains(got, "~/Library/Application Support") {
		t.Fatalf("redacted = %q", got)
	}
	if err := h.redactErr(errors.New("open /Users/me/x")); err.Error() != "open ~/x" {
		t.Fatalf("redactErr = %v", err)
	}
	if h.redactErr(nil) != nil {
		t.Fatal("redactErr(nil) != nil")
	}
}

func TestCapCrashLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, paths.CrashLogFile)
	if err := os.WriteFile(path, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	capCrashLog(dir)
	if b, _ := os.ReadFile(path); string(b) != "small" { //nolint:gosec // test file
		t.Fatalf("small log changed: %q", b)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), crashLogMax+1), 0o600); err != nil {
		t.Fatal(err)
	}
	capCrashLog(dir)
	if fi, err := os.Stat(path); err != nil || fi.Size() != 0 || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("new crash log: %v %v", fi, err)
	}
	if fi, err := os.Stat(path + ".1"); err != nil || fi.Size() != crashLogMax+1 {
		t.Fatalf("old crash log: %v %v", fi, err)
	}
}

func TestWaitForLock(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.DiscardHandler)
	release, err := waitForLock(t.Context(), log, dir)
	if err != nil || release == nil {
		t.Fatalf("free lock: %v", err)
	}
	// Held by another daemon: wait until stopped.
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if r, err := waitForLock(ctx, log, dir); err != nil || r != nil {
		t.Fatalf("held lock: release=%v err=%v", r != nil, err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	again, err := lock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = again()
}
