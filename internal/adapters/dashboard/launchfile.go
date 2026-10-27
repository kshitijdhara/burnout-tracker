package dashboard

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Launch files carry the one-time link from burnout to the browser. A
// command line is readable by every local user (ps, /proc/*/cmdline), and
// whoever redeems the token first gets the session (privacy & security
// §2, T1), so the link goes into an owner-only file in the data folder and
// the browser is pointed at the file. The file names nothing secret.
const (
	launchPrefix = "dashboard-launch-"
	launchSuffix = ".html"
	// LaunchFileTTL is how long a launch file is kept: past it, its token
	// has expired anyway.
	LaunchFileTTL = TokenTTL + 30*time.Second
)

// launchPage moves the browser on to the launch link by itself, with no
// script, and shows the link in case meta refresh is off.
const launchPage = `<!doctype html>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'">
<meta name="referrer" content="no-referrer">
<meta http-equiv="refresh" content="0;url=%s">
<title>burnout</title>
<p><a href="%s">Open the dashboard</a></p>
`

// WriteLaunchFile writes a new owner-only launch file for url into dir
// and returns its path. It first removes launch files older than
// LaunchFileTTL, left by a run that stopped before removing its own.
func WriteLaunchFile(dir, url string, now time.Time) (path string, err error) {
	sweepLaunchFiles(dir, now)
	f, err := os.CreateTemp(dir, launchPrefix+"*"+launchSuffix) // 0600
	if err != nil {
		return "", fmt.Errorf("dashboard: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()
	u := html.EscapeString(url)
	if _, err := fmt.Fprintf(f, launchPage, u, u); err != nil {
		return "", fmt.Errorf("dashboard: %w", err)
	}
	return f.Name(), nil
}

// sweepLaunchFiles removes the launch files in dir older than
// LaunchFileTTL. Newer ones may belong to another dashboard (the tray's
// and the CLI's) whose browser hasn't read them yet.
func sweepLaunchFiles(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(name, launchPrefix) || !strings.HasSuffix(name, launchSuffix) {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > LaunchFileTTL {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}
