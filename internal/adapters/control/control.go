// Package control carries requests from the CLI to a running daemon
// through small files in the owner-only data folder: pausing collection
// (privacy & security §5.1 "Pause"), forgetting recent data ("Delete
// recent", "Forget"), when the monthly "what's collected" reminder was
// last seen (§5.2 AC-4), the day's nudge (system design §8.3), and, the
// other way, whether the running daemon reads window titles. Files, not the hook socket, so the socket stays
// hooks-only and its wire contract doesn't change.
package control

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/fileedit"
)

// File names in the data folder.
const (
	pauseFile      = "pause"
	forgetFile     = "forget-request"
	forgetDoneFile = "forget-done"
	reminderFile   = "reminder-seen"
	nudgesFile     = "nudges"
	titlesFile     = "daemon-titles"
)

// Pause is a pause of collection. A zero Until means until resumed.
type Pause struct {
	Until time.Time
}

type pauseJSON struct {
	UntilMs int64 `json:"until_ms"`
}

// SetPause pauses collection until until (zero: until resumed).
func SetPause(dataDir string, until time.Time) error {
	p := pauseJSON{}
	if !until.IsZero() {
		p.UntilMs = until.UnixMilli()
	}
	return writeJSON(filepath.Join(dataDir, pauseFile), p)
}

// Resume ends a pause. Resuming when not paused is not an error.
func Resume(dataDir string) error {
	err := os.Remove(filepath.Join(dataDir, pauseFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// PauseState reports whether collection is paused at now, and the pause.
// An expired pause reads as not paused (and is left for Resume or the next
// pause to replace). An unreadable pause file counts as paused: failing
// towards collecting less is the safe direction.
func PauseState(dataDir string, now time.Time) (bool, Pause) {
	b, err := os.ReadFile(filepath.Join(dataDir, pauseFile)) //nolint:gosec // our own data folder
	if errors.Is(err, fs.ErrNotExist) {
		return false, Pause{}
	}
	var p pauseJSON
	if err != nil || json.Unmarshal(b, &p) != nil {
		return true, Pause{}
	}
	if p.UntilMs == 0 {
		return true, Pause{}
	}
	until := time.UnixMilli(p.UntilMs)
	return now.Before(until), Pause{Until: until}
}

// ForgetRequest asks the daemon to delete everything recorded at or after
// Since.
type ForgetRequest struct {
	SinceMs int64  `json:"since_ms"`
	Nonce   string `json:"nonce"`
}

// Since returns the request's start time.
func (r ForgetRequest) Since() time.Time { return time.UnixMilli(r.SinceMs) }

// RequestForget writes a request for the daemon and returns it.
func RequestForget(dataDir string, since time.Time) (ForgetRequest, error) {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return ForgetRequest{}, err
	}
	r := ForgetRequest{SinceMs: since.UnixMilli(), Nonce: hex.EncodeToString(nonce)}
	_ = os.Remove(filepath.Join(dataDir, forgetDoneFile))
	return r, writeJSON(filepath.Join(dataDir, forgetFile), r)
}

// TakeForget returns a pending request and removes it, so it runs once.
func TakeForget(dataDir string) (ForgetRequest, bool) {
	path := filepath.Join(dataDir, forgetFile)
	b, err := os.ReadFile(path) //nolint:gosec // our own data folder
	if err != nil {
		return ForgetRequest{}, false
	}
	_ = os.Remove(path)
	var r ForgetRequest
	if json.Unmarshal(b, &r) != nil || r.Nonce == "" || r.SinceMs < 0 {
		return ForgetRequest{}, false
	}
	return r, true
}

type reminderJSON struct {
	SeenMs int64 `json:"seen_ms"`
}

// SetReminderSeen records that the user opened the "what's collected"
// reminder at at.
func SetReminderSeen(dataDir string, at time.Time) error {
	return writeJSON(filepath.Join(dataDir, reminderFile), reminderJSON{SeenMs: at.UnixMilli()})
}

// ReminderSeen returns when the reminder was last seen, if ever. An
// unreadable file reads as never seen, so the reminder shows again: the
// safe direction.
func ReminderSeen(dataDir string) (time.Time, bool) {
	b, err := os.ReadFile(filepath.Join(dataDir, reminderFile)) //nolint:gosec // our own data folder
	if err != nil {
		return time.Time{}, false
	}
	var r reminderJSON
	if json.Unmarshal(b, &r) != nil || r.SeenMs <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(r.SeenMs), true
}

// Nudges is the tray's nudge state: the day's nudge and whether it was
// snoozed.
type Nudges struct {
	Day     string `json:"day,omitempty"` // logical day Kind first showed on
	Kind    string `json:"kind,omitempty"`
	Snoozed bool   `json:"snoozed,omitempty"` // until the next day
	// Off are kinds the tray of v0.1.0-alpha.1 turned off for good. The
	// tray now turns a kind off in the config file, where `burnout config`
	// turns it back on; NudgeOn clears a kind from here too.
	Off []string `json:"off,omitempty"`
}

// ReadNudges returns the nudge state; none yet is the zero state. An
// unreadable file is an error, and the caller shows no nudge: unlike the
// reminder, the safe direction is silence, so a kind turned off stays off.
func ReadNudges(dataDir string) (Nudges, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, nudgesFile)) //nolint:gosec // our own data folder
	if errors.Is(err, fs.ErrNotExist) {
		return Nudges{}, nil
	}
	if err != nil {
		return Nudges{}, fmt.Errorf("control: %w", err)
	}
	var n Nudges
	if err := json.Unmarshal(b, &n); err != nil {
		return Nudges{}, fmt.Errorf("control: nudges: %w", err)
	}
	return n, nil
}

// ShowNudge records that kind is day's nudge, so no other shows that day.
func ShowNudge(dataDir, day, kind string) error {
	n, err := ReadNudges(dataDir)
	if err != nil {
		return err
	}
	n.Day, n.Kind, n.Snoozed = day, kind, false
	return writeJSON(filepath.Join(dataDir, nudgesFile), n)
}

// SnoozeNudge hides day's nudge until the next day.
func SnoozeNudge(dataDir, day, kind string) error {
	n, _ := ReadNudges(dataDir) // an unreadable file is replaced
	n.Day, n.Kind, n.Snoozed = day, kind, true
	return writeJSON(filepath.Join(dataDir, nudgesFile), n)
}

// NudgeOn clears kind from Off, when the config file turns it on.
func NudgeOn(dataDir, kind string) error {
	n, err := ReadNudges(dataDir)
	if err != nil || !slices.Contains(n.Off, kind) {
		return err
	}
	n.Off = slices.DeleteFunc(n.Off, func(k string) bool { return k == kind })
	return writeJSON(filepath.Join(dataDir, nudgesFile), n)
}

type titlesJSON struct {
	On bool `json:"on"`
}

// SetDaemonTitles records whether the daemon's desktop collector reads
// window titles. It decides that when it starts, so after a change to the
// config file, privacy and doctor tell the two apart.
func SetDaemonTitles(dataDir string, on bool) error {
	return writeJSON(filepath.Join(dataDir, titlesFile), titlesJSON{On: on})
}

// DaemonTitles reports whether the last daemon to start reads window
// titles. Only meaningful while a daemon runs; unreadable reads as no.
func DaemonTitles(dataDir string) bool {
	b, err := os.ReadFile(filepath.Join(dataDir, titlesFile)) //nolint:gosec // our own data folder
	if err != nil {
		return false
	}
	var t titlesJSON
	return json.Unmarshal(b, &t) == nil && t.On
}

type doneJSON struct {
	Nonce string `json:"nonce"`
	Error string `json:"error,omitempty"`
}

// ForgetDone records that the daemon finished a request.
func ForgetDone(dataDir string, r ForgetRequest, ferr error) error {
	d := doneJSON{Nonce: r.Nonce}
	if ferr != nil {
		d.Error = ferr.Error()
	}
	return writeJSON(filepath.Join(dataDir, forgetDoneFile), d)
}

// ErrNoAnswer means the daemon didn't finish the request in time.
var ErrNoAnswer = errors.New("control: the daemon didn't answer")

// WaitForgetDone waits for the daemon to finish r, polling every poll.
func WaitForgetDone(dataDir string, r ForgetRequest, timeout, poll time.Duration) error {
	path := filepath.Join(dataDir, forgetDoneFile)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil { //nolint:gosec // our own data folder
			var d doneJSON
			if json.Unmarshal(b, &d) == nil && d.Nonce == r.Nonce {
				_ = os.Remove(path)
				if d.Error != "" {
					return fmt.Errorf("control: the daemon couldn't forget: %s", d.Error)
				}
				return nil
			}
		}
		time.Sleep(poll)
	}
	return ErrNoAnswer
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := fileedit.WriteAtomic(path, b, 0o600); err != nil {
		return fmt.Errorf("control: %w", err)
	}
	return nil
}
