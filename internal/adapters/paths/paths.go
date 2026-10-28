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
const AppName = "burntout"

// LegacyAppName is the directory name from before the rename to BurntOut.
const LegacyAppName = "burnout"

// Env overrides, used by tests and by users with non-standard layouts.
const (
	EnvDataDir    = "BURNTOUT_DATA_DIR"
	EnvConfigDir  = "BURNTOUT_CONFIG_DIR"
	EnvRuntimeDir = "BURNTOUT_RUNTIME_DIR"
)

// legacyEnv maps each override to its name from before the rename to
// BurntOut, which is still read (see Getenv) so a layout set up then keeps
// working, folders and all.
var legacyEnv = map[string]string{
	EnvDataDir:    "BURNOUT_DATA_DIR",
	EnvConfigDir:  "BURNOUT_CONFIG_DIR",
	EnvRuntimeDir: "BURNOUT_RUNTIME_DIR",
	// The keystore's override lives in cmd, but its old name is here too.
	"BURNTOUT_KEYSTORE": "BURNOUT_KEYSTORE",
}

// Getenv reads an override, falling back to its pre-rename name. Callers
// that can print use DeprecatedEnv to tell the user to rename it.
func Getenv(name string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	if old, ok := legacyEnv[name]; ok {
		return os.Getenv(old)
	}
	return ""
}

// DeprecatedEnv lists the pre-rename overrides in use, each with its new
// name, as "OLD → NEW".
func DeprecatedEnv(names ...string) []string {
	var out []string
	for _, name := range names {
		old, ok := legacyEnv[name]
		if ok && os.Getenv(name) == "" && os.Getenv(old) != "" {
			out = append(out, old+" → "+name)
		}
	}
	return out
}

// Log file names, inside LogDir.
const (
	DaemonLogFile = "daemon.log" // the daemon's own rotating log
	CrashLogFile  = "stderr.log" // what launchd captures: panics, failed starts
)

// LogDir returns where the background service keeps its logs.
func LogDir(dataDir string) string { return filepath.Join(dataDir, "logs") }

// DataDir returns the directory for the database, recovery copies and logs.
//
//	macOS:   ~/Library/Application Support/burntout
//	Windows: %LOCALAPPDATA%\burntout (never Documents, which OneDrive may sync)
//	Linux:   $XDG_DATA_HOME/burntout (default ~/.local/share/burntout)
func DataDir() (string, error) {
	if d := Getenv(EnvDataDir); d != "" {
		return d, nil
	}
	return dataDirNamed(AppName)
}

// LegacyDirs returns the default data and config folders from before the
// rename to BurntOut, or "" for each when this process uses another place
// than the new default: an override keeps its folder, so nothing moves. An
// override that names the new default (the background service pins its
// folders that way) still moves the old default there.
func LegacyDirs() (data, config string, err error) {
	newData, err := dataDirNamed(AppName)
	if err != nil {
		return "", "", err
	}
	if d := Getenv(EnvDataDir); d == "" || samePath(d, newData) {
		if data, err = dataDirNamed(LegacyAppName); err != nil {
			return "", "", err
		}
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", "", err
	}
	if d := Getenv(EnvConfigDir); d == "" || samePath(d, filepath.Join(base, AppName)) {
		config = filepath.Join(base, LegacyAppName)
	}
	return data, config, nil
}

func samePath(a, b string) bool {
	a, errA := filepath.Abs(a)
	b, errB := filepath.Abs(b)
	return errA == nil && errB == nil && filepath.Clean(a) == filepath.Clean(b)
}

func dataDirNamed(name string) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", name), nil
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			return "", errors.New("paths: LOCALAPPDATA is not set")
		}
		return filepath.Join(base, name), nil
	default:
		if x := os.Getenv("XDG_DATA_HOME"); x != "" {
			return filepath.Join(x, name), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share", name), nil
	}
}

// ConfigDir returns the directory for config.toml and installed hook scripts.
func ConfigDir() (string, error) {
	if d := Getenv(EnvConfigDir); d != "" {
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
//	Linux:  $XDG_RUNTIME_DIR/burntout, falling back to <data dir>/run
//	macOS:  <data dir>/run
//	Windows: unused (named pipe)
func RuntimeDir() (string, error) {
	if d := Getenv(EnvRuntimeDir); d != "" {
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
