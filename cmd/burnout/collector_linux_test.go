//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestX11Session(t *testing.T) {
	tests := []struct {
		display, session string
		wantErr          string
	}{
		{":0", "x11", ""},
		{":0", "", ""}, // started outside a login session, e.g. a test runner
		{":0", "wayland", "Wayland"},
		{"", "wayland", "Wayland"},
		{"", "x11", "DISPLAY"},
		{"", "tty", "DISPLAY"},
	}
	for _, tc := range tests {
		err := x11Session(tc.display, tc.session)
		if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("x11Session(%q, %q) = %v, want %q", tc.display, tc.session, err, tc.wantErr)
		}
	}
}
