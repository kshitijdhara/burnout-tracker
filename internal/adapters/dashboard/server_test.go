package dashboard

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSource struct{ fail bool }

func (f fakeSource) Overview(context.Context) (Overview, error) {
	if f.fail {
		return Overview{}, errors.New("open /home/someone/secret/path: denied")
	}
	v := 42.0
	return Overview{Today: "2026-10-26", Profile: "developer", Days: []DayScore{{Date: "2026-10-25", Status: "ok", Strain: &v, Band: "elevated"}}}, nil
}
func (fakeSource) Day(_ context.Context, date string) (Day, error) { return Day{Date: date}, nil }
func (fakeSource) Week(context.Context) (Week, error)              { return Week{From: "2026-10-19"}, nil }
func (fakeSource) Privacy(context.Context) (Privacy, error)        { return Privacy{}, nil }

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) get() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

func start(t *testing.T, src Source) (*Server, *clock, string) { return startWith(t, src, nil) }

func startWith(t *testing.T, src Source, ctl Control) (*Server, *clock, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{now: time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)}
	s := newServer(src, ctl, slog.New(slog.NewTextHandler(io.Discard, nil)), ln, c.get)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return s, c, "http://" + ln.Addr().String()
}

// client never follows redirects, so each step is visible.
var client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func request(t *testing.T, method, rawURL string, edit func(*http.Request)) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// login redeems a launch token and returns the session cookie.
func login(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	resp := request(t, "GET", s.LaunchURL(), nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("launch: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies %v", cookies)
	}
	return cookies[0]
}

func TestLaunchToView(t *testing.T) {
	s, _, _ := start(t, fakeSource{})
	for view, want := range map[string]string{"privacy": "/#privacy", "": "/", "javascript:alert(1)": "/"} {
		resp := request(t, "GET", s.LaunchURLTo(view), nil)
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != want {
			t.Errorf("view %q: %d %q, want %q", view, resp.StatusCode, resp.Header.Get("Location"), want)
		}
	}
}

func TestLaunchTokenAndSession(t *testing.T) {
	s, clk, base := start(t, fakeSource{})
	if r := request(t, "GET", base+"/api/overview", nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no session: %d", r.StatusCode)
	}
	if r := request(t, "GET", base+"/", nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("page without a session: %d", r.StatusCode)
	}

	launch := s.LaunchURL()
	resp := request(t, "GET", launch, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("launch: %d", resp.StatusCode)
	}
	c := resp.Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || len(c.Value) != 64 {
		t.Errorf("cookie %+v", c)
	}
	// The token works once.
	if r := request(t, "GET", launch, nil); r.StatusCode != http.StatusForbidden {
		t.Errorf("reused token: %d", r.StatusCode)
	}
	with := func(r *http.Request) { r.AddCookie(c) }
	resp = request(t, "GET", base+"/api/overview", with)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") || !strings.Contains(string(body), `"strain":42`) {
		t.Fatalf("overview: %d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	// A forged or wrong cookie gets nothing.
	if r := request(t, "GET", base+"/api/overview", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: c.Name, Value: strings.Repeat("a", 64)})
	}); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("forged cookie: %d", r.StatusCode)
	}
	// Tokens expire after 60 s.
	late := s.LaunchURL()
	clk.add(TokenTTL + time.Second)
	if r := request(t, "GET", late, nil); r.StatusCode != http.StatusForbidden {
		t.Errorf("expired token: %d", r.StatusCode)
	}
	// A wrong token of the right length is refused too.
	_ = s.LaunchURL()
	u, _ := url.Parse(late)
	u.RawQuery = "t=" + strings.Repeat("0", 64)
	if r := request(t, "GET", u.String(), nil); r.StatusCode != http.StatusForbidden {
		t.Errorf("guessed token: %d", r.StatusCode)
	}
}

func TestRequestChecks(t *testing.T) {
	s, _, base := start(t, fakeSource{})
	c := login(t, s)
	with := func(extra func(*http.Request)) func(*http.Request) {
		return func(r *http.Request) {
			r.AddCookie(c)
			if extra != nil {
				extra(r)
			}
		}
	}
	port := strings.TrimPrefix(base, "http://127.0.0.1")
	for name, tc := range map[string]struct {
		method, path string
		edit         func(*http.Request)
		want         int
	}{
		"DNS rebinding (foreign Host)":   {"GET", "/api/overview", func(r *http.Request) { r.Host = "evil.example" + port }, http.StatusMisdirectedRequest},
		"Host without the port":          {"GET", "/api/overview", func(r *http.Request) { r.Host = "localhost" }, http.StatusMisdirectedRequest},
		"localhost Host is fine":         {"GET", "/api/overview", func(r *http.Request) { r.Host = "localhost" + port }, http.StatusOK},
		"POST":                           {"POST", "/api/overview", nil, http.StatusMethodNotAllowed},
		"DELETE":                         {"DELETE", "/api/overview", nil, http.StatusMethodNotAllowed},
		"cross-site fetch":               {"GET", "/api/overview", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, http.StatusForbidden},
		"same-site (another port) fetch": {"GET", "/api/overview", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }, http.StatusForbidden},
		"typed API URL":                  {"GET", "/api/overview", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "none") }, http.StatusForbidden},
		"same-origin fetch":              {"GET", "/api/overview", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }, http.StatusOK},
		"bad date":                       {"GET", "/api/day?date=../../etc", nil, http.StatusBadRequest},
		"good date":                      {"GET", "/api/day?date=2026-10-20", nil, http.StatusOK},
		"page":                           {"GET", "/", nil, http.StatusOK},
		"script":                         {"GET", "/app.js", nil, http.StatusOK},
		"no such file":                   {"GET", "/server.go", nil, http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			r := request(t, tc.method, base+tc.path, with(tc.edit))
			if r.StatusCode != tc.want {
				t.Errorf("%s %s: %d, want %d", tc.method, tc.path, r.StatusCode, tc.want)
			}
			h := r.Header
			csp := h.Get("Content-Security-Policy")
			if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "unsafe") {
				t.Errorf("CSP %q", csp)
			}
			if h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" ||
				h.Get("Referrer-Policy") != "no-referrer" {
				t.Errorf("headers %v", h)
			}
			for k := range h {
				if strings.HasPrefix(k, "Access-Control-") {
					t.Errorf("CORS header %s", k)
				}
			}
		})
	}
}

func TestErrorsDontLeak(t *testing.T) {
	s, _, base := start(t, fakeSource{fail: true})
	c := login(t, s)
	r := request(t, "GET", base+"/api/overview", func(r *http.Request) { r.AddCookie(c) })
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusInternalServerError || strings.Contains(string(body), "secret") {
		t.Errorf("%d %s", r.StatusCode, body)
	}
}

// The page must work under the CSP: no inline script, styles or event
// handlers, and the script never parses data as HTML.
func TestAssetsFitTheCSP(t *testing.T) {
	read := func(name string) string {
		b, err := fs.ReadFile(assets, "assets/"+name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	html := read("index.html")
	if regexp.MustCompile(`<script(?:\s[^>]*)?>[^<]+</script>`).MatchString(html) ||
		regexp.MustCompile(`(?i)\son[a-z]+=|\sstyle=|<style`).MatchString(html) {
		t.Error("index.html has inline script, style or an event handler")
	}
	for _, src := range regexp.MustCompile(`(?:src|href)="([^"#]+)"`).FindAllStringSubmatch(html, -1) {
		if !strings.HasPrefix(src[1], "/") {
			t.Errorf("index.html loads %q from elsewhere", src[1])
		}
	}
	js := read("app.js")
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function"} {
		if strings.Contains(js, bad) {
			t.Errorf("app.js uses %s", bad)
		}
	}
	if js := strings.ReplaceAll(js, `"http://www.w3.org/2000/svg"`, ""); strings.Contains(js, "http://") || strings.Contains(js, "https://") {
		t.Error("app.js names an outside URL")
	}
}
