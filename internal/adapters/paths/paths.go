// Package paths resolves the per-OS locations for data, config and the
// runtime socket, and creates them with owner-only permissions.
// See docs/privacy-and-security.md §7.3.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// AppName is the directory name used under each base location.
const AppName = "burnout"

// Env overrides, used by tests and by users with non-standard layouts.
const (
	EnvDataDir    = "BURNOUT_DATA_DIR"
	EnvConfigDir  = "BURNOUT_CONFIG_DIR"
	EnvRuntimeDir = "BURNOUT_RUNTIME_DIR"
)

// Log file names, inside LogDir.
const (
	DaemonLogFile = "daemon.log" // the daemon's own rotating log
	CrashLogFile  = "stderr.log" // what launchd captures: panics, failed starts
)

// LogDir returns where the background service keeps its logs.
func LogDir(dataDir string) string { return filepath.Join(dataDir, "logs") }

// DataDir returns the directory for the database, recovery copies and logs.
//
//	macOS:   ~/Library/Application Support/burnout
//	Windows: %LOCALAPPDATA%\burnout (never Documents, which OneDrive may sync)
//	Linux:   $XDG_DATA_HOME/burnout (default ~/.local/share/burnout)
func DataDir() (string, error) {
	if d := os.Getenv(EnvDataDir); d != "" {
		return d, nil
	}
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", AppName), nil
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			return "", errors.New("paths: LOCALAPPDATA is not set")
		}
		return filepath.Join(base, AppName), nil
	default:
		if x := os.Getenv("XDG_DATA_HOME"); x != "" {
			return filepath.Join(x, AppName), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share", AppName), nil
	}
}

// ConfigDir returns the directory for config.toml and installed hook scripts.
func ConfigDir() (string, error) {
	if d := os.Getenv(EnvConfigDir); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, AppName), nil
}

// RuntimeDir returns the directory holding the daemon's socket.
//
//	Linux:  $XDG_RUNTIME_DIR/burnout, falling back to <data dir>/run
//	macOS:  <data dir>/run
//	Windows: unused (named pipe)
func RuntimeDir() (string, error) {
	if d := os.Getenv(EnvRuntimeDir); d != "" {
		return d, nil
	}
	if runtime.GOOS == "linux" {
		if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
			return filepath.Join(x, AppName), nil
		}
	}
	data, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "run"), nil
}

// ErrUnverified means the platform's permission model (Windows ACLs) wasn't
// checked. Callers report it as "unknown", never as "ok".
var ErrUnverified = errors.New("paths: permissions not verified on this platform")

// EnsurePrivateDir creates dir (and parents), forces mode 0700 on dir itself,
// then checks it with CheckOwnedPrivateDir. On Windows the mode bits are
// advisory; the directory inherits the owner-only ACL of %LOCALAPPDATA%.
func EnsurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("paths: create %s: %w", dir, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // directories need the search bit; 0700 is owner-only
			return fmt.Errorf("paths: chmod %s: %w", dir, err)
		}
	}
	return CheckOwnedPrivateDir(dir)
}

// CheckOwnedPrivateDir verifies dir is a real directory (not a symlink) and,
// on unix, owned by the current user with mode 0700. This stops a shared or
// pre-created location (say $XDG_RUNTIME_DIR=/tmp) from being used to
// intercept the daemon's socket or keys.
func CheckOwnedPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("paths: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("paths: %s is a symlink; expected a real directory", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("paths: %s is not a directory", dir)
	}
	return checkOwner(dir, fi)
}

// CheckPrivate reports whether path has no group or other permission bits.
// On Windows it returns ErrUnverified, because ACLs apply instead.
func CheckPrivate(path string) error {
	if runtime.GOOS == "windows" {
		return ErrUnverified
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("paths: %s has mode %v; expected owner-only", path, fi.Mode().Perm())
	}
	return nil
}
