package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kshitijdhara/burntout/internal/adapters/portal"
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
	old := openFile
	openFile = func(target string) error { opened = append(opened, target); return nil }
	t.Cleanup(func() { openFile = old })

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

// openLaunchFile opens the path itself only when there is no desktop
// portal; whatever else the portal says is final, or a second tab would
// open on a spent token.
func TestOpenLaunchFileFallsBackOnlyWithoutAPortal(t *testing.T) {
	oldPortal, oldBrowser := portalOpen, browserOpen
	t.Cleanup(func() { portalOpen, browserOpen = oldPortal, oldBrowser })
	failed := errors.New("portal: the file couldn't be opened (response 2)")
	for name, tc := range map[string]struct {
		portal  error
		browser bool
		want    error
	}{
		"opened by the portal": {portal: nil},
		"no portal":            {portal: portal.ErrUnavailable, browser: true},
		"cancelled":            {portal: portal.ErrCancelled, want: portal.ErrCancelled},
		"no answer yet":        {portal: portal.ErrNoAnswer, want: portal.ErrNoAnswer},
		"the portal failed":    {portal: failed, want: failed},
	} {
		t.Run(name, func(t *testing.T) {
			browsed := false
			portalOpen = func(context.Context, string) error { return tc.portal }
			browserOpen = func(string) error { browsed = true; return nil }
			err := openLaunchFile("/x/dashboard-launch-1.html")
			if browsed != tc.browser {
				t.Errorf("browser opened: %v, want %v", browsed, tc.browser)
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// When the portal may still open the launch file, it stays until its token
// expires; when nothing will read it, it goes at once.
func TestOpenLaunchKeepsFileThePortalMayStillOpen(t *testing.T) {
	dataDir := t.TempDir()
	old := openFile
	t.Cleanup(func() { openFile = old })
	var path string
	openFile = func(target string) error { path = target; return portal.ErrNoAnswer }
	if err := openLaunch(dataDir, "http://127.0.0.1:4242/launch?t=x"); !errors.Is(err, portal.ErrNoAnswer) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the launch file went while the portal may still open it: %v", err)
	}
}
