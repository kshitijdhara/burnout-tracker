package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/export"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/app"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/features"
)

const plaintextWarning = "This export is plaintext: anyone who can read the file can read your activity.\n" +
	"Keep it out of shared and synced folders, and delete it when you're done.\n"

// runExport writes recorded data out as JSON or CSV (privacy & security
// §5.1). It reads a consistent snapshot of the database, read-only, so it
// can run while the daemon does.
func runExport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	format := fs.String("format", "json", "json (one file) or csv (a new folder with one file per kind of data)")
	out := fs.String("out", "", "the file (json) or new folder (csv) to write; - writes JSON to standard output")
	since := fs.String("since", "", "first day to export, YYYY-MM-DD (default: everything)")
	until := fs.String("until", "", "last day to export, YYYY-MM-DD (default: today)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 || *out == "" {
		return errors.New("usage: burnout export --out FILE [--format json|csv] [--since YYYY-MM-DD] [--until YYYY-MM-DD]")
	}
	if *format != "json" && *format != "csv" {
		return fmt.Errorf("--format %q: want json or csv", *format)
	}
	toStdout := *out == "-"
	if toStdout && *format != "json" {
		return errors.New("--out - writes JSON only; give csv a folder")
	}
	if toStdout {
		if fi, err := os.Stdout.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return errors.New("not writing the export to the terminal: redirect it (> file) or use --out FILE")
		}
	}
	now := time.Now()
	r, err := exportRange(*since, *until, now, app.DefaultConfig().Rollover)
	if err != nil {
		return err
	}

	dataDir, err := paths.DataDir()
	if err != nil {
		return err
	}
	store, err := sqlite.OpenReadOnly(ctx, dataDir)
	if errors.Is(err, sqlite.ErrNoDatabase) {
		return errors.New("nothing recorded yet, so there's nothing to export")
	}
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	_, _ = fmt.Fprint(os.Stderr, plaintextWarning)
	meta := export.Meta{ExportedAt: now, AppVersion: version, Range: r}
	var n export.Counts
	path := *out
	err = store.Export(ctx, func(src export.Source) error {
		var err error
		switch {
		case toStdout:
			n, err = export.WriteJSON(ctx, os.Stdout, src, meta)
		case *format == "json":
			n, err = writeJSONFile(ctx, path, src, meta)
		default:
			n, err = export.WriteCSV(ctx, path, src, meta, featureNames())
		}
		return err
	})
	if err != nil {
		return err
	}
	if toStdout {
		return nil
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if err := export.Remember(dataDir, export.Entry{At: now, Path: path, Format: *format}); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "burnout: couldn't note where the export went: %v\n", err)
	}
	fmt.Printf("Exported %d minutes, %d intervals, %d days, %d check-ins and %d annotations (%s to %s)\nto %s\n",
		n.Minutes, n.Intervals, n.Days, n.Checkins, n.Annotations, r.From, r.To, printable(path))
	fmt.Println("`burnout forget` won't delete it: remove it yourself when you're done.")
	return nil
}

// exportRange turns --since and --until into a range of logical days and
// the times they span.
func exportRange(since, until string, now time.Time, rollover time.Duration) (export.Range, error) {
	r := export.Range{From: "0001-01-01", To: now.Add(-rollover).Format(time.DateOnly)}
	if since != "" {
		r.From = since
	}
	if until != "" {
		r.To = until
	}
	from, err := time.ParseInLocation(time.DateOnly, r.From, now.Location())
	if err != nil {
		return r, fmt.Errorf("--since %q: want YYYY-MM-DD", since)
	}
	to, err := time.ParseInLocation(time.DateOnly, r.To, now.Location())
	if err != nil {
		return r, fmt.Errorf("--until %q: want YYYY-MM-DD", until)
	}
	if to.Before(from) {
		return r, errors.New("--until is before --since")
	}
	r.Start, r.End = from.Add(rollover), to.AddDate(0, 0, 1).Add(rollover)
	return r, nil
}

// writeJSONFile writes a JSON export to a new owner-only file, removing it
// again if the export fails.
func writeJSONFile(ctx context.Context, path string, src export.Source, meta export.Meta) (n export.Counts, err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the user's chosen path
	if errors.Is(err, os.ErrExist) {
		return n, fmt.Errorf("%s already exists: choose another name", printable(path))
	}
	if err != nil {
		return n, err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	return export.WriteJSON(ctx, io.Writer(f), src, meta)
}

func featureNames() []string {
	reg := features.Registry()
	names := make([]string, len(reg))
	for i, f := range reg {
		names[i] = f.Name
	}
	return names
}
