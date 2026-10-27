package export

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeSource struct {
	minutes     []Minute
	intervals   []Interval
	days        []Day
	checkins    []Checkin
	annotations []Annotation
	boundaries  []Boundary
	failAfter   int // fail on this many minutes, if set
}

func each[T any](rows []T, fn func(T) error) error {
	for _, r := range rows {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeSource) Minutes(_ context.Context, _ Range, fn func(Minute) error) error {
	for i, m := range f.minutes {
		if f.failAfter > 0 && i == f.failAfter {
			return errors.New("disk on fire")
		}
		if err := fn(m); err != nil {
			return err
		}
	}
	return nil
}
func (f *fakeSource) Intervals(_ context.Context, _ Range, fn func(Interval) error) error {
	return each(f.intervals, fn)
}
func (f *fakeSource) Days(_ context.Context, _ Range, fn func(Day) error) error {
	return each(f.days, fn)
}
func (f *fakeSource) Checkins(_ context.Context, _ Range, fn func(Checkin) error) error {
	return each(f.checkins, fn)
}
func (f *fakeSource) Annotations(_ context.Context, _ Range, fn func(Annotation) error) error {
	return each(f.annotations, fn)
}

func (f *fakeSource) Boundaries(_ context.Context, _ Range, fn func(Boundary) error) error {
	return each(f.boundaries, fn)
}

func sample() *fakeSource {
	exit, strain, cov := 1, 42.5, 0.9
	return &fakeSource{
		minutes: []Minute{
			{Minute: "2026-10-21T10:00:00+02:00", Day: "2026-10-21", App: "com.apple.Terminal", Category: "terminal", AttendedS: 50, FocusedS: 60, VisibleS: 60},
			{Minute: "2026-10-21T10:01:00+02:00", Day: "2026-10-21", Category: "uncategorized", Unattributed: true, AttendedS: 10},
		},
		intervals: []Interval{{Start: "2026-10-21T10:00:00+02:00", End: "2026-10-21T10:00:03+02:00", Day: "2026-10-21",
			Kind: "shell_cmd", Command: "git", Subcommand: "push", Exit: &exit, Group: "vcs", Project: "abc"}},
		days: []Day{{Day: "2026-10-20", RegistryVersion: 1, Coverage: &cov, Features: map[string]float64{"attended_hours": 6.5, "breaks": 3},
			ModelVersion: "strain-v1", Status: "ok", Strain: &strain, Drivers: []string{"breaks"}}},
		checkins:    []Checkin{{At: "2026-10-21T20:00:00+02:00", Instrument: "cbi_work", Score: 50, Answers: map[string]int{"w2": 50, "w1": 75}}},
		annotations: []Annotation{{From: "2026-11-02", To: "2026-11-06", Kind: "vacation", Note: "=HYPERLINK(\"x\")"}},
		boundaries: []Boundary{
			{Kind: "shutdown", Value: "21:30", Since: "2026-10-02", Until: "2026-10-20", Source: "cli"},
			{Kind: "no_weekend", Since: "2026-10-21", Source: "review"},
		},
	}
}

var meta = Meta{ExportedAt: time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC), AppVersion: "test",
	Range: Range{From: "2026-10-01", To: "2026-10-25"}}

func TestWriteJSON(t *testing.T) {
	var b bytes.Buffer
	n, err := WriteJSON(context.Background(), &b, sample(), meta)
	if err != nil {
		t.Fatal(err)
	}
	if n != (Counts{Minutes: 2, Intervals: 1, Days: 1, Checkins: 1, Annotations: 1, Boundaries: 2}) {
		t.Errorf("counts %+v", n)
	}
	var doc struct {
		V           int          `json:"v"`
		From        string       `json:"from"`
		Minutes     []Minute     `json:"minutes"`
		Intervals   []Interval   `json:"intervals"`
		Days        []Day        `json:"days"`
		Checkins    []Checkin    `json:"checkins"`
		Annotations []Annotation `json:"annotations"`
		Boundaries  []Boundary   `json:"boundaries"`
	}
	if err := json.Unmarshal(b.Bytes(), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b.String())
	}
	want := sample()
	if doc.V != Version || doc.From != "2026-10-01" || !reflect.DeepEqual(doc.Minutes, want.minutes) ||
		!reflect.DeepEqual(doc.Intervals, want.intervals) || !reflect.DeepEqual(doc.Days, want.days) ||
		!reflect.DeepEqual(doc.Checkins, want.checkins) || !reflect.DeepEqual(doc.Annotations, want.annotations) ||
		!reflect.DeepEqual(doc.Boundaries, want.boundaries) {
		t.Errorf("round trip:\n%s", b.String())
	}

	// Nothing recorded: every array is still there, empty.
	b.Reset()
	if _, err := WriteJSON(context.Background(), &b, &fakeSource{}, meta); err != nil {
		t.Fatal(err)
	}
	var empty map[string]any
	if err := json.Unmarshal(b.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"minutes", "intervals", "days", "checkins", "annotations", "boundaries"} {
		if arr, ok := empty[k].([]any); !ok || len(arr) != 0 {
			t.Errorf("%s = %v", k, empty[k])
		}
	}
}

func TestWriteCSV(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "export")
	n, err := WriteCSV(context.Background(), dir, sample(), meta, []string{"attended_hours", "breaks", "meeting_hours"})
	if err != nil {
		t.Fatal(err)
	}
	if n.Minutes != 2 || n.Days != 1 {
		t.Errorf("counts %+v", n)
	}
	read := func(name string) [][]string {
		t.Helper()
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		if fi, _ := f.Stat(); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", name, fi.Mode().Perm())
		}
		recs, err := csv.NewReader(f).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		return recs
	}
	days := read(DaysCSV)
	if got := strings.Join(days[0], ","); got != "day,registry_version,coverage,present_s,model_version,status,strain,drivers,guardrails,attended_hours,breaks,meeting_hours" {
		t.Errorf("days header %s", got)
	}
	if got := strings.Join(days[1], ","); got != "2026-10-20,1,0.9,,strain-v1,ok,42.5,breaks,,6.5,3," {
		t.Errorf("days row %s", got)
	}
	if got := read(CheckinsCSV)[1][3]; got != "w1=75;w2=50" {
		t.Errorf("answers %q", got)
	}
	if got := read(IntervalsCSV)[1]; got[6] != "git" || got[8] != "1" || got[14] != "" {
		t.Errorf("interval %q", got)
	}
	// A note that a spreadsheet would run as a formula is defused.
	if got := read(AnnotationsCSV)[1][3]; got != `'=HYPERLINK("x")` {
		t.Errorf("note %q", got)
	}
	if got := read(BoundariesCSV); len(got) != 3 || strings.Join(got[0], ",") != "kind,value,since,until,source" ||
		strings.Join(got[2], ",") != "no_weekend,,2026-10-21,,review" {
		t.Errorf("boundaries %q", got)
	}
	if got := read(MinutesCSV); len(got) != 3 || got[2][4] != "true" {
		t.Errorf("minutes %q", got)
	}
	if fi, _ := os.Stat(dir); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v", fi.Mode().Perm())
	}

	// The folder must be new, and a failed export leaves nothing behind.
	if _, err := WriteCSV(context.Background(), dir, sample(), meta, nil); err == nil {
		t.Error("wrote into an existing folder")
	}
	broken := sample()
	broken.failAfter = 1
	failed := filepath.Join(t.TempDir(), "failed")
	if _, err := WriteCSV(context.Background(), failed, broken, meta, nil); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Errorf("partial export left behind: %v", err)
	}
}

func TestDefuse(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "git": "git", "-3": "-3", "-1.5": "-1.5", "=1+1": "'=1+1", "+cmd": "'+cmd",
		"-rf": "'-rf", "@SUM(A1)": "'@SUM(A1)", "\tx": "'\tx",
	} {
		if got := defuse(in); got != want {
			t.Errorf("defuse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLog(t *testing.T) {
	dataDir := t.TempDir()
	if got, err := Existing(dataDir); err != nil || len(got) != 0 {
		t.Fatalf("empty log: %v %v", got, err)
	}
	kept := filepath.Join(t.TempDir(), "kept.json")
	gone := filepath.Join(t.TempDir(), "gone.json")
	for _, p := range []string{kept, gone} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Remember(dataDir, Entry{At: meta.ExportedAt, Path: p, Format: "json"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	got, err := Existing(dataDir)
	if err != nil || len(got) != 1 || got[0].Path != kept {
		t.Fatalf("existing: %+v %v", got, err)
	}
	if all, _ := Recorded(dataDir); len(all) != 1 {
		t.Errorf("deleted exports stay in the log: %+v", all)
	}
	for range maxLogged + 5 {
		if err := Remember(dataDir, Entry{At: meta.ExportedAt, Path: kept, Format: "json"}); err != nil {
			t.Fatal(err)
		}
	}
	if all, _ := Recorded(dataDir); len(all) != maxLogged {
		t.Errorf("log holds %d entries", len(all))
	}
}

// TestSchemaMatchesTypes keeps schemas/v1/export.schema.json in step with
// the exported fields, so the published contract never drifts.
func TestSchemaMatchesTypes(t *testing.T) {
	raw, err := os.ReadFile("../../../schemas/v1/export.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	for def, typ := range map[string]reflect.Type{
		"minute": reflect.TypeFor[Minute](), "interval": reflect.TypeFor[Interval](), "dayRecord": reflect.TypeFor[Day](),
		"checkin": reflect.TypeFor[Checkin](), "annotation": reflect.TypeFor[Annotation](), "boundary": reflect.TypeFor[Boundary](),
	} {
		var fields, required []string
		for f := range typ.Fields() {
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			fields = append(fields, name)
			if opts != "omitempty" {
				required = append(required, name)
			}
		}
		var props []string
		for p := range schema.Defs[def].Properties {
			props = append(props, p)
		}
		slices.Sort(fields)
		slices.Sort(props)
		slices.Sort(required)
		req := slices.Clone(schema.Defs[def].Required)
		slices.Sort(req)
		if !slices.Equal(fields, props) {
			t.Errorf("%s: schema properties %v, type fields %v", def, props, fields)
		}
		if !slices.Equal(required, req) {
			t.Errorf("%s: schema requires %v, type always writes %v", def, req, required)
		}
	}
}
