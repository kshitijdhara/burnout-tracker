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
