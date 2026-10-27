package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/lock"
	"github.com/kshitijdhara/burntout/internal/adapters/store/sqlite"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func dirsIn(base string) legacyDirs {
	return legacyDirs{
		oldData: filepath.Join(base, "old-data"), oldConfig: filepath.Join(base, "old-config"),
		newData: filepath.Join(base, "data"), newConfig: filepath.Join(base, "config"),
	}
}

func TestMoveLegacy(t *testing.T) {
	t.Run("nothing to move", func(t *testing.T) {
		l := dirsIn(t.TempDir())
		if aside, err := moveLegacy(l, 0); aside != movedNothing || err != nil {
			t.Fatalf("%q %v", aside, err)
		}
	})
	t.Run("both folders move, once", func(t *testing.T) {
		l := dirsIn(t.TempDir())
		writeFile(t, filepath.Join(l.oldData, sqlite.FileName), "history")
		writeFile(t, filepath.Join(l.oldConfig, "config.toml"), "settings")
		if aside, err := moveLegacy(l, 0); aside != "" || err != nil {
			t.Fatalf("%q %v", aside, err)
		}
		if readFile(t, filepath.Join(l.newData, sqlite.FileName)) != "history" || readFile(t, filepath.Join(l.newConfig, "config.toml")) != "settings" {
			t.Fatal("not moved")
		}
		if pathExists(l.oldData) || pathExists(l.oldConfig) {
			t.Fatal("old folders left")
		}
		if aside, err := moveLegacy(l, 0); aside != movedNothing || err != nil {
			t.Fatalf("second run: %q %v", aside, err)
		}
	})
	t.Run("an old daemon holds the lock", func(t *testing.T) {
		l := dirsIn(t.TempDir())
		writeFile(t, filepath.Join(l.oldData, sqlite.FileName), "history")
		release, err := lock.Acquire(l.oldData)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = release() }()
		if _, err := moveLegacy(l, 150*time.Millisecond); !errors.Is(err, lock.ErrLocked) {
			t.Fatalf("got %v, want ErrLocked", err)
		}
		if pathExists(l.newData) {
			t.Fatal("moved under an old daemon")
		}
	})
	t.Run("a folder BurntOut made first doesn't stop the move", func(t *testing.T) {
		// Before the old data could move, a command made the new folder
		// (hook backups, consent). The old folder still wins.
		l := dirsIn(t.TempDir())
		writeFile(t, filepath.Join(l.oldData, sqlite.FileName), "history")
		writeFile(t, filepath.Join(l.oldData, "consent"), "old consent")
		writeFile(t, filepath.Join(l.newData, "consent"), "new consent")
		writeFile(t, filepath.Join(l.newData, "hook-backups", "zshrc.bak"), "backup")
		aside, err := moveLegacy(l, 0)
		if err != nil {
			t.Fatal(err)
		}
		if readFile(t, filepath.Join(l.newData, sqlite.FileName)) != "history" || readFile(t, filepath.Join(l.newData, "consent")) != "old consent" {
			t.Fatal("the old folder didn't win")
		}
		if readFile(t, filepath.Join(l.newData, "hook-backups", "zshrc.bak")) != "backup" {
			t.Fatal("what only the new folder had wasn't brought back")
		}
		if aside == "" || readFile(t, filepath.Join(aside, "consent")) != "new consent" {
			t.Fatalf("the clashing new file wasn't kept aside: %q", aside)
		}
	})
	t.Run("both hold a database", func(t *testing.T) {
		l := dirsIn(t.TempDir())
		writeFile(t, filepath.Join(l.oldData, sqlite.FileName), "old")
		writeFile(t, filepath.Join(l.newData, sqlite.FileName), "new")
		if _, err := moveLegacy(l, 0); !errors.Is(err, errLegacyConflict) {
			t.Fatalf("got %v, want a conflict", err)
		}
		if readFile(t, filepath.Join(l.oldData, sqlite.FileName)) != "old" || readFile(t, filepath.Join(l.newData, sqlite.FileName)) != "new" {
			t.Fatal("a conflict touched the data")
		}
	})
	t.Run("macOS: config is the data folder", func(t *testing.T) {
		base := t.TempDir()
		l := legacyDirs{oldData: filepath.Join(base, "old"), oldConfig: filepath.Join(base, "old"), newData: filepath.Join(base, "new"), newConfig: filepath.Join(base, "new")}
		writeFile(t, filepath.Join(l.oldData, "consent"), "c")
		if _, err := moveLegacy(l, 0); err != nil || !pathExists(filepath.Join(l.newData, "consent")) {
			t.Fatalf("not moved: %v", err)
		}
	})
}

// The command gate: a command that changes state doesn't run while an old
// daemon holds the old data (it would make the new folders and strand the
// old data); a read-only one runs, warns and moves nothing.
func TestPrepareLegacyGate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("folders come from LOCALAPPDATA and APPDATA there")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	for _, v := range []string{"BURNTOUT_DATA_DIR", "BURNTOUT_CONFIG_DIR", "BURNOUT_DATA_DIR", "BURNOUT_CONFIG_DIR"} {
		t.Setenv(v, "")
	}
	l, ok := legacyLocations()
	if !ok {
		t.Fatal("no legacy locations")
	}
	writeFile(t, filepath.Join(l.oldData, sqlite.FileName), "history")
	release, err := lock.Acquire(l.oldData)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := prepareLegacy(&out, "status"); err != nil {
		t.Fatalf("read-only command refused: %v", err)
	}
	if !strings.Contains(out.String(), "still in the old burnout folder") || pathExists(l.newData) {
		t.Fatalf("read-only command: %q, new folder made %v", out.String(), pathExists(l.newData))
	}
	out.Reset()
	if err := prepareLegacy(&out, "install-hooks"); err == nil {
		t.Fatal("a state-changing command ran while an old daemon held the data")
	}
	if pathExists(l.newData) {
		t.Fatal("the new folder was made while the old data couldn't move")
	}
	_ = release()
	out.Reset()
	if err := prepareLegacy(&out, "install-hooks"); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(l.newData, sqlite.FileName)) != "history" {
		t.Fatal("the data didn't move once the old daemon stopped")
	}
}
