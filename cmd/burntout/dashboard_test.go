package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestOpenLaunchKeepsTokenOffArgv: the one-time link reaches the browser
// through an owner-only file, so the command that opens it, which every
// local user can read, carries only the file's path (privacy & security
// §2, T1).
func TestOpenLaunchKeepsTokenOffArgv(t *testing.T) {
	const token = "CANARYtoken0123456789abcdef"
	link := "http://127.0.0.1:4242/launch?t=" + token
	dataDir := t.TempDir()
	var opened []string
	openFile = func(target string) error { opened = append(opened, target); return nil }
	t.Cleanup(func() { openFile = openBrowser })

	if err := openLaunch(dataDir, link); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 {
		t.Fatalf("opened %v", opened)
	}
	path := opened[0]
	if filepath.Dir(path) != dataDir || strings.Contains(path, token) {
		t.Errorf("opened %s", path)
	}
	for _, arg := range browserCommand(path).Args {
		if strings.Contains(arg, token) {
			t.Errorf("the token is on the command line: %q", browserCommand(path).Args)
		}
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("launch file mode: %v %v", fi.Mode(), err)
		}
	}
	b, err := os.ReadFile(path) //nolint:gosec // test file
	if err != nil || !strings.Contains(string(b), `content="0;url=`+link+`"`) {
		t.Errorf("launch file: %v\n%s", err, b)
	}

	// If the browser can't be started, the file goes at once.
	openFile = func(target string) error { opened = append(opened, target); return errors.New("no browser") }
	if err := openLaunch(dataDir, link); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Stat(opened[1]); !os.IsNotExist(err) {
		t.Errorf("launch file kept after a failed open: %v", err)
	}
}
