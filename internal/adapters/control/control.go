// Package control carries requests from the CLI to a running daemon
// through small files in the owner-only data folder: pausing collection
// (privacy & security §5.1 "Pause") and forgetting recent data ("Delete
// recent", "Forget"). Files, not the hook socket, so the socket stays
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
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/integrations/fileedit"
)

// File names in the data folder.
const (
	pauseFile      = "pause"
	forgetFile     = "forget-request"
	forgetDoneFile = "forget-done"
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
