package control

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestPause(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	if paused, _ := PauseState(dir, now); paused {
		t.Fatal("paused with no pause file")
	}
	if err := SetPause(dir, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if paused, p := PauseState(dir, now); !paused || !p.Until.Equal(now.Add(time.Hour)) {
		t.Fatalf("paused %v until %v", paused, p.Until)
	}
	if paused, _ := PauseState(dir, now.Add(2*time.Hour)); paused {
		t.Fatal("still paused after the pause ended")
	}
	if err := SetPause(dir, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if paused, p := PauseState(dir, now.AddDate(1, 0, 0)); !paused || !p.Until.IsZero() {
		t.Fatalf("until resumed: %v %v", paused, p)
	}
	// Windows has no mode bits; the data folder's owner-only ACL applies.
	if fi, _ := os.Stat(filepath.Join(dir, pauseFile)); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("pause file mode %v", fi.Mode().Perm())
	}
	if err := Resume(dir); err != nil {
		t.Fatal(err)
	}
	if err := Resume(dir); err != nil {
		t.Fatalf("second resume: %v", err)
	}
	if paused, _ := PauseState(dir, now); paused {
		t.Fatal("paused after resume")
	}
	// A damaged pause file errs on the side of collecting nothing.
	if err := os.WriteFile(filepath.Join(dir, pauseFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if paused, _ := PauseState(dir, now); !paused {
		t.Error("damaged pause file read as not paused")
	}
}

func TestForgetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	since := time.UnixMilli(1_800_000_000_000)
	r, err := RequestForget(dir, since)
	if err != nil || !r.Since().Equal(since) || len(r.Nonce) != 16 {
		t.Fatalf("request %+v, %v", r, err)
	}
	got, ok := TakeForget(dir)
	if !ok || got != r {
		t.Fatalf("take: %+v %v", got, ok)
	}
	if _, ok := TakeForget(dir); ok {
		t.Fatal("request taken twice")
	}
	if err := WaitForgetDone(dir, r, 50*time.Millisecond, 10*time.Millisecond); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("no answer: %v", err)
	}
	if err := ForgetDone(dir, ForgetRequest{Nonce: "someone-else"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := WaitForgetDone(dir, r, 50*time.Millisecond, 10*time.Millisecond); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("another request's answer accepted: %v", err)
	}
	if err := ForgetDone(dir, r, errors.New("disk full")); err != nil {
		t.Fatal(err)
	}
	if err := WaitForgetDone(dir, r, time.Second, 10*time.Millisecond); err == nil || err.Error() != "control: the daemon couldn't forget: disk full" {
		t.Fatalf("failed answer: %v", err)
	}
	if err := ForgetDone(dir, r, nil); err != nil {
		t.Fatal(err)
	}
	if err := WaitForgetDone(dir, r, time.Second, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestReminderSeen(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReminderSeen(dir); ok {
		t.Fatal("seen with no reminder file")
	}
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	if err := SetReminderSeen(dir, at); err != nil {
		t.Fatal(err)
	}
	if seen, ok := ReminderSeen(dir); !ok || !seen.Equal(at) {
		t.Fatalf("seen %v %v", seen, ok)
	}
	if err := os.WriteFile(filepath.Join(dir, reminderFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReminderSeen(dir); ok {
		t.Fatal("an unreadable file must read as never seen")
	}
}

func TestNudges(t *testing.T) {
	dir := t.TempDir()
	if n, err := ReadNudges(dir); err != nil || n.Day != "" || len(n.Off) != 0 {
		t.Fatalf("no file: %+v %v", n, err)
	}
	if err := ShowNudge(dir, "2026-10-23", "shutdown"); err != nil {
		t.Fatal(err)
	}
	if err := SnoozeNudge(dir, "2026-10-23", "shutdown"); err != nil {
		t.Fatal(err)
	}
	// Off as v0.1.0-alpha.1's tray left it.
	if err := os.WriteFile(filepath.Join(dir, nudgesFile), []byte(`{"day":"2026-10-24","kind":"day_off","snoozed":true,"off":["day_off","shutdown"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NudgeOn(dir, "shutdown"); err != nil {
		t.Fatal(err)
	}
	if err := NudgeOn(dir, "shutdown"); err != nil {
		t.Fatal(err)
	}
	// A new day's nudge clears the snooze and keeps what is off.
	if err := ShowNudge(dir, "2026-10-26", "weekly_hours"); err != nil {
		t.Fatal(err)
	}
	n, err := ReadNudges(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n.Day != "2026-10-26" || n.Kind != "weekly_hours" || n.Snoozed || len(n.Off) != 1 || n.Off[0] != "day_off" {
		t.Errorf("state %+v", n)
	}
	if fi, _ := os.Stat(filepath.Join(dir, nudgesFile)); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("nudges file mode %v", fi.Mode().Perm())
	}

	// Unreadable: an error, so no nudge shows, and showing one doesn't
	// overwrite it, nor does turning a kind on; the user's own snooze does.
	if err := os.WriteFile(filepath.Join(dir, nudgesFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadNudges(dir); err == nil {
		t.Error("a corrupt file reads")
	}
	if err := ShowNudge(dir, "2026-10-27", "shutdown"); err == nil {
		t.Error("showing a nudge replaced a corrupt file")
	}
	if err := NudgeOn(dir, "shutdown"); err == nil {
		t.Error("turning a kind on replaced a corrupt file")
	}
	if err := SnoozeNudge(dir, "2026-10-27", "shutdown"); err != nil {
		t.Fatal(err)
	}
	if n, err := ReadNudges(dir); err != nil || !n.Snoozed || n.Kind != "shutdown" {
		t.Errorf("after snooze: %+v %v", n, err)
	}
}

func TestDaemonTitles(t *testing.T) {
	dir := t.TempDir()
	if DaemonTitles(dir) {
		t.Error("no file reads as on")
	}
	for _, on := range []bool{true, false} {
		if err := SetDaemonTitles(dir, on); err != nil {
			t.Fatal(err)
		}
		if DaemonTitles(dir) != on {
			t.Errorf("DaemonTitles = %v, want %v", !on, on)
		}
	}
}
