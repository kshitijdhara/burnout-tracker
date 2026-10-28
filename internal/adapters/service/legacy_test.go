package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Legacy manager finds and removes the service as it was named before
// the rename, and never touches the current one.
func TestLegacyUninstall(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			r := &fakeRunner{}
			m := manager(t, goos, r)
			m.SID = "S-1-5-21-1"
			old := m.Legacy()
			b, err := old.backend()
			if err != nil {
				t.Fatal(err)
			}
			path := b.path(old)
			if path == "" {
				t.Fatal("no legacy definition path")
			}
			current, _ := m.Path()
			if path == current {
				t.Fatalf("legacy and current definitions share %s", path)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			if st, _ := old.Status(); !st.Installed {
				t.Fatalf("legacy status: %+v", st)
			}
			r.calls = nil
			if err := old.Uninstall(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("legacy definition still at %s", path)
			}
			legacyName := map[string]string{"darwin": LegacyLabel, "linux": LegacyUnitName, "windows": legacyTaskName + m.SID}[goos]
			currentName := map[string]string{"darwin": Label, "linux": UnitName, "windows": TaskName(m.SID)}[goos]
			sawLegacy := false
			for _, c := range r.calls {
				if strings.Contains(c, currentName) {
					t.Errorf("a legacy uninstall touched the current service: %s", c)
				}
				sawLegacy = sawLegacy || strings.Contains(c, legacyName)
			}
			if len(r.calls) > 0 && !sawLegacy {
				t.Errorf("no command named the legacy service: %q", r.calls)
			}
		})
	}
}
