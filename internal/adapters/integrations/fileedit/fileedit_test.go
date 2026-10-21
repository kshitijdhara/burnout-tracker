package fileedit

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWriteAtomicAndBackup(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rc")
	if err := WriteAtomic(p, []byte("a\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	b, mode, existed, err := ReadExisting(p, 0o600)
	if err != nil || !existed || string(b) != "a\n" {
		t.Fatalf("ReadExisting = %q %v %v", b, existed, err)
	}
	if runtime.GOOS != "windows" && mode != 0o640 {
		t.Fatalf("mode = %v", mode)
	}
	if err := Backup(p, b, time.Date(2026, 10, 21, 1, 2, 3, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".burnout-backup-20261021T010203Z"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == "" && e.Name() != "rc" && e.Name()[0] == '.' {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestReadExistingMissing(t *testing.T) {
	b, mode, existed, err := ReadExisting(filepath.Join(t.TempDir(), "nope"), 0o644)
	if err != nil || existed || b != nil || mode != 0o644 {
		t.Fatalf("got %q %v %v %v", b, mode, existed, err)
	}
}

func TestResolveTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "zshrc")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".zshrc")
	if err := os.Symlink("dotfiles/zshrc", link); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveTarget(link)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(target); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	dangling := filepath.Join(dir, ".bashrc")
	if err := os.Symlink("missing/bashrc", dangling); err != nil {
		t.Fatal(err)
	}
	if got, _ := ResolveTarget(dangling); got != filepath.Join(dir, "missing/bashrc") {
		t.Fatalf("dangling resolved to %s", got)
	}
}

func TestShellQuote(t *testing.T) {
	if got := ShellQuote("/Applications/It's here/burnout"); got != `'/Applications/It'\''s here/burnout'` {
		t.Fatalf("got %s", got)
	}
}
