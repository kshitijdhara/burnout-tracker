package crashloop

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

func TestStartTurnsSafeModeOnAtLimit(t *testing.T) {
	var h History
	for i := range Limit {
		h = h.Start(t0.Add(time.Duration(i) * 30 * time.Second))
		if want := i+1 >= Limit; h.Safe != want {
			t.Fatalf("start %d: safe = %v, want %v", i+1, h.Safe, want)
		}
	}
}

func TestOldStartsDontCount(t *testing.T) {
	h := History{}.Start(t0).Start(t0.Add(time.Minute))
	h = h.Start(t0.Add(Window)) // the first start is now Window old
	if h.Safe || len(h.Starts) != 2 {
		t.Fatalf("history = %+v, want 2 starts and no safe mode", h)
	}
}

func TestFutureStartsDontCount(t *testing.T) {
	// The clock moved back: starts "after" now are dropped, not counted.
	h := History{Starts: []time.Time{t0.Add(time.Hour), t0.Add(2 * time.Hour)}}.Start(t0)
	if h.Safe || len(h.Starts) != 1 {
		t.Fatalf("history = %+v, want 1 start and no safe mode", h)
	}
}

func TestSafeModeIsSticky(t *testing.T) {
	// A restart long after safe mode began stays safe until Clear.
	h := History{Safe: true}.Start(t0)
	if !h.Safe {
		t.Fatal("safe mode ended without a clean hour")
	}
}

func TestDropForgetsOneCleanStart(t *testing.T) {
	h := History{}.Start(t0).Start(t0.Add(time.Minute))
	h = h.Drop(t0.Add(time.Minute)).Start(t0.Add(2 * time.Minute))
	if h.Safe || len(h.Starts) != 2 {
		t.Fatalf("history = %+v: a clean stop counted as a crash", h)
	}
}

func TestFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if h := Load(dir); h.Safe || len(h.Starts) != 0 {
		t.Fatalf("no file: %+v", h)
	}
	var at time.Time
	for i := range Limit {
		h, ts, err := Record(dir, t0.Add(time.Duration(i)*time.Second+123456))
		if err != nil {
			t.Fatal(err)
		}
		at = ts
		if h.Safe != (i+1 >= Limit) {
			t.Fatalf("start %d: safe = %v", i+1, h.Safe)
		}
	}
	if err := Forget(dir, at); err != nil {
		t.Fatal(err)
	}
	if h := Load(dir); !h.Safe || len(h.Starts) != Limit-1 {
		t.Fatalf("after Forget: %+v", h)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, FileName))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want owner-only", fi.Mode().Perm())
		}
	}
	if err := Clear(dir); err != nil {
		t.Fatal(err)
	}
	if err := Clear(dir); err != nil {
		t.Fatal("clearing twice:", err)
	}
	if h := Load(dir); h.Safe || len(h.Starts) != 0 {
		t.Fatalf("after Clear: %+v", h)
	}
}

func TestUnreadableFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if h := Load(dir); h.Safe || len(h.Starts) != 0 {
		t.Fatalf("corrupt file: %+v", h)
	}
}
