//go:build !windows

package paths

import (
	"fmt"
	"os"
	"syscall"
)

func checkOwner(dir string, fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("paths: can't read the owner of %s", dir)
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("paths: %s is owned by uid %d, not the current user", dir, st.Uid)
	}
	if fi.Mode().Perm() != 0o700 {
		return fmt.Errorf("paths: %s has mode %v; expected 0700", dir, fi.Mode().Perm())
	}
	return nil
}
