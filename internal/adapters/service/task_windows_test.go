//go:build windows

package service

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

// TestTaskImports has Task Scheduler itself check the rendered XML, as
// plutil and systemd-analyze do for the others: it imports it under a
// throwaway name, never runs it, and deletes it. Only in CI, so it never
// touches a developer's Task Scheduler.
func TestTaskImports(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("changes Task Scheduler; runs in CI only")
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	def, err := RenderTask(exe, testArgs, u.Uid)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "task.xml")
	if err := os.WriteFile(path, def, 0o600); err != nil {
		t.Fatal(err)
	}
	name := "burntout-test-" + filepath.Base(t.TempDir())
	r := ExecRunner{}
	if out, err := r.Run("schtasks", "/Create", "/XML", path, "/TN", name, "/F"); err != nil {
		t.Fatalf("schtasks /Create: %v\n%s", err, out)
	}
	defer func() {
		if out, err := r.Run("schtasks", "/Delete", "/TN", name, "/F"); err != nil {
			t.Errorf("schtasks /Delete: %v\n%s", err, out)
		}
	}()
	if out, err := r.Run("schtasks", "/Query", "/TN", name); err != nil {
		t.Fatalf("schtasks /Query: %v\n%s", err, out)
	}
}
