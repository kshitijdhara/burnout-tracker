// Package dashboard serves the local dashboard (system design §10,
// privacy & security §8.2): a web view of the user's own data on
// 127.0.0.1, and the user's controls (pause, forget, annotations, the
// weekly check-in). A browser tab on any website can reach localhost, so the
// server trusts nothing a page can forge:
//
//   - it listens on 127.0.0.1 only, on a random port;
//   - every request must carry Host 127.0.0.1:<port> or localhost:<port>,
//     which defeats DNS rebinding;
//   - access starts from a one-time launch token (single use, 60 s) that
//     `burnout dashboard` puts in the URL it opens; the server swaps it for
//     an HttpOnly, SameSite=Strict session cookie and redirects to a
//     token-free URL;
//   - requests a browser marks as cross-site are refused;
//   - GET and HEAD are served everywhere; POST and DELETE only on the
//     write routes (writes.go), and only from the page itself: marked
//     same-origin (or unmarked with a matching Origin) and carrying the
//     session's CSRF token in a header;
//   - no CORS headers; a strict Content-Security-Policy with no inline
//     script; framing denied; nothing cached.
package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

//go:embed assets
var assets embed.FS

// TokenTTL is how long a launch token stays valid.
const TokenTTL = 60 * time.Second

// Source supplies the dashboard's data. Every method is called per request
// and must be safe for concurrent use.
type Source interface {
	Overview(ctx context.Context) (Overview, error)
	Day(ctx context.Context, date string) (Day, error) // "" means today
	Week(ctx context.Context) (Week, error)
	Privacy(ctx context.Context) (Privacy, error)
}

// Server is one dashboard run.
type Server struct {
	src  Source
	ctl  Control // nil: the write routes are off
	log  *slog.Logger
	now  func() time.Time
	port int

	mu       sync.Mutex
	launch   string // the outstanding launch token; empty once used
	expires  time.Time
	sessions map[string]string // session cookie → its CSRF token

	ln     net.Listener
	srv    *http.Server
	writes *http.ServeMux
}

// Listen opens the server on 127.0.0.1 at a random port. ctl may be nil,
// which leaves the dashboard read-only.
func Listen(src Source, ctl Control, log *slog.Logger) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("dashboard: %w", err)
	}
	return newServer(src, ctl, log, ln, time.Now), nil
}

func newServer(src Source, ctl Control, log *slog.Logger, ln net.Listener, now func() time.Time) *Server {
	s := &Server{
		src: src, ctl: ctl, log: log, now: now, ln: ln,
		port:     ln.Addr().(*net.TCPAddr).Port,
		sessions: map[string]string{},
	}
	s.srv = &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	return s
}

// LaunchURL issues a fresh one-time launch token (replacing any unused
// one) and returns the URL that redeems it.
func (s *Server) LaunchURL() string { return s.LaunchURLTo("") }

// landings maps the views a launch link may open (they match app.js) to
// where the redeemed link goes. Only these constant paths are ever
// redirected to.
var landings = map[string]string{
	"overview": "/#overview", "today": "/#today", "week": "/#week", "privacy": "/#privacy",
}

// LaunchURLTo is LaunchURL for a link that opens view (e.g. "privacy").
// An unknown view opens the default one.
func (s *Server) LaunchURLTo(view string) string {
	tok := randomToken()
	s.mu.Lock()
	s.launch, s.expires = tok, s.now().Add(TokenTTL)
	s.mu.Unlock()
	u := fmt.Sprintf("http://127.0.0.1:%d/launch?t=%s", s.port, tok)
	if _, ok := landings[view]; ok {
		u += "&v=" + view
	}
	return u
}

// landing is where a redeemed launch link goes: the page, at its view.
func landing(r *http.Request) string {
	if to, ok := landings[r.URL.Query().Get("v")]; ok {
		return to
	}
	return "/"
}

// Serve runs until ctx ends.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shut)
	}()
	if err := s.srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("dashboard: %w", err)
	}
	return nil
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("dashboard: no randomness: " + err.Error()) // crypto/rand never fails on supported systems
	}
	return hex.EncodeToString(b)
}

func (s *Server) cookieName() string { return "burnout_session_" + strconv.Itoa(s.port) }

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /launch", s.redeem)
	mux.HandleFunc("GET /api/overview", s.api(func(r *http.Request) (any, error) { return s.src.Overview(r.Context()) }))
	mux.HandleFunc("GET /api/day", s.api(func(r *http.Request) (any, error) {
		date := r.URL.Query().Get("date")
		if date != "" {
			if _, err := time.Parse(time.DateOnly, date); err != nil {
				return nil, errBadRequest
			}
		}
		return s.src.Day(r.Context(), date)
	}))
	mux.HandleFunc("GET /api/week", s.api(func(r *http.Request) (any, error) { return s.src.Week(r.Context()) }))
	mux.HandleFunc("GET /api/privacy", s.api(func(r *http.Request) (any, error) { return s.src.Privacy(r.Context()) }))
	mux.HandleFunc("GET /api/session", s.api(s.session))
	mux.HandleFunc("GET /api/annotations", s.api(s.annotations))
	mux.HandleFunc("GET /api/checkin", s.api(func(*http.Request) (any, error) { return weeklyCheckin(), nil }))
	static, _ := fs.Sub(assets, "assets")
	mux.Handle("GET /", http.FileServerFS(static))
	s.writes = s.writeRoutes()
	return s.guard(mux)
}

var errBadRequest = errors.New("bad request")

// guard applies the checks every request passes before routing.
func (s *Server) guard(next http.Handler) http.Handler {
	allowed := map[string]bool{
		"127.0.0.1:" + strconv.Itoa(s.port): true,
		"localhost:" + strconv.Itoa(s.port): true,
	}
	origins := map[string]bool{}
	for h := range allowed {
		origins["http://"+h] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; "+
			"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")

		if !allowed[r.Host] {
			http.Error(w, "unknown host", http.StatusMisdirectedRequest)
			return
		}
		// Anything but GET and HEAD must be one of the write routes, and
		// come from the page itself: a fetch the browser marks
		// same-origin, or (a browser without Fetch Metadata) an unmarked
		// one whose Origin is this server.
		write := r.Method != http.MethodGet && r.Method != http.MethodHead
		if write {
			if _, pattern := s.writes.Handler(r); pattern == "" {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			site := r.Header.Get("Sec-Fetch-Site")
			if site != "same-origin" && (site != "" || !origins[r.Header.Get("Origin")]) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		// Browsers label requests a page makes to another site; the
		// dashboard only ever talks to itself. A launch link opened from
		// the terminal carries "none".
		switch r.Header.Get("Sec-Fetch-Site") {
		case "", "same-origin":
		case "none":
			if r.URL.Path != "/launch" && r.URL.Path != "/" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		default:
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		csrf, ok := s.sessionToken(r)
		if r.URL.Path != "/launch" && !ok {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintln(w, "Open the dashboard with `burnout dashboard`: it opens a one-time link.")
			return
		}
		if write {
			// The cookie alone proves nothing: a forged form or fetch
			// carries it too. Only the page can read the token.
			got := r.Header.Get(csrfHeader)
			if len(got) != len(csrf) || subtle.ConstantTimeCompare([]byte(got), []byte(csrf)) != 1 {
				http.Error(w, "missing or wrong CSRF token", http.StatusForbidden)
				return
			}
			s.writes.ServeHTTP(noStore{w}, r)
			return
		}
		next.ServeHTTP(noStore{w}, r)
	})
}

// noStore keeps Cache-Control: no-store on every response, including the
// errors http.FileServer writes after clearing it.
type noStore struct{ http.ResponseWriter }

func (n noStore) WriteHeader(code int) {
	n.Header().Set("Cache-Control", "no-store")
	n.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the connection.
func (n noStore) Unwrap() http.ResponseWriter { return n.ResponseWriter }

func (s *Server) hasSession(r *http.Request) bool {
	_, ok := s.sessionToken(r)
	return ok
}

// sessionToken returns the CSRF token of the request's session, if it has
// one.
func (s *Server) sessionToken(r *http.Request) (string, bool) {
	c, err := r.Cookie(s.cookieName())
	if err != nil || len(c.Value) != 64 {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.sessions[c.Value]
	return tok, ok
}

// redeem swaps a valid launch token for a session cookie, once.
func (s *Server) redeem(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("t")
	s.mu.Lock()
	ok := s.launch != "" && len(tok) == len(s.launch) &&
		subtle.ConstantTimeCompare([]byte(tok), []byte(s.launch)) == 1 && s.now().Before(s.expires)
	if ok {
		s.launch = "" // single use
	}
	session := ""
	if ok {
		session = randomToken()
		s.sessions[session] = randomToken() // its CSRF token
	}
	s.mu.Unlock()
	if !ok {
		if s.hasSession(r) {
			http.Redirect(w, r, "/", http.StatusSeeOther) // a reload of an already used link
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintln(w, "This link has expired or was already used. Run `burnout dashboard` again for a new one.")
		return
	}
	// Not Secure: the server is plain HTTP on loopback, which never leaves
	// the machine, and Safari refuses Secure cookies from http://127.0.0.1.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // see above
		Name: s.cookieName(), Value: session, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, landing(r), http.StatusSeeOther)
}

// api serves fn's result as JSON, or 204 when there is none. Errors are
// logged, not shown, since they can name paths; only a badRequest's text
// reaches the page.
func (s *Server) api(fn func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := fn(r)
		var bad badRequest
		switch {
		case errors.Is(err, errBadRequest):
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		case errors.As(err, &bad):
			http.Error(w, bad.Error(), http.StatusBadRequest)
			return
		case errors.Is(err, errNotFound):
			http.Error(w, "not found; reload the page", http.StatusNotFound)
			return
		case errors.Is(err, errWritesOff):
			http.Error(w, "this dashboard can't change your data; use the burnout command", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			s.log.Warn("dashboard query failed", "path", r.URL.Path, "err", err)
			http.Error(w, "couldn't read your data; see the terminal running `burnout dashboard`", http.StatusInternalServerError)
			return
		}
		if v == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(v)
	}
}
