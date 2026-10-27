package fileedit

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	backupDir := filepath.Join(t.TempDir(), "backups")
	at := time.Date(2026, 10, 21, 1, 2, 3, 0, time.UTC)
	first, err := Backup(backupDir, p, b, at)
	if err != nil {
		t.Fatal(err)
	}
	// Two backups in the same second must not overwrite each other.
	second, err := Backup(backupDir, p, []byte("b\n"), at)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || filepath.Dir(first) != backupDir {
		t.Fatalf("backups %s and %s", first, second)
	}
	if !strings.HasPrefix(filepath.Base(first), "rc.20261021T010203Z.") || filepath.Ext(first) != ".bak" {
		t.Fatalf("backup name %s", first)
	}
	if got, _ := os.ReadFile(first); string(got) != "a\n" {
		t.Fatalf("first backup holds %q", got)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(first); fi.Mode().Perm() != 0o600 {
			t.Fatalf("backup mode %v", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(backupDir); fi.Mode().Perm() != 0o700 {
			t.Fatalf("backup dir mode %v", fi.Mode().Perm())
		}
	}
	// Nothing is written next to the original file.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("files next to the original: %v", entries)
	}
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
	if got := ShellQuote("/Applications/It's here/burntout"); got != `'/Applications/It'\''s here/burntout'` {
		t.Fatalf("got %s", got)
	}
}
