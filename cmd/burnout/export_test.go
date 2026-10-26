package main

import (
	"testing"
	"time"
)

func TestExportRange(t *testing.T) {
	now := time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC) // before the rollover: logical day 2026-10-25
	r, err := exportRange("", "", now, 4*time.Hour)
	if err != nil || r.From != "0001-01-01" || r.To != "2026-10-25" || !r.End.Equal(time.Date(2026, 10, 26, 4, 0, 0, 0, time.UTC)) {
		t.Fatalf("default range %+v, %v", r, err)
	}
	r, err = exportRange("2026-10-01", "2026-10-07", now, 4*time.Hour)
	if err != nil || !r.Start.Equal(time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC)) || !r.End.Equal(time.Date(2026, 10, 8, 4, 0, 0, 0, time.UTC)) {
		t.Fatalf("range %+v, %v", r, err)
	}
	for _, bad := range [][2]string{{"yesterday", ""}, {"", "2026-13-01"}, {"2026-10-07", "2026-10-01"}} {
		if _, err := exportRange(bad[0], bad[1], now, 4*time.Hour); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
