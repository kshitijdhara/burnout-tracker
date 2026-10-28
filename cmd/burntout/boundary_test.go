package main

import (
	"bufio"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burntout/internal/config"
	"github.com/kshitijdhara/burntout/internal/domain/annotation"
	"github.com/kshitijdhara/burntout/internal/domain/boundary"
	"github.com/kshitijdhara/burntout/internal/domain/model"
	"github.com/kshitijdhara/burntout/internal/domain/review"
)

func tempStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestBoundaryCommand(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	now := time.Date(2026, 10, 27, 20, 0, 0, 0, time.Local)
	run := func(args ...string) (string, error) {
		var out strings.Builder
		err := boundaryCommand(ctx, st, &out, args, boundary.SourceCLI, now)
		return out.String(), err
	}
	expect := func(want string, args ...string) {
		t.Helper()
		out, err := run(args...)
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("%v: %q %v, want %q", args, out, err, want)
		}
	}
	expect("No boundaries", "list")
	expect("Set: stop work by 21:30 on workdays, from tomorrow", "add", "shutdown", "21:30")
	expect("Already set", "add", "shutdown", "21:30")
	expect("Changed: stop work by 20:00 on workdays (was stop work by 21:30 on workdays)", "add", "shutdown", "20:00")
	expect("Set: no work at the weekend", "add", "no-weekend")
	expect("at most 3.5 hours of meetings", "set", "meetings-max", "3.5h")
	expect("stop work by 20:00 on workdays", "list")
	if out, _ := run("list"); strings.Contains(out, "21:30") {
		t.Errorf("an ended rule is listed: %s", out)
	}
	expect("stop work by 21:30 on workdays, from 2026-10-28 to 2026-10-27", "list", "--history")
	expect("Removed", "remove", "no-weekend")
	expect("Removed", "remove", "4") // meetings-max, by id
	for _, args := range [][]string{
		{"add", "shutdown", "late"}, {"add", "bedtime", "22:00"}, {"add"}, {"remove", "no-weekend"},
		{"remove", "99"}, {"list", "--all"}, {"frobnicate"},
		{"add", "meetings-max", "nan"}, {"add", "meetings-max", "Inf"}, {"add", "shutdown", "9:30"},
	} {
		if _, err := run(args...); err == nil || !isInputError(err) {
			t.Errorf("%v: %v, want an input error", args, err)
		}
	}
	if bs, _ := st.Boundaries(ctx); len(boundary.Current(bs)) != 1 || boundary.Current(bs)[0].Kind != boundary.Shutdown {
		t.Errorf("a refused rule was stored: %+v", boundary.Current(bs))
	}
	if out, err := run("add", "focus-block", "5"); err == nil || strings.Contains(out, "Set") || !strings.Contains(err.Error(), "between 15 and 240") {
		t.Errorf("bad focus block: %q %v", out, err)
	}
}

func TestSplitNote(t *testing.T) {
	got := splitNote("vacation 2026-10-20  2026-10-21 --note  family trip, mostly offline ")
	want := []string{"vacation", "2026-10-20", "2026-10-21", "--note", "family trip, mostly offline"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%q", got)
	}
	if got := splitNote("on-call yesterday"); len(got) != 2 {
		t.Errorf("%q", got)
	}
}

func TestAskReview(t *testing.T) {
	ctx := context.Background()
	st := tempStore(t)
	now := time.Date(2026, 10, 26, 20, 0, 0, 0, time.Local)
	offer, _ := boundary.Parse("shutdown", "19:00")
	w := review.Week{From: "2026-10-19", To: "2026-10-25", Offers: []review.Offer{{Boundary: offer, For: "tip"}}}
	labels := map[string]model.Category{}
	acts := reviewActions{
		store: st,
		label: func(app string, c model.Category) error { labels[app] = c; return nil },
		apps:  []sqlite.UncategorizedApp{{App: "com.new.tool", Seconds: 7200}, {App: "com.other", Seconds: 600}, {App: "com.third", Seconds: 60}, {App: "com.fourth", Seconds: 60}},
		today: "2026-10-26",
		now:   now,
	}
	input := strings.Join([]string{
		"20:30",           // the offer, at another time
		"meetings-max 13", // refused, asked again
		"meetings-max 4",  // set
		"remove shutdown", // and taken back
		"",                // on to marks
		"party yesterday", // refused
		"vacation 2026-10-20 2026-10-21 --note family trip",
		"",
		"code",    // com.new.tool
		"private", // refused for com.other
		"docs",
		"", // com.third skipped; com.fourth is past the limit
	}, "\n") + "\n"
	var out strings.Builder
	if err := askReview(ctx, bufio.NewReader(strings.NewReader(input)), &out, w, acts); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"Set a boundary: stop work by 19:00 on workdays?", "Set: stop work by 20:30 on workdays", "between 0 and 12",
		"Saved: new time in com.new.tool counts as code (undo: `burntout config remove classify.overrides com.new.tool=code`)",
		"Set: at most 4 hours of meetings", "Removed", "kind \"party\"", "Marked: vacation, 2026-10-20 to 2026-10-21 (2 days): family trip",
		"com.new.tool (2h00m)", "Pick one of: code, terminal", "Saved: new time in com.other counts as docs",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "com.fourth") {
		t.Errorf("asked about more than %d apps:\n%s", maxLabels, got)
	}
	bs, _ := st.Boundaries(ctx)
	if len(bs) != 2 || bs[0].Value != "20:30" || bs[0].Active() || !bs[1].Active() || bs[1].Kind != boundary.MeetingsMax {
		t.Errorf("boundaries: %+v", bs)
	}
	for _, b := range bs {
		if b.Source != boundary.SourceReview {
			t.Errorf("not marked as set in a review: %+v", b)
		}
	}
	notes, _ := st.Annotations(ctx, "2026-10-01", "2026-10-31")
	if len(notes) != 1 || notes[0].Kind != annotation.Vacation || notes[0].Note != "family trip" {
		t.Errorf("notes: %+v", notes)
	}
	if len(labels) != 2 || labels["com.new.tool"] != model.CategoryCode || labels["com.other"] != model.CategoryDocs {
		t.Errorf("labels: %v", labels)
	}

	// q stops at once, and so does the end of input; what was answered stays.
	for _, in := range []string{"q\n", "y\n"} {
		out.Reset()
		if err := askReview(ctx, bufio.NewReader(strings.NewReader(in)), &out, w, acts); err != nil || !strings.Contains(out.String(), "Stopped") {
			t.Errorf("%q: %v\n%s", in, err, out.String())
		}
	}
	if bs, _ := st.Boundaries(ctx); len(bs) != 3 || bs[2].Value != "19:00" {
		t.Errorf("the accepted offer before the end of input: %+v", bs)
	}
}

// failingStore fails every write, like a locked or missing database.
type failingStore struct{ *sqlite.Store }

var errLocked = errors.New("sqlite: database is locked")

func (failingStore) SetBoundary(context.Context, boundary.Boundary, string, time.Time) (int64, bool, error) {
	return 0, false, errLocked
}

func (failingStore) RemoveBoundary(context.Context, int64, string, time.Time) (bool, error) {
	return false, errLocked
}

// A store error in the boundary step ends the review with the error; a
// typo is said and asked again.
func TestAskReviewStoreErrors(t *testing.T) {
	ctx := context.Background()
	acts := reviewActions{store: failingStore{tempStore(t)}, label: func(string, model.Category) error { return nil },
		today: "2026-10-26", now: time.Date(2026, 10, 26, 20, 0, 0, 0, time.Local)}
	w := review.Week{From: "2026-10-19", To: "2026-10-25"}
	var out strings.Builder
	in := "remove shutdwn\nadd a b c\nmeetings-max nan\nshutdown 21:30\n\n\n"
	err := askReview(ctx, bufio.NewReader(strings.NewReader(in)), &out, w, acts)
	if !errors.Is(err, errLocked) {
		t.Fatalf("got %v, want the store error\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{"no shutdwn boundary", "usage: burntout boundary", "between 0 and 12"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Mark this week") {
		t.Errorf("the review carried on after the store failed:\n%s", got)
	}
}

func TestLabelApp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := labelApp(path, "com.new.tool", model.CategoryDocs); err != nil {
		t.Fatal(err)
	}
	if err := labelApp(path, "com.other", model.CategoryCode); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	o := cfg.Overrides()
	if len(o) != 2 || o["com.new.tool"] != model.CategoryDocs {
		t.Errorf("overrides: %v", o)
	}
	apps := unlabelled([]sqlite.UncategorizedApp{{App: "com.new.tool"}, {App: "com.third"}, {App: "a=b"}}, cfg)
	if len(apps) != 1 || apps[0].App != "com.third" {
		t.Errorf("unlabelled: %+v", apps)
	}

	// App ids keep their case; override keys are lower-cased when the
	// config is loaded. A labelled app isn't asked about again.
	if err := labelApp(path, "com.microsoft.VSCode", model.CategoryCode); err != nil {
		t.Fatal(err)
	}
	if cfg, err = config.Load(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Overrides()["com.microsoft.VSCode"]; ok {
		t.Fatal("override keys aren't lower-cased, so this test checks nothing")
	}
	apps = unlabelled([]sqlite.UncategorizedApp{{App: "com.microsoft.VSCode"}, {App: "Gnome-terminal"}}, cfg)
	if len(apps) != 1 || apps[0].App != "Gnome-terminal" {
		t.Errorf("unlabelled with mixed case: %+v", apps)
	}
}
