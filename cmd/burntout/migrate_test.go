package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kshitijdhara/burntout/internal/adapters/lock"
)

func TestMoveLegacy(t *testing.T) {
	base := t.TempDir()
	dir := func(name string) string { return filepath.Join(base, name) }
	mk := func(path string) {
		t.Helper()
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "marker"), []byte(path), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Nothing to move.
	if moved, err := moveLegacy(dir("old-data"), dir("old-config"), dir("data"), dir("config")); moved || err != nil {
		t.Fatalf("empty: %v %v", moved, err)
	}

	// A daemon of the old app holds its lock: nothing moves.
	mk(dir("old-data"))
	mk(dir("old-config"))
	release, err := lock.Acquire(dir("old-data"))
	if err != nil {
		t.Fatal(err)
	}
	if moved, err := moveLegacy(dir("old-data"), dir("old-config"), dir("data"), dir("config")); moved || !errors.Is(err, lock.ErrLocked) {
		t.Fatalf("locked: %v %v", moved, err)
	}
	if pathExists(dir("data")) || pathExists(dir("config")) {
		t.Fatal("moved while the old daemon held the lock")
	}
	_ = release()

	// Both folders move, once.
	if moved, err := moveLegacy(dir("old-data"), dir("old-config"), dir("data"), dir("config")); !moved || err != nil {
		t.Fatalf("move: %v %v", moved, err)
	}
	for _, p := range []string{"data/marker", "config/marker"} {
		if !pathExists(dir(p)) {
			t.Fatalf("%s missing after the move", p)
		}
	}
	if pathExists(dir("old-data")) || pathExists(dir("old-config")) {
		t.Fatal("old folders still there")
	}
	if moved, err := moveLegacy(dir("old-data"), dir("old-config"), dir("data"), dir("config")); moved || err != nil {
		t.Fatalf("second run: %v %v", moved, err)
	}

	// A new folder that already exists is never overwritten.
	mk(dir("old-data"))
	if moved, err := moveLegacy(dir("old-data"), "", dir("data"), ""); moved || err != nil {
		t.Fatalf("existing new folder: %v %v", moved, err)
	}
	if !pathExists(dir("old-data/marker")) {
		t.Fatal("the old folder was touched although the new one exists")
	}

	// macOS: config is the data folder, moved with it.
	if moved, err := moveLegacy(dir("mac-old"), dir("mac-old"), dir("mac-new"), dir("mac-new")); moved || err != nil {
		t.Fatalf("same folder, nothing there: %v %v", moved, err)
	}
	mk(dir("mac-old"))
	if moved, err := moveLegacy(dir("mac-old"), dir("mac-old"), dir("mac-new"), dir("mac-new")); !moved || err != nil {
		t.Fatalf("same folder: %v %v", moved, err)
	}
}
