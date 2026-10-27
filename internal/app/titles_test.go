package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/classify"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

// recordingHasher hashes for real and remembers every title key it made,
// so a test can look for them where they must never be.
type recordingHasher struct {
	mu     sync.Mutex
	titles []string
}

func (h *recordingHasher) Key(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	k := hex.EncodeToString(sum[:16])
	if len(parts) > 0 && parts[0] == "title" {
		h.mu.Lock()
		h.titles = append(h.titles, k)
		h.mu.Unlock()
	}
	return k
}

func titled(id model.WindowID, app, title string) model.Window {
	return model.Window{ID: id, AppID: app, Title: model.NewSensitive(title), Visible: 1}
}

func TestTitleCascade(t *testing.T) {
	rules := classify.New(
		[]string{"domain:intranet.example.com", "domain:mybank.com", "domain:mail.google.com", "org.mozilla.firefox"},
		map[string]model.Category{"domain:docs.google.com": model.CategoryCode, "domain:paypal.com": model.CategoryCode, "com.google.chrome": model.CategoryOther},
	)
	tr := newTitleRules(rules, &recordingHasher{})
	const chrome = "com.google.Chrome"
	tests := []struct {
		w    model.Window
		want model.Category
	}{
		{titled(1, chrome, "Design doc - Google Docs - Google Chrome"), model.CategoryCode}, // site override; not mail.google.com's
		{titled(2, chrome, "Pull request #42 · a/b · GitHub"), model.CategoryCode},          // bundled site
		{titled(3, chrome, "Summary - PayPal"), model.CategoryPrivate},                      // sensitive beats override
		{titled(9, chrome, "Bank of America | Online Banking | Accounts Overview"), model.CategoryPrivate},
		{titled(10, chrome, "Statements - MyBank - Google Chrome"), model.CategoryPrivate},   // the excluded site's word
		{titled(11, chrome, "Intranet home - Example"), model.CategoryOther},                 // not a subdomain's parent's
		{titled(4, chrome, "Wiki | intranet.example.com"), model.CategoryPrivate},            // domain exclusion
		{titled(5, chrome, "Some page nobody knows"), model.CategoryOther},                   // the app's own category
		{titled(6, chrome, ""), model.CategoryOther},                                         // no title
		{titled(7, "org.mozilla.firefox", "Lofi - YouTube"), model.CategoryPrivate},          // app exclusion first
		{titled(8, "com.apple.Terminal", "ssh paypal.com - Tinder"), model.CategoryTerminal}, // titles only for browsers
	}
	for _, tc := range tests {
		if got := tr.category(tc.w); got != tc.want {
			t.Errorf("window %d: %q, want %q", tc.w.ID, got, tc.want)
		}
	}
}

// titleCollector shows browser pages whose titles carry canaries: one per
// rule the redactor applies, a sensitive site and an unknown one.
type titleCollector struct {
	clock *fakeClock
	n     int
}

var titleCanaries = []string{"canary.person@example.com", "CANARYPROJECT", "4815162342", "ghpCANARYaB3dE5fG7hJ9kL1mN3pQ5r"}

func (f *titleCollector) Snapshot(context.Context) (model.Snapshot, error) {
	at := t0.Add(time.Duration(f.n) * time.Second)
	f.n++
	f.clock.set(at)
	pages := []string{
		"Inbox - canary.person@example.com - Gmail",
		"CANARYPROJECT/main.go · owner/repo · GitHub",
		"Statement 4815162342 - PayPal",
		"Token ghpCANARYaB3dE5fG7hJ9kL1mN3pQ5r - CANARYPROJECT wiki",
	}
	w := titled(1, "com.google.Chrome", pages[f.n/30%len(pages)])
	w.Focused, w.Bounds = true, model.Rect{W: 800, H: 600}
	return model.Snapshot{At: at, Windows: []model.Window{w}, Monitors: []model.Rect{{W: 1920, H: 1080}},
		PointerWindow: 1, Keys: 2, InputCountsOn: true}, nil
}
func (f *titleCollector) Capabilities() ports.Capabilities {
	return ports.Capabilities{Name: "fake", Titles: true}
}
func (f *titleCollector) Close() error { return nil }

// No title, no part of one and no title key reaches the sink or the log;
// only categories do (privacy & security §4.1, ADR-0006 items 5–6).
func TestTitlesNeverReachTheSink(t *testing.T) {
	for _, on := range []bool{true, false} {
		clock := &fakeClock{now: t0}
		sink := &fakeSink{}
		h := &recordingHasher{}
		var logs bytes.Buffer
		d := newTestDaemon(t, clock, sink, &titleCollector{clock: clock}, func(cfg *Config, deps *Deps) {
			cfg.Settings.TitleClassification = on
			deps.Hasher = h
			deps.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		})
		ctx := context.Background()
		for range 240 {
			d.onTick(ctx)
		}
		d.settle(ctx, clock.Now())

		sink.mu.Lock()
		everything, _ := json.Marshal(struct {
			R []model.MinuteRollup
			I []model.Interval
			H string
			T []string
		}{sink.rollups, sink.intervals, sink.health, sink.topology})
		cats := map[model.Category]bool{}
		for _, r := range sink.rollups {
			cats[r.Category] = true
		}
		sink.mu.Unlock()
		out := string(everything) + logs.String()
		for _, c := range append(titleCanaries, "paypal", "PayPal", "github.com", "mail.google.com", "Statement", "Inbox") {
			if strings.Contains(out, c) {
				t.Errorf("titles %t: %q reached the sink or log", on, c)
			}
		}
		h.mu.Lock()
		for _, k := range h.titles {
			if strings.Contains(out, k) {
				t.Errorf("titles %t: a title cache key reached the sink or log", on)
			}
		}
		// Nothing derived from a title is hashed until the ADR-0006 cache
		// exists; the check above guards the keys once it does.
		if len(h.titles) != 0 {
			t.Errorf("titles %t: %d title keys made", on, len(h.titles))
		}
		h.mu.Unlock()
		want := map[model.Category]bool{model.CategoryBrowser: true}
		if on {
			want = map[model.Category]bool{model.CategoryComms: true, model.CategoryCode: true, model.CategoryPrivate: true, model.CategoryBrowser: true}
		}
		for c := range want {
			if !cats[c] {
				t.Errorf("titles %t: no %s minutes in %v", on, c, cats)
			}
		}
		if len(cats) != len(want) {
			t.Errorf("titles %t: categories %v, want %v", on, cats, want)
		}
	}
}
