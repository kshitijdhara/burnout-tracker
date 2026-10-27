package dashboard

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestLaunchFile: the launch file is owner-only, carries the link only in
// a script-free redirect, and old ones are swept.
func TestLaunchFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	stale := filepath.Join(dir, launchPrefix+"old"+launchSuffix)
	fresh := filepath.Join(dir, launchPrefix+"other"+launchSuffix)
	other := filepath.Join(dir, "minutes.db")
	for _, p := range []string{stale, fresh, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(stale, now.Add(-LaunchFileTTL-time.Second), now.Add(-LaunchFileTTL-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, now.Add(-time.Second), now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}

	const link = "http://127.0.0.1:1234/launch?t=abc123&v=privacy"
	path, err := WriteLaunchFile(dir, link, now)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), launchPrefix) || strings.Contains(path, "abc123") {
		t.Errorf("path %s", path)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("launch file mode: %v %v", fi.Mode(), err)
		}
	}
	b, err := os.ReadFile(path) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	escaped := "http://127.0.0.1:1234/launch?t=abc123&amp;v=privacy"
	if !strings.Contains(page, `<meta http-equiv="refresh" content="0;url=`+escaped+`">`) ||
		!strings.Contains(page, `<a href="`+escaped+`">`) ||
		!strings.Contains(page, `content="default-src 'none'"`) || strings.Contains(page, "<script") {
		t.Errorf("launch page:\n%s", page)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale launch file kept: %v", err)
	}
	for _, p := range []string{fresh, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed: %v", p, err)
		}
	}
	second, err := WriteLaunchFile(dir, link, now)
	if err != nil || second == path {
		t.Errorf("second launch file %s, %v", second, err)
	}
}

// TestLaunchFromFile: the launch file's page is another origin, so the
// browser marks its navigation cross-site. The token is still accepted,
// and the answer is a page that moves on by itself, so the next request
// is same-origin and carries the Strict cookie. Nothing else is opened
// to cross-site requests.
func TestLaunchFromFile(t *testing.T) {
	s, _, base := start(t, fakeSource{})
	for _, site := range []string{"cross-site", "same-site"} {
		resp := request(t, "GET", s.LaunchURLTo("privacy"), func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", site) })
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || len(resp.Cookies()) != 1 ||
			!strings.Contains(string(body), `<meta http-equiv="refresh" content="0;url=/#privacy">`) || strings.Contains(string(body), "<script") {
			t.Fatalf("%s launch: %d %v\n%s", site, resp.StatusCode, resp.Cookies(), body)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
			t.Errorf("CSP %q", csp)
		}
		c := resp.Cookies()[0]
		same := func(r *http.Request) { r.AddCookie(c); r.Header.Set("Sec-Fetch-Site", "same-origin") }
		if r := request(t, "GET", base+"/", same); r.StatusCode != http.StatusOK {
			t.Errorf("%s: the page after the bounce: %d", site, r.StatusCode)
		}
		for _, path := range []string{"/", "/api/overview", "/api/session"} {
			cross := func(r *http.Request) { r.AddCookie(c); r.Header.Set("Sec-Fetch-Site", site) }
			if r := request(t, "GET", base+path, cross); r.StatusCode != http.StatusForbidden {
				t.Errorf("%s GET %s: %d", site, path, r.StatusCode)
			}
		}
	}
	// A cross-site link with a wrong token gets nothing.
	_ = s.LaunchURL()
	resp := request(t, "GET", base+"/launch?t="+strings.Repeat("0", 64), func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	if resp.StatusCode != http.StatusForbidden || len(resp.Cookies()) != 0 {
		t.Errorf("cross-site wrong token: %d %v", resp.StatusCode, resp.Cookies())
	}
}

// TestSessionLimits: a session ends after SessionIdle without a request,
// and past MaxSessions the one used longest ago goes.
func TestSessionLimits(t *testing.T) {
	s, clk, base := start(t, fakeSource{})
	get := func(c *http.Cookie) int {
		return request(t, "GET", base+"/api/overview", func(r *http.Request) { r.AddCookie(c) }).StatusCode
	}
	idle := login(t, s)
	clk.add(SessionIdle - time.Minute)
	if code := get(idle); code != http.StatusOK {
		t.Fatalf("session within its idle time: %d", code)
	}
	clk.add(SessionIdle - time.Minute) // used a minute ago counts from then
	if code := get(idle); code != http.StatusOK {
		t.Fatalf("session kept alive by use: %d", code)
	}
	clk.add(SessionIdle + time.Second)
	if code := get(idle); code != http.StatusUnauthorized {
		t.Fatalf("idle session: %d", code)
	}

	var cs []*http.Cookie
	for range MaxSessions {
		cs = append(cs, login(t, s))
		clk.add(time.Second)
	}
	if code := get(cs[0]); code != http.StatusOK { // now the most recently used
		t.Fatalf("first session: %d", code)
	}
	clk.add(time.Second)
	_ = login(t, s)
	if code := get(cs[1]); code != http.StatusUnauthorized {
		t.Errorf("least recently used session kept past the cap: %d", code)
	}
	for _, c := range append([]*http.Cookie{cs[0]}, cs[2:]...) {
		if code := get(c); code != http.StatusOK {
			t.Errorf("session dropped: %d", code)
		}
	}
	s.mu.Lock()
	n := len(s.sessions)
	s.mu.Unlock()
	if n != MaxSessions {
		t.Errorf("%d sessions, want %d", n, MaxSessions)
	}
}
