//go:build linux

package fde

import (
	"context"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func status(_ context.Context, path string) (State, string, error) {
	return linuxStatus(sysfs{root: "/sys"}, "/proc/self/mountinfo", existingAncestor(path))
}

func linuxStatus(sys sysfs, mountinfoPath, path string) (State, string, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return Unknown, "could not stat data directory", err
	}
	dev := uint64(st.Dev) //nolint:unconvert // Dev is uint32 on some architectures
	if dir, ok := sys.devDir(unix.Major(dev), unix.Minor(dev)); ok {
		s, detail := sys.classify(dir)
		return s, detail, nil
	}

	// Anonymous device number (btrfs subvolume, overlayfs, tmpfs): find the
	// mount's source device instead.
	mi, err := os.ReadFile(mountinfoPath) //nolint:gosec // fixed path under /proc
	if err != nil {
		return Unknown, "could not read mount table", err
	}
	source, fstype, ok := mountSource(string(mi), path)
	if !ok {
		return Unknown, "could not find the data directory's mount", nil
	}
	if resolved, err := filepath.EvalSymlinks(source); err == nil {
		source = resolved
	}
	if dir, ok := sys.blockDir(filepath.Base(source)); ok && filepath.Dir(source) == "/dev" {
		s, detail := sys.classify(dir)
		return s, detail, nil
	}
	return Unknown, "filesystem " + fstype + " is not on a local block device", nil
}
