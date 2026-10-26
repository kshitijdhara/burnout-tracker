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
