//go:build !windows

package lock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lockFile(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) //nolint:gosec // fd fits in int
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrLocked
	}
	if err != nil {
		return fmt.Errorf("lock: flock: %w", err)
	}
	return nil
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN) //nolint:gosec // fd fits in int
}
