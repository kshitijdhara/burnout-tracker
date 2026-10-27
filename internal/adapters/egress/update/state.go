package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// StateFile is the file in the data folder holding the weekly check's
// last result, so the tray and doctor can show it without a network call.
const StateFile = "update-check.json"

// Weekly-check timing.
const (
	CheckEvery = 7 * 24 * time.Hour // between successful checks
	RetryAfter = 24 * time.Hour     // after a failed one
)

// State is the weekly check's last result.
type State struct {
	Checked time.Time `json:"checked,omitzero"` // last successful check
	Tried   time.Time `json:"tried,omitzero"`   // last attempt
	Latest  string    `json:"latest,omitempty"` // newest stable version then
}

// LoadState reads the state; a missing or unreadable file is the zero
// State (never checked).
func LoadState(dataDir string) State {
	var s State
	b, err := os.ReadFile(filepath.Join(dataDir, StateFile)) //nolint:gosec // our own data folder
	if err != nil || len(b) > 4<<10 {
		return State{}
	}
	if json.Unmarshal(b, &s) != nil {
		return State{}
	}
	return s
}

// SaveState writes the state atomically, owner-only.
func SaveState(dataDir string, s State) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, StateFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Due reports whether the weekly check should run now.
func (s State) Due(now time.Time) bool {
	return now.Sub(s.Checked) >= CheckEvery && now.Sub(s.Tried) >= RetryAfter
}

// Newer returns the version found by the last check if it is newer than
// current. A development build ("dev") is never told to update.
func (s State) Newer(current string) (Version, bool) {
	cur, err := ParseVersion(current)
	if err != nil {
		return Version{}, false
	}
	latest, err := ParseVersion(s.Latest)
	if err != nil || latest.Compare(cur) <= 0 {
		return Version{}, false
	}
	return latest, true
}
