// Package crashloop decides when the background service starts in safe
// mode (system design §11.2 "Crash loop"): after Limit starts within
// Window, the daemon runs without the native desktop collector, the part
// most likely to crash it, and records hook data only. Safe mode stays on
// across restarts until the daemon has run for Clean without crashing.
//
// Start times live in a small owner-only file in the data folder. The
// decision itself (History.Start) is pure; the file functions are thin.
package crashloop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/kshitijdhara/burntout/internal/adapters/integrations/fileedit"
)

// FileName is the start history inside the data folder.
const FileName = "starts"

// Tunables.
const (
	Limit  = 3                // starts within Window that turn safe mode on
	Window = 10 * time.Minute // how far back starts count
	Clean  = time.Hour        // running this long without a crash ends safe mode
)

// History is the recent starts and whether safe mode is on.
type History struct {
	Starts []time.Time
	Safe   bool
}

// Start records a start at now and returns the new history. Starts older
// than Window, or after now (the clock moved back), no longer count. Safe
// mode turns on at Limit starts and stays on until Cleared.
func (h History) Start(now time.Time) History {
	starts := make([]time.Time, 0, len(h.Starts)+1)
	for _, s := range h.Starts {
		if age := now.Sub(s); age >= 0 && age < Window {
			starts = append(starts, s)
		}
	}
	starts = append(starts, now)
	return History{Starts: starts, Safe: h.Safe || len(starts) >= Limit}
}

// Drop removes the start at t, for a run that ended cleanly: logging out
// or restarting the service is not a crash. Safe mode is unchanged.
func (h History) Drop(t time.Time) History {
	return History{
		Starts: slices.DeleteFunc(slices.Clone(h.Starts), func(s time.Time) bool { return s.Equal(t) }),
		Safe:   h.Safe,
	}
}

type fileJSON struct {
	StartsMs []int64 `json:"starts_ms"`
	Safe     bool    `json:"safe,omitempty"`
}

// Load reads the history. A missing or unreadable file is an empty
// history: the worst case is one more normal start.
func Load(dataDir string) History {
	b, err := os.ReadFile(filepath.Join(dataDir, FileName)) //nolint:gosec // our own private data folder
	if err != nil {
		return History{}
	}
	var f fileJSON
	if json.Unmarshal(b, &f) != nil {
		return History{}
	}
	h := History{Safe: f.Safe}
	for _, ms := range f.StartsMs {
		h.Starts = append(h.Starts, time.UnixMilli(ms))
	}
	return h
}

// Save writes the history, owner-only.
func Save(dataDir string, h History) error {
	f := fileJSON{StartsMs: make([]int64, len(h.Starts)), Safe: h.Safe}
	for i, s := range h.Starts {
		f.StartsMs[i] = s.UnixMilli()
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := fileedit.WriteAtomic(filepath.Join(dataDir, FileName), b, 0o600); err != nil {
		return fmt.Errorf("crashloop: %w", err)
	}
	return nil
}

// Record records a start at now and reports whether it is in safe mode.
// The time is stored to the millisecond; pass the returned time to Forget.
func Record(dataDir string, now time.Time) (History, time.Time, error) {
	now = time.UnixMilli(now.UnixMilli())
	h := Load(dataDir).Start(now)
	return h, now, Save(dataDir, h)
}

// Forget drops the start at t (from Record) after a clean stop.
func Forget(dataDir string, t time.Time) error {
	return Save(dataDir, Load(dataDir).Drop(t))
}

// Clear ends safe mode and forgets every start, after Clean of running.
func Clear(dataDir string) error {
	err := os.Remove(filepath.Join(dataDir, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
