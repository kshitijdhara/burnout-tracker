package model

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestSensitiveNeverFormats(t *testing.T) {
	const secret = "hunter2-canary"
	s := NewSensitive(secret)
	outs := []string{
		fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s %q %x", s, s, s, s, s, s),
		s.String(), s.GoString(), s.LogValue().String(),
	}
	b, err := json.Marshal(struct{ S Sensitive[string] }{s})
	if err != nil {
		t.Fatal(err)
	}
	outs = append(outs, string(b))
	var sb strings.Builder
	slog.New(slog.NewJSONHandler(&sb, nil)).Info("x", "s", s)
	outs = append(outs, sb.String())
	for _, o := range outs {
		if strings.Contains(o, secret) {
			t.Fatalf("secret leaked: %q", o)
		}
	}
	if s.Reveal() != secret {
		t.Fatal("Reveal lost the value")
	}
}

func TestLogicalDay(t *testing.T) {
	loc := time.FixedZone("x", 2*3600)
	tests := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 10, 21, 1, 30, 0, 0, loc), "2026-10-20"},
		{time.Date(2026, 10, 21, 4, 0, 0, 0, loc), "2026-10-21"},
		{time.Date(2026, 10, 21, 23, 59, 0, 0, loc), "2026-10-21"},
	}
	for _, tc := range tests {
		if got := LogicalDay(tc.at, 4*time.Hour); got != tc.want {
			t.Errorf("LogicalDay(%v) = %s, want %s", tc.at, got, tc.want)
		}
	}
	if got := UTCOffsetMinutes(time.Date(2026, 1, 1, 0, 0, 0, 0, loc)); got != 120 {
		t.Errorf("offset = %d", got)
	}
}
