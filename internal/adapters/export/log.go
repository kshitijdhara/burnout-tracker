package export

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// logFile, in the data folder, lists where exports were written. Only
// paths and times: the app can't delete an export the user saved
// elsewhere, but forget and the privacy view can point at it
// (privacy & security §7.2).
const logFile = "exports.log"

// maxLogged bounds the log; the oldest entries are dropped.
const maxLogged = 200

// Entry is one past export.
type Entry struct {
	At     time.Time `json:"at"`
	Path   string    `json:"path"`
	Format string    `json:"format"`
}

// Remember records an export written to path. The log is rewritten whole
// (it is small), owner-only.
func Remember(dataDir string, e Entry) error {
	entries, err := Recorded(dataDir)
	if err != nil {
		return err
	}
	entries = append(entries, e)
	if len(entries) > maxLogged {
		entries = entries[len(entries)-maxLogged:]
	}
	return writeLog(dataDir, entries)
}

// Recorded returns the logged exports, oldest first. Damaged lines are
// skipped.
func Recorded(dataDir string) ([]Entry, error) {
	f, err := os.Open(filepath.Join(dataDir, logFile)) //nolint:gosec // our own data folder
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	defer func() { _ = f.Close() }()
	var out []Entry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Path != "" {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// Existing returns the logged exports still on disk, and forgets the
// ones that are gone.
func Existing(dataDir string) ([]Entry, error) {
	entries, err := Recorded(dataDir)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	var keep []Entry
	for _, e := range entries {
		if _, err := os.Lstat(e.Path); err == nil {
			keep = append(keep, e)
		}
	}
	if len(keep) != len(entries) {
		if err := writeLog(dataDir, keep); err != nil {
			return keep, err
		}
	}
	return keep, nil
}

func writeLog(dataDir string, entries []Entry) error {
	path := filepath.Join(dataDir, logFile)
	if len(entries) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("export: %w", err)
		}
		return nil
	}
	tmp, err := os.CreateTemp(dataDir, ".exports-*")
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	w := bufio.NewWriter(tmp)
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			_ = tmp.Close()
			return err
		}
		_, _ = w.Write(append(b, '\n'))
	}
	if err := errors.Join(w.Flush(), tmp.Close()); err != nil {
		return fmt.Errorf("export: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("export: %w", err)
	}
	return nil
}
