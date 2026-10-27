//go:build !darwin || cgo

package tray

import (
	"errors"
	"testing"
	"time"
)

func TestStateNotes(t *testing.T) {
	now := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	rec := Status{Text: "Recording"}
	var s state
	if v := s.view(rec, now); v.Line != "Recording" || v.Paused || v.Busy {
		t.Fatalf("idle: %+v", v)
	}
	del := action{done: "Deleted today", fail: "Couldn't delete today"}
	s.busy = true
	if v := s.view(rec, now); !v.Busy {
		t.Error("a running action doesn't turn the items off")
	}
	s.finished(del, nil, now)
	if v := s.view(rec, now.Add(time.Second)); v.Line != "Deleted today" || v.Busy {
		t.Errorf("after success: %+v", v)
	}
	if v := s.view(rec, now.Add(noteFor)); v.Line != "Recording" {
		t.Errorf("the note outlasts noteFor: %+v", v)
	}
	s.finished(del, errors.New("no answer"), now)
	if v := s.view(rec, now); v.Line != "Couldn't delete today" {
		t.Errorf("after failure: %+v", v)
	}
	// A pause shows the new status, not a note.
	s.finished(action{fail: "Couldn't pause"}, nil, now)
	if v := s.view(Status{Paused: true, Text: "Paused until 16:00"}, now); v.Line != "Paused until 16:00" || !v.Paused {
		t.Errorf("after a pause: %+v", v)
	}
}

// TestTitle: the reminder takes the place beside the icon before a nudge,
// and the nudge never takes the first line.
func TestTitle(t *testing.T) {
	now := time.Date(2026, 10, 26, 22, 0, 0, 0, time.UTC)
	n := Nudge{Kind: "shutdown", Title: "Wrap up?", Text: "A good moment to wrap up?"}
	var s state
	for _, tc := range []struct {
		st    Status
		title string
	}{
		{Status{Text: "Recording"}, ""},
		{Status{Text: "Recording", Nudge: n}, "Wrap up?"},
		{Status{Text: "Recording", Reminder: true}, reviewTitle},
		{Status{Text: "Recording", Reminder: true, Nudge: n}, reviewTitle},
	} {
		v := s.view(tc.st, now)
		if got := v.title(); got != tc.title {
			t.Errorf("%+v: title %q, want %q", tc.st, got, tc.title)
		}
		if v.Line != "Recording" {
			t.Errorf("%+v: first line %q", tc.st, v.Line)
		}
	}
}

func TestNudgeActions(t *testing.T) {
	var got []string
	m := Menu{
		SnoozeNudge:  func(k string) error { got = append(got, "snooze "+k); return nil },
		TurnOffNudge: func(k string) error { got = append(got, "off "+k); return nil },
	}
	_ = m.actions("shutdown")["nudge-snooze"].run()
	_ = m.actions("day_off")["nudge-off"].run()
	if len(got) != 2 || got[0] != "snooze shutdown" || got[1] != "off day_off" {
		t.Errorf("%q", got)
	}
}
