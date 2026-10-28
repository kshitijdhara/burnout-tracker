package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsurePrivateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := CheckOwnedPrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestCheckOwnedPrivateDirRejects(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks and modes need unix")
	}
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	loose := filepath.Join(base, "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o755); err != nil { //nolint:gosec // the test needs a loose mode
		t.Fatal(err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{"symlink": link, "loose mode": loose, "not a dir": file, "missing": filepath.Join(base, "nope")} {
		if err := CheckOwnedPrivateDir(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// EnsurePrivateDir refuses to adopt a symlinked location.
	if err := EnsurePrivateDir(link); err == nil {
		t.Error("EnsurePrivateDir accepted a symlink")
	}
}

func TestCheckPrivate(t *testing.T) {
	f := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := CheckPrivate(f)
	if runtime.GOOS == "windows" {
		if !errors.Is(err, ErrUnverified) {
			t.Fatalf("windows: got %v, want ErrUnverified", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f, 0o644); err != nil { //nolint:gosec // the test needs a readable mode
		t.Fatal(err)
	}
	if CheckPrivate(f) == nil {
		t.Fatal("world-readable file accepted")
	}
}

func TestExcludeFromBackupsNoopOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("runs tmutil on macOS")
	}
	if err := ExcludeFromBackups(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

// Overrides under the pre-rename names still apply, and are reported.
func TestLegacyEnvFallback(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	t.Setenv("BURNOUT_DATA_DIR", "/custom/burnout")
	if d, _ := DataDir(); d != "/custom/burnout" {
		t.Fatalf("DataDir = %q, want the old override", d)
	}
	if got := DeprecatedEnv(EnvDataDir, EnvConfigDir); len(got) != 1 || got[0] != "BURNOUT_DATA_DIR → BURNTOUT_DATA_DIR" {
		t.Fatalf("DeprecatedEnv = %q", got)
	}
	t.Setenv(EnvDataDir, "/new")
	if d, _ := DataDir(); d != "/new" || len(DeprecatedEnv(EnvDataDir)) != 0 {
		t.Fatalf("the new name doesn't win: %q", d)
	}
}

// The background service pins its folders to the new defaults; the old
// default must still be found then, or its daemon never migrates. Any
// other override keeps its folder, so nothing moves.
func TestLegacyDirsWhenPinned(t *testing.T) {
	t.Setenv("BURNOUT_DATA_DIR", "")
	t.Setenv("BURNOUT_CONFIG_DIR", "")
	newDefault, err := dataDirNamed(AppName)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvDataDir, newDefault)
	if data, _, _ := LegacyDirs(); data == "" || filepath.Base(data) != LegacyAppName {
		t.Fatalf("pinned to the new default: legacy data %q", data)
	}
	t.Setenv(EnvDataDir, t.TempDir())
	if data, _, _ := LegacyDirs(); data != "" {
		t.Fatalf("a custom override still moves the old default: %q", data)
	}
}
