package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/annotation"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/checkin"
)

// memControl is a Control in memory, for tests and the preview.
type memControl struct {
	mu       sync.Mutex
	paused   string
	resumed  int
	forgot   []string
	notes    []annotation.Annotation
	checkins []checkin.Record
	fail     bool
}

func (m *memControl) Pause(_ context.Context, length string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.paused = length
	return nil
}

func (m *memControl) Resume(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.paused, m.resumed = "", m.resumed+1
	return nil
}

func (m *memControl) Forget(_ context.Context, span string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("forget /home/someone/secret: the daemon didn't answer")
	}
	m.forgot = append(m.forgot, span)
	return nil
}

func (m *memControl) Annotations(context.Context) ([]Annotation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Annotation
	for _, a := range m.notes {
		out = append(out, Annotation{ID: a.ID, Text: fmt.Sprintf("%s, %s to %s: %s", a.Kind, a.From, a.To, a.Note)})
	}
	return out, nil
}

func (m *memControl) AddAnnotation(_ context.Context, a annotation.Annotation) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a.ID = int64(len(m.notes) + 1)
	m.notes = append(m.notes, a)
	return a.ID, nil
}

func (m *memControl) RemoveAnnotation(_ context.Context, id int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, a := range m.notes {
		if a.ID == id {
			m.notes = append(m.notes[:i], m.notes[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (m *memControl) SaveCheckins(_ context.Context, rs []checkin.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkins = append(m.checkins, rs...)
	return nil
}

func (*memControl) Region() string { return "GB" }

// writer is a logged-in page: its cookie and CSRF token.
type writer struct {
	t      *testing.T
	base   string
	cookie *http.Cookie
	csrf   string
}

func loginWriter(t *testing.T, s *Server, base string) writer {
	t.Helper()
	c := login(t, s)
	resp := request(t, "GET", base+"/api/session", func(r *http.Request) { r.AddCookie(c) })
	var sess Session
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil || len(sess.CSRF) != 64 {
		t.Fatalf("session: %d %v %+v", resp.StatusCode, err, sess)
	}
	return writer{t: t, base: base, cookie: c, csrf: sess.CSRF}
}

// do sends a write as the page does: same-origin, with the token and a
// JSON body. edit runs last, to break one of those.
func (w writer) do(method, path, body string, edit func(*http.Request)) (int, string) {
	w.t.Helper()
	req, err := http.NewRequest(method, w.base+path, strings.NewReader(body))
	if err != nil {
		w.t.Fatal(err)
	}
	req.AddCookie(w.cookie)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set(csrfHeader, w.csrf)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if edit != nil {
		edit(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// every write route, with a body it accepts.
var writeCases = []struct{ method, path, body string }{
	{"POST", "/api/pause", `{"for":"1h"}`},
	{"POST", "/api/resume", `{}`},
	{"DELETE", "/api/data?range=hour", ""},
	{"POST", "/api/annotations", `{"kind":"vacation","from":"2026-11-02","to":"2026-11-06"}`},
	{"DELETE", "/api/annotations/1", ""},
	{"POST", "/api/checkins", fullCheckin(100)},
}

// fullCheckin answers every weekly item with v.
func fullCheckin(v int) string {
	all := map[string]checkin.Answers{}
	for _, inst := range checkin.Weekly() {
		all[inst.ID] = checkin.Answers{}
		for _, it := range inst.Items {
			all[inst.ID][it.ID] = v
		}
	}
	b, _ := json.Marshal(map[string]any{"answers": all})
	return string(b)
}

func TestWritesNeedTheirOwnPage(t *testing.T) {
	ctl := &memControl{}
	s, _, base := startWith(t, fakeSource{}, ctl)
	w := loginWriter(t, s, base)
	other := loginWriter(t, s, base)
	port := strings.TrimPrefix(base, "http://127.0.0.1")
	for name, edit := range map[string]func(*http.Request){
		"no token":                     func(r *http.Request) { r.Header.Del(csrfHeader) },
		"wrong token":                  func(r *http.Request) { r.Header.Set(csrfHeader, strings.Repeat("0", 64)) },
		"short token":                  func(r *http.Request) { r.Header.Set(csrfHeader, w.csrf[:10]) },
		"another session's token":      func(r *http.Request) { r.Header.Set(csrfHeader, other.csrf) },
		"token in the query":           func(r *http.Request) { r.Header.Del(csrfHeader); r.URL.RawQuery += "&csrf=" + w.csrf },
		"cross-site":                   func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"same-site (another port)":     func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
		"typed or bookmarked":          func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "none") },
		"no fetch metadata, no Origin": func(r *http.Request) { r.Header.Del("Sec-Fetch-Site") },
		"no fetch metadata, foreign": func(r *http.Request) {
			r.Header.Del("Sec-Fetch-Site")
			r.Header.Set("Origin", "http://evil.example"+port)
		},
		"no fetch metadata, null":       func(r *http.Request) { r.Header.Del("Sec-Fetch-Site"); r.Header.Set("Origin", "null") },
		"no fetch metadata, other port": func(r *http.Request) { r.Header.Del("Sec-Fetch-Site"); r.Header.Set("Origin", "http://127.0.0.1:1") },
		"no session":                    func(r *http.Request) { r.Header.Del("Cookie") },
	} {
		for _, c := range writeCases {
			code, _ := w.do(c.method, c.path, c.body, edit)
			want := http.StatusForbidden
			if name == "no session" {
				want = http.StatusUnauthorized
			}
			if code != want {
				t.Errorf("%s: %s %s: %d, want %d", name, c.method, c.path, code, want)
			}
		}
	}
	if ctl.paused != "" || ctl.resumed != 0 || len(ctl.forgot) != 0 || len(ctl.notes) != 0 || len(ctl.checkins) != 0 {
		t.Errorf("a refused write changed something: %+v", ctl)
	}
	// Without fetch metadata, a matching Origin is the page.
	for _, origin := range []string{base, "http://localhost" + port} {
		code, _ := w.do("POST", "/api/resume", `{}`, func(r *http.Request) {
			r.Header.Del("Sec-Fetch-Site")
			r.Header.Set("Origin", origin)
			if strings.Contains(origin, "localhost") {
				r.Host = "localhost" + port
			}
		})
		if code != http.StatusNoContent {
			t.Errorf("Origin %s: %d", origin, code)
		}
	}
}

func TestOnlyWriteRoutesTakeWrites(t *testing.T) {
	s, _, base := startWith(t, fakeSource{}, &memControl{})
	w := loginWriter(t, s, base)
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/overview"}, {"DELETE", "/api/privacy"}, {"PUT", "/api/pause"}, {"PATCH", "/api/annotations/1"},
		{"GET", "/api/pause"}, {"POST", "/api/data"}, {"DELETE", "/api/annotations"}, {"POST", "/launch"}, {"POST", "/"},
	} {
		want := http.StatusMethodNotAllowed
		if c.method == "GET" {
			want = http.StatusNotFound // reads stay reads; the file server finds no such file
		}
		if code, _ := w.do(c.method, c.path, "", nil); code != want {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, code, want)
		}
	}
	// GETs still need no token.
	for _, path := range []string{"/api/overview", "/api/session", "/api/annotations", "/api/checkin"} {
		if code, _ := w.do("GET", path, "", func(r *http.Request) { r.Header.Del(csrfHeader) }); code != http.StatusOK {
			t.Errorf("GET %s without a token: %d", path, code)
		}
	}
	if r := request(t, "GET", base+"/api/session", nil); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("session without a cookie: %d", r.StatusCode)
	}
}

func TestWriteRoutes(t *testing.T) {
	ctl := &memControl{}
	s, _, base := startWith(t, fakeSource{}, ctl)
	w := loginWriter(t, s, base)
	expect := func(method, path, body string, want int) string {
		t.Helper()
		code, out := w.do(method, path, body, nil)
		if code != want {
			t.Fatalf("%s %s %s: %d %s, want %d", method, path, body, code, out, want)
		}
		return out
	}

	for _, p := range []string{PauseHour, PauseTomorrow, PauseResumed} {
		expect("POST", "/api/pause", `{"for":"`+p+`"}`, http.StatusNoContent)
		if ctl.paused != p {
			t.Errorf("paused %q, want %q", ctl.paused, p)
		}
	}
	expect("POST", "/api/pause", `{"for":"forever"}`, http.StatusBadRequest)
	expect("POST", "/api/pause", `{"for":"1h","extra":1}`, http.StatusBadRequest)
	expect("POST", "/api/pause", `{"for":"1h"}{}`, http.StatusBadRequest)
	expect("POST", "/api/resume", `{}`, http.StatusNoContent)
	if ctl.paused != "" || ctl.resumed != 1 {
		t.Errorf("resume: %+v", ctl)
	}
	if code, _ := w.do("POST", "/api/resume", `{}`, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }); code != http.StatusBadRequest {
		t.Errorf("a form-encodable body type: %d", code)
	}

	expect("DELETE", "/api/data?range=hour", "", http.StatusNoContent)
	expect("DELETE", "/api/data?range=today", "", http.StatusNoContent)
	expect("DELETE", "/api/data?range=all", "", http.StatusBadRequest)
	expect("DELETE", "/api/data", "", http.StatusBadRequest)
	if strings.Join(ctl.forgot, ",") != "hour,today" {
		t.Errorf("forgot %v", ctl.forgot)
	}

	out := expect("POST", "/api/annotations", `{"kind":"on_call","from":"2026-10-20","to":"2026-10-22","note":"pager"}`, http.StatusOK)
	if out != "{\"id\":1}\n" {
		t.Errorf("add: %s", out)
	}
	expect("POST", "/api/annotations", `{"kind":"sick","from":"2026-10-23"}`, http.StatusOK) // one day
	if a := ctl.notes[1]; a.To != "2026-10-23" {
		t.Errorf("one-day annotation %+v", a)
	}
	if out := expect("POST", "/api/annotations", `{"kind":"party","from":"2026-10-20"}`, http.StatusBadRequest); !strings.Contains(out, "kind") {
		t.Errorf("bad kind says %q", out)
	}
	expect("POST", "/api/annotations", `{"kind":"other","from":"2026-10-22","to":"2026-10-20"}`, http.StatusBadRequest)
	expect("POST", "/api/annotations", `{"kind":"other","from":"2026-10-22","note":"`+strings.Repeat("x", 201)+`"}`, http.StatusBadRequest)
	expect("POST", "/api/annotations", `{"kind":"other","from":"2026-10-22","note":"`+strings.Repeat("x", maxBody)+`"}`, http.StatusBadRequest)
	if list := expect("GET", "/api/annotations", "", http.StatusOK); !strings.Contains(list, `"id":1`) || !strings.Contains(list, "pager") {
		t.Errorf("list: %s", list)
	}
	expect("DELETE", "/api/annotations/1", "", http.StatusNoContent)
	expect("DELETE", "/api/annotations/1", "", http.StatusNotFound)
	expect("DELETE", "/api/annotations/x", "", http.StatusBadRequest)
	if len(ctl.notes) != 1 || ctl.notes[0].Kind != annotation.Sick {
		t.Errorf("notes %+v", ctl.notes)
	}

	var questions []Instrument
	if err := json.Unmarshal([]byte(expect("GET", "/api/checkin", "", http.StatusOK)), &questions); err != nil ||
		len(questions) != 2 || len(questions[0].Items) != 6 || len(questions[1].Items[0].Choices) != 5 {
		t.Errorf("questions %v %+v", err, questions)
	}
	var res CheckinResult
	_ = json.Unmarshal([]byte(expect("POST", "/api/checkins", fullCheckin(25), http.StatusOK)), &res)
	if len(res.Scores) != 2 || res.Support || res.Message != "" || len(ctl.checkins) != 2 {
		t.Errorf("low check-in: %+v, %d saved", res, len(ctl.checkins))
	}
	_ = json.Unmarshal([]byte(expect("POST", "/api/checkins", fullCheckin(100), http.StatusOK)), &res)
	if !res.Support || res.Message != checkin.SupportMessage || len(res.Resources) < 2 || res.Resources[0].Name != "Samaritans" {
		t.Errorf("high check-in shows no support: %+v", res)
	}
	partial := strings.Replace(fullCheckin(50), `"p1":50,`, "", 1)
	expect("POST", "/api/checkins", partial, http.StatusBadRequest)
	expect("POST", "/api/checkins", strings.Replace(fullCheckin(50), `"p1":50`, `"p1":51`, 1), http.StatusBadRequest)
	expect("POST", "/api/checkins", `{"answers":{"daily-v1":{"energy":0,"drained":0}}}`, http.StatusBadRequest)
	if len(ctl.checkins) != 4 {
		t.Errorf("a refused check-in was saved: %d", len(ctl.checkins))
	}
}

func TestWriteErrorsDontLeak(t *testing.T) {
	s, _, base := startWith(t, fakeSource{}, &memControl{fail: true})
	w := loginWriter(t, s, base)
	if code, out := w.do("DELETE", "/api/data?range=today", "", nil); code != http.StatusInternalServerError || strings.Contains(out, "secret") {
		t.Errorf("%d %s", code, out)
	}
}

func TestWritesOff(t *testing.T) {
	s, _, base := start(t, fakeSource{})
	w := loginWriter(t, s, base)
	resp := request(t, "GET", base+"/api/session", func(r *http.Request) { r.AddCookie(w.cookie) })
	var sess Session
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil || sess.Writable {
		t.Errorf("session %v %+v", err, sess)
	}
	for _, c := range writeCases {
		if code, _ := w.do(c.method, c.path, c.body, nil); code != http.StatusServiceUnavailable {
			t.Errorf("%s %s without a Control: %d", c.method, c.path, code)
		}
	}
	if code, out := w.do("GET", "/api/annotations", "", nil); code != http.StatusOK || strings.TrimSpace(out) != "[]" {
		t.Errorf("annotations without a Control: %d %s", code, out)
	}
}
