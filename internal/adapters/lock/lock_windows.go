//go:build windows

package lock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockAll covers the whole file (any non-empty range works; this is the
// conventional "entire file" range).
const lockAll = ^uint32(0)

func lockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, lockAll, lockAll, ol)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLocked
	}
	if err != nil {
		return fmt.Errorf("lock: LockFileEx: %w", err)
	}
	return nil
}

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockAll, lockAll, new(windows.Overlapped))
}
