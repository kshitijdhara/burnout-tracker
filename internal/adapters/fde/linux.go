//go:build linux

package fde

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Linux detection walks sysfs from the block device holding the data
// directory down through its device-mapper slaves, looking for a dm-crypt
// layer (LUKS, plain dm-crypt, LVM on LUKS). The parsing is kept separate
// from fde_linux.go, which supplies the device, so it can be tested against
// fixture trees.

const maxSlaveDepth = 8

// sysfs reads block-device information under root (normally /sys).
type sysfs struct{ root string }

// devDir returns the sysfs directory for a block device number, if any.
// Anonymous devices (btrfs subvolumes, overlayfs, tmpfs) have none.
func (s sysfs) devDir(major, minor uint32) (string, bool) {
	dir := filepath.Join(s.root, "dev", "block", fmt.Sprintf("%d:%d", major, minor))
	_, err := os.Stat(dir)
	return dir, err == nil
}

// blockDir returns the sysfs directory for a block device name such as "dm-0".
func (s sysfs) blockDir(name string) (string, bool) {
	dir := filepath.Join(s.root, "class", "block", name)
	_, err := os.Stat(dir)
	return dir, err == nil
}

// classify reports whether the block device at dir is backed by dm-crypt.
func (s sysfs) classify(dir string) (State, string) {
	if s.hasCrypt(dir, 0) {
		return Enabled, "dm-crypt (LUKS) volume"
	}
	if _, err := os.Stat(filepath.Join(dir, "dm")); err == nil {
		return Disabled, "device-mapper volume without dm-crypt"
	}
	return Disabled, "unencrypted block device"
}

func (s sysfs) hasCrypt(dir string, depth int) bool {
	if depth > maxSlaveDepth {
		return false
	}
	//nolint:gosec // reads sysfs attributes under a fixed root
	if uuid, err := os.ReadFile(filepath.Join(dir, "dm", "uuid")); err == nil &&
		strings.HasPrefix(strings.TrimSpace(string(uuid)), "CRYPT-") {
		return true
	}
	slaves, err := os.ReadDir(filepath.Join(dir, "slaves"))
	if err != nil {
		return false
	}
	for _, sl := range slaves {
		if sub, ok := s.blockDir(sl.Name()); ok && s.hasCrypt(sub, depth+1) {
			return true
		}
	}
	return false
}

// mountSource finds the mount containing path in /proc/self/mountinfo
// content and returns its source and filesystem type.
func mountSource(mountinfo, path string) (source, fstype string, ok bool) {
	best := -1
	sc := bufio.NewScanner(strings.NewReader(mountinfo))
	for sc.Scan() {
		pre, post, found := strings.Cut(sc.Text(), " - ")
		fields, tail := strings.Fields(pre), strings.Fields(post)
		if !found || len(fields) < 5 || len(tail) < 2 {
			continue
		}
		mp := unescapeMount(fields[4])
		if !within(path, mp) || len(mp) <= best {
			continue
		}
		best, fstype, source, ok = len(mp), tail[0], unescapeMount(tail[1]), true
	}
	return source, fstype, ok
}

func within(path, mountpoint string) bool {
	return mountpoint == "/" || path == mountpoint || strings.HasPrefix(path, mountpoint+"/")
}

// unescapeMount decodes the octal escapes (\040 for space, \011 tab, \012
// newline, \134 backslash) the kernel uses in mountinfo.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// existingAncestor returns path or its nearest existing ancestor, because the
// data directory may not exist yet on first run.
func existingAncestor(path string) string {
	for p := filepath.Clean(path); ; {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}
