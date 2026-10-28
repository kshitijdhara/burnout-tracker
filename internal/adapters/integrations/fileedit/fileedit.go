// Package fileedit holds the safe-editing primitives every installer uses
// when it touches a user's own files (shell rc files, Claude Code settings):
// follow symlinks, back up first, write atomically (privacy & security §8.5).
package fileedit

import (
	"crypto/rand"
	"encoding/hex"
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

// Backup copies content into backupDir before path is changed and returns
// the backup's path. Backups go to the app's private directory, never next
// to the file: rc files and settings can hold secrets, and a file next to a
// symlinked rc often lives in a dotfiles repository that gets pushed. Each
// backup gets a unique name and is created exclusively, owner-only.
func Backup(backupDir, path string, content []byte, now time.Time) (string, error) {
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", fmt.Errorf("fileedit: backup dir: %w", err)
	}
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("fileedit: backup name: %w", err)
	}
	name := filepath.Join(backupDir, fmt.Sprintf("%s.%s.%s.bak",
		filepath.Base(path), now.UTC().Format("20060102T150405Z"), hex.EncodeToString(suffix[:])))
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // name is built from our own backup dir
	if err != nil {
		return "", fmt.Errorf("fileedit: backup %s: %w", path, err)
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("fileedit: backup %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("fileedit: backup %s: %w", path, err)
	}
	return name, nil
}

// WriteAtomic replaces path with content via a temp file in the same
// directory and a rename, so a crash never leaves a half-written file.
func WriteAtomic(path string, content []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".burntout-tmp-*")
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
