package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/lock"
	"github.com/kshitijdhara/burntout/internal/adapters/paths"
	"github.com/kshitijdhara/burntout/internal/adapters/store/sqlite"
)

// Moving what the app kept under its old name, burnout, to BurntOut's
// places (privacy & security §7.3). The old folders are the record of what
// hasn't moved: while the old data folder exists, its data hasn't. Keyring
// secrets move themselves on first use (keystore.adoptLegacy), so they
// need no step here.

var (
	// errLegacyBlocked means the old data couldn't move yet, usually
	// because an old burnout daemon still has it open. Commands that would
	// create the new folders don't run then: they would strand the old data.
	errLegacyBlocked = errors.New("your data is still in the old burnout folder")
	// errLegacyConflict means both the old and the new data folder hold a
	// database, so neither is moved; the user picks.
	errLegacyConflict = errors.New("both the old burnout folder and the BurntOut folder hold data")
)

// legacyLockWait is how long a migration waits for the old folder's lock,
// so another BurntOut process migrating at the same moment (at login the
// tray, the daemon and a shell often start together) isn't mistaken for
// an old daemon.
const legacyLockWait = 3 * time.Second

// readOnlyCommands look but don't change anything, so they never move
// folders (system design §6: read-only views); they only warn while old
// data waits. service and uninstall remove the old background service
// first, then move the data themselves.
var readOnlyCommands = map[string]bool{
	"status": true, "timeline": true, "doctor": true, "privacy": true, "export": true, "version": true,
	"service": true, "uninstall": true,
}

// prepareLegacy runs before the command name, once it is known: it warns
// about renamed overrides, and moves old data for commands that change
// state. It returns an error when the command must not run yet.
func prepareLegacy(w io.Writer, name string) error {
	for _, d := range paths.DeprecatedEnv(paths.EnvDataDir, paths.EnvConfigDir, paths.EnvRuntimeDir, envKeystore) {
		_, _ = fmt.Fprintf(w, "BurntOut: %s is the old name of this setting; it still works for now, but please rename it.\n", d)
	}
	if readOnlyCommands[name] {
		warnLegacyPending(w)
		return nil
	}
	if err := migrateLegacy(w); errors.Is(err, errLegacyBlocked) {
		return fmt.Errorf("`burntout %s` waits until your data has moved: see above", name)
	}
	return nil
}

// migrateLegacy moves the old folders if they are still there, and says
// what happened. It returns errLegacyBlocked if they couldn't move.
func migrateLegacy(w io.Writer) error {
	l, ok := legacyLocations()
	if !ok {
		return nil
	}
	aside, err := moveLegacy(l, legacyLockWait)
	switch {
	case errors.Is(err, errLegacyConflict):
		conflictNote(w, l)
		return nil
	case errors.Is(err, lock.ErrLocked):
		_, _ = fmt.Fprintf(w, "BurntOut: your data is still in %s, in use by an older burnout daemon.\n"+
			"Run `burntout service install` to replace the old background service and move your data,\n"+
			"or stop an old daemon running in a terminal (Ctrl-C) and run the command again.\n", l.oldData)
		return errLegacyBlocked
	case err != nil:
		_, _ = fmt.Fprintf(w, "BurntOut: couldn't move your data from %s: %v\n", l.oldData, err)
		return errLegacyBlocked
	case aside == movedNothing:
		return nil
	}
	_, _ = fmt.Fprintf(w, "BurntOut (formerly burnout) moved your data and settings to %s.\n"+
		"Run `burntout install-hooks` and `burntout service install` to replace the old hooks and background service.\n", l.newData)
	if aside != "" {
		_, _ = fmt.Fprintf(w, "Files BurntOut had made before the move that the old folder also had are kept in %s.\n", aside)
	}
	return nil
}

// warnLegacyPending tells read-only commands that the old data hasn't
// moved, without moving it.
func warnLegacyPending(w io.Writer) {
	l, ok := legacyLocations()
	if !ok || !pathExists(l.oldData) {
		return
	}
	if hasDatabase(l.oldData) && hasDatabase(l.newData) {
		conflictNote(w, l)
		return
	}
	_, _ = fmt.Fprintf(w, "BurntOut: your data is still in the old burnout folder, %s.\n"+
		"Run `burntout service install` to move it (and replace the old background service).\n", l.oldData)
}

func conflictNote(w io.Writer, l legacyDirs) {
	_, _ = fmt.Fprintf(w, "BurntOut: both %s (from burnout) and %s hold recorded data, so neither was moved.\n"+
		"Keep the one you want: delete the other folder, or move the old one in place of the new one while no daemon runs.\n", l.oldData, l.newData)
}

// legacyDirs are the old and new folders.
type legacyDirs struct {
	oldData, oldConfig, newData, newConfig string
}

func legacyLocations() (legacyDirs, bool) {
	oldData, oldConfig, err := paths.LegacyDirs()
	if err != nil || (oldData == "" && oldConfig == "") {
		return legacyDirs{}, false
	}
	newData, err1 := paths.DataDir()
	newConfig, err2 := paths.ConfigDir()
	if err1 != nil || err2 != nil {
		return legacyDirs{}, false
	}
	return legacyDirs{oldData: oldData, oldConfig: oldConfig, newData: newData, newConfig: newConfig}, true
}

// movedNothing is moveLegacy's answer when there was nothing to move.
const movedNothing = "\x00"

// moveLegacy moves the old folders to the new places. It takes the old data
// folder's daemon lock first, so no old daemon can start or keep writing
// while it moves, and two BurntOut processes don't move it at once; on Unix
// the lock is held across the rename (it follows the folder). It returns
// where files BurntOut had already made in the new folder went when the
// old folder also had them ("" if nowhere), movedNothing if there was
// nothing to move, lock.ErrLocked while an old daemon runs and
// errLegacyConflict when both folders hold a database.
func moveLegacy(l legacyDirs, wait time.Duration) (string, error) {
	aside := movedNothing
	if l.oldData != "" && pathExists(l.oldData) {
		if hasDatabase(l.oldData) && hasDatabase(l.newData) {
			return movedNothing, errLegacyConflict
		}
		release, err := acquireWithin(l.oldData, wait)
		if errors.Is(err, os.ErrNotExist) {
			return movedNothing, nil // another BurntOut process moved it meanwhile
		}
		if err != nil {
			return movedNothing, err
		}
		if runtime.GOOS == "windows" {
			_ = release() // Windows can't rename a folder with a file open in it
			release = func() error { return nil }
		}
		aside, err = moveInto(l.oldData, l.newData)
		_ = release()
		if err != nil {
			return movedNothing, err
		}
	}
	// On macOS the config folder is the data folder, moved already.
	if l.oldConfig != "" && l.oldConfig != l.oldData && pathExists(l.oldConfig) {
		configAside, err := moveInto(l.oldConfig, l.newConfig)
		if err != nil {
			return movedNothing, err
		}
		if aside == movedNothing {
			aside = configAside
		}
	}
	return aside, nil
}

// acquireWithin takes dir's daemon lock, retrying for up to wait while
// someone else holds it. It returns an error wrapping os.ErrNotExist if
// dir disappears meanwhile.
func acquireWithin(dir string, wait time.Duration) (func() error, error) {
	deadline := time.Now().Add(wait)
	for {
		release, err := lock.Acquire(dir)
		if err == nil {
			if !pathExists(dir) { // moved while we waited for it
				_ = release()
				return nil, os.ErrNotExist
			}
			return release, nil
		}
		if !pathExists(dir) {
			return nil, os.ErrNotExist
		}
		if !errors.Is(err, lock.ErrLocked) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// moveInto renames old to dst. If dst exists already (BurntOut made it
// before the old data could move), the old folder still wins: dst is set
// aside, old takes its place, and what only the set-aside folder had comes
// back. It returns where the rest of the set-aside folder stayed, or "".
func moveInto(old, dst string) (string, error) {
	if !pathExists(dst) {
		return "", os.Rename(old, dst)
	}
	aside := dst + ".before-migration"
	if pathExists(aside) {
		aside += "-" + strconv.FormatInt(time.Now().Unix(), 10)
	}
	if err := os.Rename(dst, aside); err != nil {
		return "", err
	}
	if err := os.Rename(old, dst); err != nil {
		_ = os.Rename(aside, dst)
		return "", err
	}
	entries, err := os.ReadDir(aside)
	if err != nil {
		return aside, nil
	}
	for _, e := range entries {
		target := filepath.Join(dst, e.Name())
		if e.Name() == lock.FileName || pathExists(target) {
			continue
		}
		_ = os.Rename(filepath.Join(aside, e.Name()), target)
	}
	_ = os.Remove(filepath.Join(aside, lock.FileName))
	if os.Remove(aside) == nil {
		return "", nil
	}
	return aside, nil
}

func hasDatabase(dir string) bool { return pathExists(filepath.Join(dir, sqlite.FileName)) }

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
