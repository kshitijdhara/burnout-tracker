package annotation

import (
	"errors"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	good := Annotation{From: "2026-10-20", To: "2026-10-24", Kind: Vacation, Note: "Lisbon"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	if good.Days() != 5 || !good.Covers("2026-10-24") || good.Covers("2026-10-25") || !good.Excludes() {
		t.Errorf("days %d", good.Days())
	}
	for name, a := range map[string]Annotation{
		"kind":     {From: "2026-10-20", To: "2026-10-20", Kind: "holiday"},
		"date":     {From: "20/10/2026", To: "2026-10-20", Kind: Sick},
		"reversed": {From: "2026-10-21", To: "2026-10-20", Kind: Sick},
		"too long": {From: "2025-01-01", To: "2026-10-20", Kind: Exam},
		"note":     {From: "2026-10-20", To: "2026-10-20", Kind: Other, Note: strings.Repeat("x", MaxNote+1)},
		"control":  {From: "2026-10-20", To: "2026-10-20", Kind: Other, Note: "a\x1b[31mb"},
	} {
		if err := a.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestExcluded(t *testing.T) {
	as := []Annotation{
		{From: "2026-10-20", To: "2026-10-21", Kind: Vacation},
		{From: "2026-10-22", To: "2026-10-22", Kind: OnCall}, // scored, not excluded
		{From: "2026-10-23", To: "2026-10-23", Kind: Sick},
	}
	got := Excluded(as, []string{"2026-10-19", "2026-10-20", "2026-10-21", "2026-10-22", "2026-10-23"})
	if len(got) != 3 || !got["2026-10-20"] || !got["2026-10-21"] || !got["2026-10-23"] || got["2026-10-22"] {
		t.Errorf("excluded %v", got)
	}
}
