//go:build linux

package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/collector/wayland"
)

func TestX11Session(t *testing.T) {
	tests := []struct {
		s       desktopSession
		wantErr string
	}{
		{desktopSession{display: ":0", sessionType: "x11"}, ""},
		{desktopSession{display: ":0"}, ""}, // started outside a login session, e.g. a test runner
		{desktopSession{display: ":0", sessionType: "wayland"}, "Wayland"},
		{desktopSession{display: ":0", waylandDisplay: "wayland-0"}, "Wayland"}, // service without XDG_SESSION_TYPE
		{desktopSession{sessionType: "wayland"}, "Wayland"},
		{desktopSession{sessionType: "x11"}, "DISPLAY"},
		{desktopSession{sessionType: "tty"}, "DISPLAY"},
	}
	for _, tc := range tests {
		err := x11Session(tc.s)
		if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("x11Session(%+v) = %v, want %q", tc.s, err, tc.wantErr)
		}
	}
}

func TestDesktopSessionGNOME(t *testing.T) {
	for desktop, want := range map[string]bool{
		"GNOME": true, "ubuntu:GNOME": true, "GNOME-Classic:GNOME": true, "gnome": true,
		"KDE": false, "": false, "sway": false, "GNOME-Flashback": false,
	} {
		if got := (desktopSession{desktop: desktop}).gnome(); got != want {
			t.Errorf("gnome(%q) = %v", desktop, got)
		}
	}
}

func TestGnomeBridgeError(t *testing.T) {
	if msg := gnomeBridgeError(wayland.ErrNotRunning).Error(); !strings.Contains(msg, wayland.EnableCommand) {
		t.Errorf("no enable hint: %q", msg)
	}
	other := errors.New("boom")
	if err := gnomeBridgeError(other); !errors.Is(err, other) {
		t.Errorf("cause lost: %v", err)
	}
}
