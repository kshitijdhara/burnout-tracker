package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/store/sqlite"
	"github.com/kshitijdhara/burnout-tracker/internal/app"
	"github.com/kshitijdhara/burnout-tracker/internal/config"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

const (
	// bundleLogLines is how much of the daemon log a bundle carries.
	bundleLogLines = 200
	// bundleLogBytes bounds how far back from the end of the log it reads.
	bundleLogBytes = 256 << 10
	excludedApp    = "[excluded app]"
)

// bundleFile is one file in a diagnostics bundle, already redacted.
type bundleFile struct {
	Name string
	Data []byte
}

// runBundle builds a diagnostics bundle (privacy & security §10), shows
// all of it, and writes it to a new owner-only file once the user types
// yes. Nothing is uploaded: the user decides where it goes.
func runBundle(ctx context.Context, path string) error {
	if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return errors.New("doctor --bundle shows everything it will write and asks first: run it in a terminal")
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists: choose another name", printable(path))
	}
	var report bytes.Buffer
	if err := doctor(ctx, &report, true); err != nil {
		return err
	}
	files, err := collectBundle(ctx, report.Bytes())
	if err != nil {
		return err
	}
	previewBundle(os.Stdout, files)
	fmt.Printf("\nThis is everything in the bundle. It stays on this computer until you send it to someone.\n"+
		"Type \"yes\" to write it to %s: ", printable(path))
	if !confirmed(os.Stdin) {
		fmt.Println("Nothing was written.")
		return nil
	}
	if err := writeBundle(path, files); err != nil {
		return err
	}
	fmt.Printf("Wrote %s.\n", printable(path))
	return nil
}

// collectBundle gathers the bundle's files: the doctor report, the
// config, today's health and the end of the daemon log. Every file goes
// through bundleRedactor.
func collectBundle(ctx context.Context, report []byte) ([]bundleFile, error) {
	dataDir, err := paths.DataDir()
	if err != nil {
		return nil, err
	}
	cfg, _, cfgErr := loadConfig()
	files := []bundleFile{{"doctor.txt", report}}

	if cfgErr != nil {
		files = append(files, bundleFile{"config.toml", []byte("# The config file is invalid, so it isn't included: run `burnout config validate`.\n")})
	} else {
		excluded := cfg.Privacy.ExcludeApps
		cfg.Privacy.ExcludeApps = slices.Repeat([]string{excludedApp}, len(excluded))
		files = append(files, bundleFile{"config.toml", config.Render(cfg)})
		cfg.Privacy.ExcludeApps = excluded
	}

	if store, err := sqlite.OpenReadOnly(ctx, dataDir); err == nil {
		day := model.LogicalDay(time.Now(), app.DefaultConfig().Rollover)
		h, err := store.Health(ctx, day)
		_ = store.Close()
		if err == nil && h != "" {
			files = append(files, bundleFile{"health.json", []byte(h + "\n")})
		}
	}

	logPath := filepath.Join(paths.LogDir(dataDir), paths.DaemonLogFile)
	switch tail, err := tailLines(logPath, bundleLogLines, bundleLogBytes); {
	case err == nil:
		files = append(files, bundleFile{paths.DaemonLogFile, tail})
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("read the daemon log: %w", err)
	}

	redact := bundleRedactor(cfg.Privacy.ExcludeApps)
	for i := range files {
		files[i].Data = redact(files[i].Data)
	}
	return files, nil
}

// bundleRedactor returns a function that shows the home folder as ~ and
// replaces the apps the user excluded, in any case, as their names are
// what they wanted kept private. It runs over whole files, so a name split
// across writes is still caught.
func bundleRedactor(excluded []string) func([]byte) []byte {
	var homes []string
	if home, err := os.UserHomeDir(); err == nil {
		homes = append(homes, home)
		if resolved, err := filepath.EvalSymlinks(home); err == nil && resolved != home {
			homes = append(homes, resolved) // macOS: /var is /private/var
		}
	}
	var apps []string
	for _, a := range excluded {
		if a = strings.TrimSpace(a); a != "" {
			apps = append(apps, regexp.QuoteMeta(a))
		}
	}
	// Longest first, so one name that contains another is replaced whole.
	byLength := func(a, b string) int { return len(b) - len(a) }
	slices.SortFunc(homes, byLength)
	slices.SortFunc(apps, byLength)
	var appRE *regexp.Regexp
	if len(apps) > 0 {
		appRE = regexp.MustCompile("(?i)" + strings.Join(apps, "|"))
	}
	return func(p []byte) []byte {
		if appRE != nil {
			p = appRE.ReplaceAllLiteral(p, []byte(excludedApp))
		}
		for _, h := range homes {
			var b bytes.Buffer
			_, _ = homeRedactor{w: &b, home: h}.Write(p)
			p = b.Bytes()
		}
		return p
	}
}

// tailLines returns the last n lines of the file at path, reading at most
// maxBytes from its end.
func tailLines(path string, n int, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // our own log file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := max(0, fi.Size()-maxBytes)
	b, err := io.ReadAll(io.NewSectionReader(f, start, fi.Size()-start))
	if err != nil {
		return nil, err
	}
	if start > 0 {
		// The first line is cut off; drop it.
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	lines := bytes.SplitAfter(b, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return bytes.Join(lines[max(0, len(lines)-n):], nil), nil
}

// previewBundle shows every file in full, as it will be written.
func previewBundle(w io.Writer, files []bundleFile) {
	for _, f := range files {
		_, _ = fmt.Fprintf(w, "\n===== %s =====\n", f.Name)
		for line := range strings.Lines(string(f.Data)) {
			_, _ = fmt.Fprintln(w, printable(strings.TrimRight(line, "\r\n")))
		}
	}
}

// confirmed reports whether the user typed yes.
func confirmed(in io.Reader) bool {
	line, err := bufio.NewReader(in).ReadString('\n')
	return err == nil && strings.TrimSpace(line) == "yes"
}

// writeBundle writes files as a zip to a new owner-only file, leaving
// nothing behind if it fails. Entries are stored uncompressed: they are
// small, and the bundle stays readable to anyone checking what's in it.
func writeBundle(path string, files []bundleFile) (err error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	now := time.Now()
	for _, f := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Store, Modified: now})
		if err != nil {
			return err
		}
		if _, err := w.Write(f.Data); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}

	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the user's chosen path
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s already exists: choose another name", printable(path))
	}
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	_, err = out.Write(buf.Bytes())
	return err
}
