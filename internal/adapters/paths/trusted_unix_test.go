//go:build !windows

package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckTrustedExecutable(t *testing.T) {
	// Not t.TempDir: /tmp is writable by everyone, which the check rejects.
	base, err := os.MkdirTemp(".", "trusted-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if base, err = filepath.Abs(base); err != nil {
		t.Fatal(err)
	}
	mkbin := func(dir string, mode os.FileMode) string {
		t.Helper()
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(dir, "burntout")
		if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // a test executable
			t.Fatal(err)
		}
		return bin
	}
	good := mkbin(filepath.Join(base, "good"), 0o755)
	shared := mkbin(filepath.Join(base, "shared"), 0o777)
	link := filepath.Join(base, "good", "link")
	if err := os.Symlink(shared, link); err != nil {
		t.Fatal(err)
	}

	if err := CheckTrustedExecutable(good); err != nil {
		t.Fatalf("own folder rejected: %v", err)
	}
	for name, p := range map[string]string{
		"world-writable folder":     shared,
		"link into a shared folder": link,
		"relative path":             filepath.Join("good", "burntout"),
		"missing file":              filepath.Join(base, "good", "nope"),
	} {
		if err := CheckTrustedExecutable(p); err == nil {
			t.Errorf("%s: accepted %s", name, p)
		}
	}
}
