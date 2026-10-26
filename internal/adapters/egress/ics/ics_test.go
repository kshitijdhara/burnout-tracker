package ics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

const token = "SECRETtoken42"

func src(t *testing.T, s string) Source {
	t.Helper()
	got, err := Parse(model.NewSensitive(s))
	if err != nil {
		t.Fatalf("%s: %v", s, err)
	}
	return got
}

// testClient is Client(), trusting srv's certificate.
func testClient(srv *httptest.Server) *http.Client {
	c := Client()
	c.Transport.(*http.Transport).TLSClientConfig.RootCAs = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	return c
}

func TestParse(t *testing.T) {
	s := src(t, "webcal://calendar.example.com/feed/"+token+".ics")
	if !s.Remote() || s.Describe() != "calendar.example.com" || !strings.HasPrefix(s.Value().Reveal(), "https://") {
		t.Errorf("webcal: %+v %s", s, s.Value().Reveal())
	}
	if strings.Contains(s.Describe(), token) {
		t.Error("Describe leaks the token")
	}
	for _, bad := range []string{
		"http://calendar.example.com/" + token, "ftp://x/y", "https://", "https://user:pass@host/x", "/no/such/file.ics",
	} {
		_, err := Parse(model.NewSensitive(bad))
		if err == nil {
			t.Errorf("%q accepted", bad)
		} else if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "pass") {
			t.Errorf("error leaks the URL: %v", err)
		}
	}
	file := filepath.Join(t.TempDir(), "work.ics")
	if err := os.WriteFile(file, []byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := src(t, file)
	if f.Remote() || f.Describe() != "work.ics" {
		t.Errorf("file: %+v", f)
	}
	r, err := Open(context.Background(), f, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	_ = r.Close()
	if !strings.HasPrefix(string(b), "BEGIN:VCALENDAR") {
		t.Errorf("file contents %q", b)
	}
}

func TestFetch(t *testing.T) {
	var gotUA, gotCookie string
	mux := http.NewServeMux()
	mux.HandleFunc("/feed/", func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotCookie = r.UserAgent(), r.Header.Get("Cookie")
		http.SetCookie(w, &http.Cookie{Name: "track", Value: "1"})
		_, _ = io.WriteString(w, "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n")
	})
	mux.HandleFunc("/denied/", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusForbidden) })
	mux.HandleFunc("/downgrade/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/feed/"+token, http.StatusFound)
	})
	mux.HandleFunc("/huge/", func(w http.ResponseWriter, _ *http.Request) {
		chunk := strings.Repeat("x", 1<<20)
		for range MaxBytes>>20 + 1 {
			_, _ = io.WriteString(w, chunk)
		}
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	client := testClient(srv)
	ctx := context.Background()

	for range 2 { // the second request must not send the cookie back
		r, err := Open(ctx, src(t, srv.URL+"/feed/"+token), client)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(r)
		_ = r.Close()
	}
	if gotUA != userAgent || gotCookie != "" {
		t.Errorf("sent User-Agent %q, Cookie %q", gotUA, gotCookie)
	}

	for path, want := range map[string]string{"/denied/": "403", "/downgrade/": "redirected away from https"} {
		_, err := Open(ctx, src(t, srv.URL+path+token), client)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", path, err, want)
		}
		if err != nil && strings.Contains(err.Error(), token) {
			t.Errorf("%s: error leaks the URL: %v", path, err)
		}
	}

	r, err := Open(ctx, src(t, srv.URL+"/huge/"+token), client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); !errors.Is(err, ErrTooLarge) {
		t.Errorf("huge feed: %v", err)
	}
	_ = r.Close()

	// An untrusted certificate fails, and the error doesn't carry the URL.
	_, err = Open(ctx, src(t, srv.URL+"/feed/"+token), Client())
	if err == nil || strings.Contains(err.Error(), token) {
		t.Errorf("self-signed certificate: %v", err)
	}
}
