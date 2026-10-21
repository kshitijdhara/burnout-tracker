// Package lock ensures only one daemon runs per user. Two daemons writing
// the same database would double-count attention through the additive
// rollup upsert, so the daemon takes this lock before opening anything.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FileName is the lock file inside the data directory.
const FileName = "daemon.lock"

// ErrLocked means another process holds the lock.
var ErrLocked = errors.New("lock: another daemon is already running")

// Acquire takes an exclusive, non-blocking lock on <dir>/daemon.lock. The
// lock is released by calling release, or by the OS when the process exits
// (so a crash never leaves a stale lock).
func Acquire(dir string) (release func() error, err error) {
	path := filepath.Join(dir, FileName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // our own private data directory
	if err != nil {
		return nil, fmt.Errorf("lock: open %s: %w", path, err)
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() error {
		uerr := unlockFile(f)
		cerr := f.Close()
		return errors.Join(uerr, cerr)
	}, nil
}
