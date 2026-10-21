// Package fileedit holds the safe-editing primitives every installer uses
// when it touches a user's own files (shell rc files, Claude Code settings):
// follow symlinks, back up first, write atomically (privacy & security §8.5).
package fileedit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ResolveTarget follows a symlinked file to the file it points at, so
// dotfile managers keep their link and we edit the real file. A dangling
// link resolves to the path it names, so the file gets created there.
func ResolveTarget(path string) (string, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		return path, nil
	}
	if target, err := filepath.EvalSymlinks(path); err == nil {
		return target, nil
	}
	link, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(link) {
		link = filepath.Join(filepath.Dir(path), link)
	}
	return link, nil
}

// ReadExisting returns a file's content and mode, and whether it existed.
// A missing file yields empty content and defaultMode.
func ReadExisting(path string, defaultMode fs.FileMode) ([]byte, fs.FileMode, bool, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the user's own file, chosen by the installer
	if errors.Is(err, fs.ErrNotExist) {
		return nil, defaultMode, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, 0, false, err
	}
	return b, fi.Mode().Perm(), true, nil
}

// Backup copies content next to path before it is changed. Backups are
// owner-only because rc files and settings can hold secrets.
func Backup(path string, content []byte, now time.Time) error {
	name := fmt.Sprintf("%s.burnout-backup-%s", path, now.UTC().Format("20060102T150405Z"))
	if err := os.WriteFile(name, content, 0o600); err != nil { //nolint:gosec // path derives from the user's own file
		return fmt.Errorf("fileedit: backup %s: %w", path, err)
	}
	return nil
}

// WriteAtomic replaces path with content via a temp file in the same
// directory and a rename, so a crash never leaves a half-written file.
func WriteAtomic(path string, content []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".burnout-tmp-*")
	if err != nil {
		return err
	}
	clean := func() { _ = os.Remove(tmp.Name()) }
	for _, step := range []func() error{
		func() error { _, err := tmp.Write(content); return err },
		func() error { return tmp.Chmod(mode) },
		tmp.Sync,
	} {
		if err := step(); err != nil {
			_ = tmp.Close()
			clean()
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		clean()
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		clean()
		return err
	}
	return nil
}

// ShellQuote single-quotes s for a POSIX shell.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
