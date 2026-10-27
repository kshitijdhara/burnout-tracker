package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/kshitijdhara/burntout/internal/adapters/keystore"
	"github.com/kshitijdhara/burntout/internal/adapters/lock"
	"github.com/kshitijdhara/burntout/internal/adapters/paths"
)

// migrateLegacy moves what the app kept under its old name, burnout, to
// BurntOut's places, once: the data and config folders, and the keyring
// secrets. It runs before every command but the silent hook ones, and does
// nothing once the new folders exist or when the folders are overridden.
// The old hooks and background service are replaced by `install-hooks`
// and `service install`, which it says.
func migrateLegacy(w io.Writer) {
	oldData, oldConfig, err := paths.LegacyDirs()
	if err != nil {
		return
	}
	newData, err1 := paths.DataDir()
	newConfig, err2 := paths.ConfigDir()
	if err1 != nil || err2 != nil {
		return
	}
	moved, err := moveLegacy(oldData, oldConfig, newData, newConfig)
	if errors.Is(err, lock.ErrLocked) {
		_, _ = fmt.Fprintln(w, "BurntOut: your data is still in use by an older burnout daemon, so it stays where it is for now.\n"+
			"Run `burntout service install` to replace the old background service (or stop a daemon in a terminal with Ctrl-C);\n"+
			"BurntOut moves your data the next time it starts.")
		return
	}
	if err != nil {
		_, _ = fmt.Fprintf(w, "BurntOut: couldn't move your data from its old place: %v\n", err)
		return
	}
	if !moved {
		return
	}
	if _, err := keystore.MigrateKeyring(hmacKeyName, icsKey); err != nil {
		_, _ = fmt.Fprintf(w, "BurntOut: couldn't move its keys in the OS keystore: %v\n", err)
	}
	_, _ = fmt.Fprintf(w, "BurntOut (formerly burnout) moved your data and settings to %s.\n"+
		"Run `burntout install-hooks` and `burntout service install` to replace the old hooks and background service.\n", newData)
}

// moveLegacy renames the old folders to the new ones, if the old ones
// exist and the new ones don't. It refuses while an old daemon holds the
// old data folder's lock. It reports whether it moved anything.
func moveLegacy(oldData, oldConfig, newData, newConfig string) (bool, error) {
	moved := false
	if oldData != "" && pathExists(oldData) && !pathExists(newData) {
		release, err := lock.Acquire(oldData)
		if err != nil {
			return false, err
		}
		_ = release()
		if err := os.Rename(oldData, newData); err != nil {
			return false, err
		}
		moved = true
	}
	// On macOS the config folder is the data folder, already moved.
	if oldConfig != "" && oldConfig != oldData && pathExists(oldConfig) && !pathExists(newConfig) {
		if err := os.Rename(oldConfig, newConfig); err != nil {
			return moved, err
		}
		moved = true
	}
	return moved, nil
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
