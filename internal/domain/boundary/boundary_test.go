package boundary

import (
	"errors"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	for _, c := range []struct{ kind, value, want string }{
		{"shutdown", "21:30", "21:30"},
		{"shutdown", "9:05", "09:05"},
		{"no-weekend", "", ""},
		{"no_weekend", " ", ""},
		{"meetings-max", "4h", "4"},
		{"meetings-max", "3.50", "3.5"},
		{"focus-block", "90m", "90"},
	} {
		b, err := Parse(c.kind, c.value)
		if err != nil || b.Value != c.want || b.Validate() != nil {
			t.Errorf("Parse(%q, %q) = %+v, %v", c.kind, c.value, b, err)
		}
	}
	for _, c := range [][2]string{
		{"shutdown", "25:00"}, {"shutdown", ""}, {"no-weekend", "yes"}, {"meetings-max", "0"},
		{"meetings-max", "13"}, {"focus-block", "10"}, {"focus-block", "1.5"}, {"bedtime", "22:00"},
	} {
		if _, err := Parse(c[0], c[1]); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q, %q) accepted: %v", c[0], c[1], err)
		}
	}
	if err := (Boundary{Kind: Shutdown, Value: "9:30"}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("unnormalised value accepted: %v", err)
	}
}

func TestActiveOnAndAccessors(t *testing.T) {
	b := Boundary{Kind: Shutdown, Value: "21:30", Since: "2026-10-21", Until: "2026-10-24"}
	for day, want := range map[string]bool{"2026-10-20": false, "2026-10-21": true, "2026-10-23": true, "2026-10-24": false} {
		if b.ActiveOn(day) != want {
			t.Errorf("ActiveOn(%s) = %v", day, !want)
		}
	}
	if b.Active() || b.ShutdownAt() != 21*time.Hour+30*time.Minute {
		t.Errorf("%+v", b)
	}
	if got := (Boundary{Kind: MeetingsMax, Value: "1"}).Describe(); got != "at most 1 hour of meetings on workdays" {
		t.Error(got)
	}
	if n := (Boundary{Kind: FocusBlock, Value: "90"}).Number(); n != 90 {
		t.Error(n)
	}
}
