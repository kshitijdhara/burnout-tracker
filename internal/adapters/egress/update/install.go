package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Kind is how this copy of burnout was installed, which decides what an
// update may do to it (ADR-0005, "What update does").
type Kind int

// The install kinds.
const (
	// UserPrefix is a binary in a folder the user can write that no
	// package manager owns (install.sh's ~/.local/bin, install.ps1's
	// %LOCALAPPDATA%\Programs\burnout, an unpacked archive). Update swaps
	// it in place.
	UserPrefix Kind = iota
	// Package is a .deb or .rpm (or anything in a system folder the user
	// can't write). Update downloads and verifies the new package, and the
	// user installs it.
	Package
	// App is the binary inside Burnout.app on macOS. Re-signing needs the
	// user, so update downloads and verifies the .dmg and the user
	// installs it.
	App
	// ReadOnly is an archive install in a folder this user can't write
	// (/usr/local/bin, /opt, …). Update downloads and verifies the archive
	// and the user replaces the binary, with the rights that folder needs.
	ReadOnly
)

func (k Kind) String() string {
	switch k {
	case Package:
		return "system package"
	case App:
		return "macOS app"
	case ReadOnly:
		return "copy in a folder you can't write"
	}
	return "user install"
}

// Install is where the running binary lives and how it got there.
type Install struct {
	Path string // the binary, symlinks resolved
	Kind Kind
	GOOS string
	Arch string
}

// Detect classifies the binary at exe (os.Executable). The rule, in order:
//
//  1. Symlinks are resolved first: a ~/.local/bin/burnout link into
//     Burnout.app is the app, not a user install.
//  2. On macOS, a path inside "*.app/Contents/MacOS/" is App.
//  3. On Linux, a path under /usr (except /usr/local), /bin or /sbin is
//     Package: that is where .deb and .rpm put it (/usr/bin).
//     On Windows, a path under Program Files is Package (an installer).
//  4. Anything else is UserPrefix if its folder is writable by this user,
//     and ReadOnly if it isn't, so update never needs elevation.
func Detect(exe, goos, arch string) (Install, error) {
	path, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return Install{}, err
	}
	in := Install{Path: path, GOOS: goos, Arch: arch}
	in.Kind = managedPath(path, goos)
	if in.Kind == UserPrefix && !writable(filepath.Dir(path)) {
		in.Kind = ReadOnly
	}
	return in, nil
}

// managedPath applies rules 2 and 3 to a resolved path; anything else is
// UserPrefix.
func managedPath(path, goos string) Kind {
	switch goos {
	case "darwin":
		if strings.Contains(path, ".app/Contents/MacOS/") {
			return App
		}
	case "windows":
		lower := strings.ToLower(path)
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
			if dir := os.Getenv(env); dir != "" && strings.HasPrefix(lower, strings.ToLower(dir)+`\`) {
				return Package
			}
		}
	default:
		if strings.HasPrefix(path, "/usr/local/") {
			return UserPrefix
		}
		for _, dir := range []string{"/usr/", "/bin/", "/sbin/"} {
			if strings.HasPrefix(path, dir) {
				return Package
			}
		}
	}
	return UserPrefix
}

// writable reports whether this user can create files in dir.
func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".burnout-write-test-*")
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	return true
}

// Asset is the release file this install needs for version v.
func (in Install) Asset(v Version) (string, error) {
	base := "burnout_" + v.String() + "_" + in.GOOS + "_"
	arch := in.Arch
	if in.GOOS == "darwin" {
		arch = "all" // one universal binary
	}
	switch {
	case in.Kind == App:
		return base + "all.dmg", nil
	case in.Kind == Package && in.GOOS == "linux":
		if _, err := os.Stat("/usr/bin/rpm"); err == nil {
			if _, err := os.Stat("/usr/bin/dpkg"); err != nil {
				return base + arch + ".rpm", nil
			}
		}
		return base + arch + ".deb", nil
	case in.GOOS == "windows":
		return base + arch + ".zip", nil
	}
	return base + arch + ".tar.gz", nil
}

// binaryName is the binary's name inside an archive.
func (in Install) binaryName() string {
	if in.GOOS == "windows" {
		return "burnout.exe"
	}
	return "burnout"
}

// Previous is where Swap keeps the binary it replaced.
func (in Install) Previous() string { return in.Path + ".old" }

// Swap replaces the binary with the one in the verified archive at
// archive, keeping the old binary at Previous(). Only for UserPrefix.
//
// On Unix the new binary is written next to the old one and renamed over
// it, so the path always holds a whole binary; the old one is hard-linked
// (or copied) to Previous() first. On Windows a running .exe can't be
// overwritten but can be renamed, so the old binary is renamed to
// Previous() and the new one moved in, and renamed back if that fails.
func (in Install) Swap(archive string) error {
	if in.Kind != UserPrefix {
		return fmt.Errorf("update: a %s is updated by installing the downloaded file, not in place", in.Kind)
	}
	dir := filepath.Dir(in.Path)
	staged := filepath.Join(dir, "."+in.binaryName()+".new")
	if err := extract(archive, in.binaryName(), staged, in.GOOS == "windows"); err != nil {
		_ = os.Remove(staged)
		return err
	}
	prev := in.Previous()
	if in.GOOS == "windows" {
		if err := os.Remove(prev); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(staged)
			return fmt.Errorf("update: can't remove the older binary %s (is it still running?): %w", prev, err)
		}
		if err := os.Rename(in.Path, prev); err != nil {
			_ = os.Remove(staged)
			return err
		}
		if err := os.Rename(staged, in.Path); err != nil {
			_ = os.Rename(prev, in.Path)
			_ = os.Remove(staged)
			return err
		}
		return nil
	}
	if err := keepCopy(in.Path, prev); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("update: keeping the current binary: %w", err)
	}
	if err := os.Rename(staged, in.Path); err != nil {
		_ = os.Remove(staged)
		return err
	}
	return nil
}

// keepCopy puts a copy of src at dst, replacing it: a hard link when the
// filesystem allows, else a copy.
func keepCopy(src, dst string) error {
	_ = os.Remove(dst)
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src) //nolint:gosec // our own binary
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	return writeFile(dst, in, MaxAssetBytes)
}

// extract writes the archive's top-level binary name to dst. A zip is
// read on Windows, a .tar.gz elsewhere.
func extract(archive, name, dst string, isZip bool) error {
	if isZip {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return errors.New("update: the download isn't a zip archive")
		}
		defer func() { _ = zr.Close() }()
		for _, f := range zr.File {
			if f.Name != name || !f.Mode().IsRegular() {
				continue
			}
			r, err := f.Open()
			if err != nil {
				return err
			}
			defer func() { _ = r.Close() }()
			return writeFile(dst, r, MaxAssetBytes)
		}
		return fmt.Errorf("update: the archive has no %s", name)
	}
	f, err := os.Open(archive) //nolint:gosec // a verified download we just wrote
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return errors.New("update: the download isn't a .tar.gz archive")
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("update: the archive has no %s", name)
		}
		if err != nil {
			return errors.New("update: the archive is damaged")
		}
		if strings.TrimPrefix(h.Name, "./") == name && h.Typeflag == tar.TypeReg {
			return writeFile(dst, tr, MaxAssetBytes)
		}
	}
}

// writeFile writes at most limit bytes of r to a new executable file at
// dst, synced to disk.
func writeFile(dst string, r io.Reader, limit int64) error {
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755) //nolint:gosec // an executable, like the one it replaces
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, limit+1))
	if err == nil && n > limit {
		err = errTooLarge
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
