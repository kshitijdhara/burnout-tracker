//go:build !windows

package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// CheckTrustedExecutable refuses a binary that another user could replace:
// the background service and the hooks run it with the user's rights at
// every login and command. The file and every folder above it, both as
// given and with symlinks resolved, must be owned by the current user or
// root and not writable by everyone. Group-writable is allowed, as for the
// admin-group folders Homebrew uses on macOS.
func CheckTrustedExecutable(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("paths: %s is not absolute", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("paths: %w", err)
	}
	for _, p := range []string{filepath.Clean(path), resolved} {
		for dir := p; ; dir = filepath.Dir(dir) {
			if err := checkTrusted(dir); err != nil {
				return err
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	return nil
}

func checkTrusted(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return fmt.Errorf("paths: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil // a link's own bits mean nothing; its folder is checked
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("paths: can't read the owner of " + p)
	}
	if uid := int(st.Uid); uid != os.Getuid() && uid != 0 {
		return fmt.Errorf("paths: %s is owned by uid %d, so another user could replace the binary; install it in a folder you own (for example ~/.local/bin)", p, uid)
	}
	// The sticky bit doesn't help here: everyone can still add files.
	if fi.Mode().Perm()&0o002 != 0 {
		return fmt.Errorf("paths: %s is writable by everyone, so another user could replace the binary; install it in a folder you own (for example ~/.local/bin)", p)
	}
	return nil
}
