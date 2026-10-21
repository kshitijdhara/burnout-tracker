//go:build linux

package procinfo

import "testing"

func TestParseStatPPID(t *testing.T) {
	tests := map[string]int{
		"1234 (zsh) S 1200 1234 1234 34816 1234 4194560":       1200,
		"1234 (my) prog) (x) R 77 1 1 0 -1 4194560":            77, // comm contains ') ('
		"55 (tmux: server) S 1 55 55 0 -1 4194624 0 0 0 0 0 0": 1,
	}
	for in, want := range tests {
		got, err := parseStatPPID(in)
		if err != nil || got != want {
			t.Errorf("parseStatPPID(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "no paren", "1 (x)", "1 (x) S notanumber"} {
		if _, err := parseStatPPID(bad); err == nil {
			t.Errorf("parseStatPPID(%q) accepted", bad)
		}
	}
}
