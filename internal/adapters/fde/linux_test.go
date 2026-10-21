//go:build linux

package fde

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeSysfs builds a sysfs tree: devices maps name -> (dm uuid, slaves).
func fakeSysfs(t *testing.T, devices map[string]struct {
	uuid   string
	slaves []string
}) sysfs {
	t.Helper()
	root := t.TempDir()
	for name, d := range devices {
		dir := filepath.Join(root, "class", "block", name)
		mkdir(t, filepath.Join(dir, "slaves"))
		if d.uuid != "" {
			mkdir(t, filepath.Join(dir, "dm"))
			if err := os.WriteFile(filepath.Join(dir, "dm", "uuid"), []byte(d.uuid+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, s := range d.slaves {
			mkdir(t, filepath.Join(dir, "slaves", s))
		}
	}
	return sysfs{root: root}
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestSysfsClassify(t *testing.T) {
	type dev = struct {
		uuid   string
		slaves []string
	}
	sys := fakeSysfs(t, map[string]dev{
		"sda1": {},
		"sda2": {},
		"dm-0": {uuid: "CRYPT-LUKS2-abc-luks", slaves: []string{"sda2"}},
		"dm-1": {uuid: "LVM-xyz", slaves: []string{"dm-0"}}, // LVM on LUKS
		"dm-2": {uuid: "LVM-plain", slaves: []string{"sda1"}},
	})
	tests := map[string]State{"sda1": Disabled, "dm-0": Enabled, "dm-1": Enabled, "dm-2": Disabled}
	for name, want := range tests {
		dir, ok := sys.blockDir(name)
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if got, _ := sys.classify(dir); got != want {
			t.Errorf("classify(%s) = %v, want %v", name, got, want)
		}
	}
}

func TestMountSource(t *testing.T) {
	const mi = `22 1 0:21 / / rw,relatime - btrfs /dev/mapper/luks-123 rw,ssd
23 22 0:22 /home /home rw,relatime - btrfs /dev/mapper/luks-123 rw,ssd
24 22 0:5 / /dev rw - devtmpfs devtmpfs rw
25 22 0:30 / /mnt/my\040disk rw - ext4 /dev/sdb1 rw
26 22 0:31 / /run/user/1000 rw - tmpfs tmpfs rw
`
	tests := []struct {
		path, source, fstype string
	}{
		{"/home/alice/.local/share/burnout", "/dev/mapper/luks-123", "btrfs"},
		{"/var/lib/x", "/dev/mapper/luks-123", "btrfs"},
		{"/mnt/my disk/data", "/dev/sdb1", "ext4"},
		{"/run/user/1000/burnout", "tmpfs", "tmpfs"},
		{"/homeless", "/dev/mapper/luks-123", "btrfs"}, // not under /home
	}
	for _, tc := range tests {
		src, fs, ok := mountSource(mi, tc.path)
		if !ok || src != tc.source || fs != tc.fstype {
			t.Errorf("mountSource(%s) = %s %s %v, want %s %s", tc.path, src, fs, ok, tc.source, tc.fstype)
		}
	}
	if _, _, ok := mountSource("garbage\n", "/x"); ok {
		t.Error("garbage mountinfo matched")
	}
}

func TestUnescapeMount(t *testing.T) {
	tests := map[string]string{`a\040b`: "a b", `plain`: "plain", `x\134y`: `x\y`, `trail\04`: `trail\04`}
	for in, want := range tests {
		if got := unescapeMount(in); got != want {
			t.Errorf("unescapeMount(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExistingAncestor(t *testing.T) {
	dir := t.TempDir()
	if got := existingAncestor(filepath.Join(dir, "a", "b")); got != dir {
		t.Fatalf("existingAncestor = %s, want %s", got, dir)
	}
}
