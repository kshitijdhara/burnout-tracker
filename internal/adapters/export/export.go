// Package export writes the user's recorded data out as JSON or CSV
// (privacy & security §5.1 "Export"). The format is a versioned contract
// (system design §3.3): schemas/v1/export.schema.json describes the JSON,
// and the CSV files carry the same fields. Exports are plaintext, so the
// CLI warns before writing one, and each export's path is remembered so
// `forget` can point at the copies it can't delete.
package export

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Version is the export format's version, the "v" in every JSON export.
const Version = 1

// Range selects logical days From to To, inclusive (YYYY-MM-DD).
// Check-ins, which have no logical day, are selected by time: Start up to
// End.
type Range struct {
	From, To   string
	Start, End time.Time
}

// Minute is one minute's attention on one app (or category, for private
// apps and aggregate-only mode).
type Minute struct {
	Minute       string  `json:"minute"` // RFC 3339, local time with offset
	Day          string  `json:"day"`
	App          string  `json:"app,omitempty"` // empty for private apps and aggregate-only mode
	Category     string  `json:"category"`
	Unattributed bool    `json:"unattributed,omitempty"` // present, but no window clearly held attention
	AttendedS    float64 `json:"attended_s"`
	FocusedS     float64 `json:"focused_s"`
	VisibleS     float64 `json:"visible_s"`
}

// Interval is a span on a timeline: a shell command, an agent or human
// state, presence, or a calendar busy block.
type Interval struct {
	Start      string `json:"start"` // RFC 3339, local time with offset
	End        string `json:"end"`
	Day        string `json:"day"`
	Kind       string `json:"kind"`
	Category   string `json:"category,omitempty"`
	Session    string `json:"session,omitempty"` // keyed hash of the agent session
	Command    string `json:"command,omitempty"`
	Subcommand string `json:"subcommand,omitempty"`
	Exit       *int   `json:"exit,omitempty"`
	Group      string `json:"group,omitempty"`
	Project    string `json:"project,omitempty"` // keyed hash of the project folder
	State      string `json:"state,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Agent      string `json:"agent,omitempty"`
	Tools      int    `json:"tools,omitempty"`
}

// Day is one logical day's features and strain score.
type Day struct {
	Day             string             `json:"day"`
	RegistryVersion int                `json:"registry_version,omitempty"`
	Coverage        *float64           `json:"coverage,omitempty"`
	PresentS        *float64           `json:"present_s,omitempty"`
	Features        map[string]float64 `json:"features,omitempty"`
	ModelVersion    string             `json:"model_version,omitempty"`
	Status          string             `json:"status,omitempty"` // ok, missing or calibrating
	Strain          *float64           `json:"strain,omitempty"`
	Drivers         []string           `json:"drivers,omitempty"`    // feature names
	Guardrails      []string           `json:"guardrails,omitempty"` // limits crossed
}

// Checkin is one completed check-in instrument.
type Checkin struct {
	At         string         `json:"at"` // RFC 3339, local time with offset
	Instrument string         `json:"instrument"`
	Score      float64        `json:"score"`
	Answers    map[string]int `json:"answers"` // item id → answer
}

// Annotation marks days as vacation, sick, on call, …
type Annotation struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
	Note string `json:"note,omitempty"`
}

// Boundary is a rule the user set for their work, with the days it
// applied to. Until is the first day it no longer applied, empty while it
// is active.
type Boundary struct {
	Kind   string `json:"kind"`
	Value  string `json:"value,omitempty"`
	Since  string `json:"since"`
	Until  string `json:"until,omitempty"`
	Source string `json:"source"` // cli, dashboard or review
}

// Source reads the rows of r, oldest first, calling fn for each. The
// store implements it over one read transaction, so an export is a
// consistent snapshot.
type Source interface {
	Minutes(ctx context.Context, r Range, fn func(Minute) error) error
	Intervals(ctx context.Context, r Range, fn func(Interval) error) error
	Days(ctx context.Context, r Range, fn func(Day) error) error
	Checkins(ctx context.Context, r Range, fn func(Checkin) error) error
	Annotations(ctx context.Context, r Range, fn func(Annotation) error) error
	// Boundaries are those that applied on a day in r, in the order set.
	Boundaries(ctx context.Context, r Range, fn func(Boundary) error) error
}

// Meta describes an export.
type Meta struct {
	ExportedAt time.Time
	AppVersion string
	Range      Range
}

// Counts are how many rows of each kind were written.
type Counts struct {
	Minutes, Intervals, Days, Checkins, Annotations, Boundaries int
}

// WriteJSON streams one JSON document to w: the metadata, then an array
// per kind of row. Memory stays flat however much is exported.
func WriteJSON(ctx context.Context, w io.Writer, src Source, m Meta) (Counts, error) {
	bw := bufio.NewWriter(w)
	var n Counts
	head, err := json.Marshal(struct {
		V          int    `json:"v"`
		ExportedAt string `json:"exported_at"`
		AppVersion string `json:"app_version"`
		From       string `json:"from"`
		To         string `json:"to"`
	}{Version, m.ExportedAt.Format(time.RFC3339), m.AppVersion, m.Range.From, m.Range.To})
	if err != nil {
		return n, err
	}
	// Reopen the header object to append the arrays.
	if _, err := bw.Write(head[:len(head)-1]); err != nil {
		return n, err
	}
	sections := []struct {
		name  string
		count *int
		each  func(func(any) error) error
	}{
		{"minutes", &n.Minutes, func(f func(any) error) error {
			return src.Minutes(ctx, m.Range, func(r Minute) error { return f(r) })
		}},
		{"intervals", &n.Intervals, func(f func(any) error) error {
			return src.Intervals(ctx, m.Range, func(r Interval) error { return f(r) })
		}},
		{"days", &n.Days, func(f func(any) error) error {
			return src.Days(ctx, m.Range, func(r Day) error { return f(r) })
		}},
		{"checkins", &n.Checkins, func(f func(any) error) error {
			return src.Checkins(ctx, m.Range, func(r Checkin) error { return f(r) })
		}},
		{"annotations", &n.Annotations, func(f func(any) error) error {
			return src.Annotations(ctx, m.Range, func(r Annotation) error { return f(r) })
		}},
		{"boundaries", &n.Boundaries, func(f func(any) error) error {
			return src.Boundaries(ctx, m.Range, func(r Boundary) error { return f(r) })
		}},
	}
	for _, s := range sections {
		if _, err := fmt.Fprintf(bw, ",\n%q:[", s.name); err != nil {
			return n, err
		}
		err := s.each(func(row any) error {
			b, err := json.Marshal(row)
			if err != nil {
				return err
			}
			sep := ",\n"
			if *s.count == 0 {
				sep = "\n"
			}
			*s.count++
			if _, err := bw.WriteString(sep); err != nil {
				return err
			}
			_, err = bw.Write(b)
			return err
		})
		if err != nil {
			return n, fmt.Errorf("export: %s: %w", s.name, err)
		}
		if _, err := bw.WriteString("]"); err != nil {
			return n, err
		}
	}
	if _, err := bw.WriteString("}\n"); err != nil {
		return n, err
	}
	return n, bw.Flush()
}

// CSV file names, one per kind of row.
const (
	MinutesCSV     = "minutes.csv"
	IntervalsCSV   = "intervals.csv"
	DaysCSV        = "days.csv"
	CheckinsCSV    = "checkins.csv"
	AnnotationsCSV = "annotations.csv"
	BoundariesCSV  = "boundaries.csv"
)

// WriteCSV writes one CSV file per kind of row into dir, which must not
// exist yet: it is created owner-only, and removed again if the export
// fails. features names the days.csv feature columns, in order.
func WriteCSV(ctx context.Context, dir string, src Source, m Meta, features []string) (n Counts, err error) {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return n, fmt.Errorf("export: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	opt := func(v *float64) string {
		if v == nil {
			return ""
		}
		return f(*v)
	}
	files := []struct {
		name   string
		header []string
		rows   func(write func([]string) error) error
		count  *int
	}{
		{MinutesCSV, []string{"minute", "day", "app", "category", "unattributed", "attended_s", "focused_s", "visible_s"},
			func(write func([]string) error) error {
				return src.Minutes(ctx, m.Range, func(r Minute) error {
					return write([]string{r.Minute, r.Day, r.App, r.Category, strconv.FormatBool(r.Unattributed), f(r.AttendedS), f(r.FocusedS), f(r.VisibleS)})
				})
			}, &n.Minutes},
		{IntervalsCSV, []string{"start", "end", "day", "kind", "category", "session", "command", "subcommand", "exit", "group", "project", "state", "reason", "agent", "tools"},
			func(write func([]string) error) error {
				return src.Intervals(ctx, m.Range, func(r Interval) error {
					exit := ""
					if r.Exit != nil {
						exit = strconv.Itoa(*r.Exit)
					}
					tools := ""
					if r.Tools != 0 {
						tools = strconv.Itoa(r.Tools)
					}
					return write([]string{r.Start, r.End, r.Day, r.Kind, r.Category, r.Session, r.Command, r.Subcommand, exit, r.Group, r.Project, r.State, r.Reason, r.Agent, tools})
				})
			}, &n.Intervals},
		{DaysCSV, append([]string{"day", "registry_version", "coverage", "present_s", "model_version", "status", "strain", "drivers", "guardrails"}, features...),
			func(write func([]string) error) error {
				return src.Days(ctx, m.Range, func(r Day) error {
					reg := ""
					if r.RegistryVersion != 0 {
						reg = strconv.Itoa(r.RegistryVersion)
					}
					row := []string{r.Day, reg, opt(r.Coverage), opt(r.PresentS), r.ModelVersion, r.Status, opt(r.Strain),
						strings.Join(r.Drivers, ";"), strings.Join(r.Guardrails, ";")}
					for _, name := range features {
						if v, ok := r.Features[name]; ok {
							row = append(row, f(v))
						} else {
							row = append(row, "")
						}
					}
					return write(row)
				})
			}, &n.Days},
		{CheckinsCSV, []string{"at", "instrument", "score", "answers"},
			func(write func([]string) error) error {
				return src.Checkins(ctx, m.Range, func(r Checkin) error {
					answers := make([]string, 0, len(r.Answers))
					for _, id := range slices.Sorted(maps.Keys(r.Answers)) {
						answers = append(answers, id+"="+strconv.Itoa(r.Answers[id]))
					}
					return write([]string{r.At, r.Instrument, f(r.Score), strings.Join(answers, ";")})
				})
			}, &n.Checkins},
		{AnnotationsCSV, []string{"from", "to", "kind", "note"},
			func(write func([]string) error) error {
				return src.Annotations(ctx, m.Range, func(r Annotation) error {
					return write([]string{r.From, r.To, r.Kind, r.Note})
				})
			}, &n.Annotations},
		{BoundariesCSV, []string{"kind", "value", "since", "until", "source"},
			func(write func([]string) error) error {
				return src.Boundaries(ctx, m.Range, func(r Boundary) error {
					return write([]string{r.Kind, r.Value, r.Since, r.Until, r.Source})
				})
			}, &n.Boundaries},
	}
	for _, file := range files {
		if err := writeCSVFile(filepath.Join(dir, file.name), file.header, file.rows, file.count); err != nil {
			return n, fmt.Errorf("export: %s: %w", file.name, err)
		}
	}
	return n, nil
}

func writeCSVFile(path string, header []string, rows func(func([]string) error) error, count *int) error {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // inside the directory just created
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(out)
	w := csv.NewWriter(bw)
	err = w.Write(header)
	if err == nil {
		err = rows(func(rec []string) error {
			for i, field := range rec {
				rec[i] = defuse(field)
			}
			*count++
			return w.Write(rec)
		})
	}
	if err == nil {
		w.Flush()
		err = w.Error()
	}
	if err == nil {
		err = bw.Flush()
	}
	return errors.Join(err, out.Close())
}

// defuse keeps a spreadsheet from reading a text field as a formula (CSV
// injection): fields starting with = + - @ get a leading apostrophe. Plain
// negative numbers are left alone.
func defuse(field string) string {
	if field == "" || !strings.ContainsRune("=+-@\t\r", rune(field[0])) {
		return field
	}
	if _, err := strconv.ParseFloat(field, 64); err == nil {
		return field
	}
	return "'" + field
}
