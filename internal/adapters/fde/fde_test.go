package fde

import (
	"context"
	"testing"
)

func TestParseFdesetup(t *testing.T) {
	tests := map[string]State{
		"FileVault is On.":  Enabled,
		"FileVault is Off.": Disabled,
		"Encryption in progress: Percent completed = 10": Enabled,
		"Decryption in progress: Percent completed = 10": Disabled,
		"": Unknown,
	}
	for in, want := range tests {
		if got, _ := parseFdesetup(in); got != want {
			t.Errorf("parseFdesetup(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseBitLockerProtection(t *testing.T) {
	tests := map[string]State{
		"1": Enabled, "6": Enabled, "3": Enabled,
		"0": Disabled, "2": Disabled, "4": Disabled, "5": Disabled, "8": Disabled,
		"7": Unknown, "": Unknown, "abc": Unknown, " 1\r\n": Enabled,
	}
	for in, want := range tests {
		if got, _ := parseBitLockerProtection(in); got != want {
			t.Errorf("parseBitLockerProtection(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestStateString(t *testing.T) {
	if Enabled.String() != "enabled" || Disabled.String() != "disabled" || Unknown.String() != "unknown" {
		t.Fatal("unexpected State strings")
	}
}

// On the real machine the call must succeed and never block, whatever it finds.
func TestStatusRuns(t *testing.T) {
	s, detail, err := Status(context.Background(), t.TempDir())
	t.Logf("state=%v detail=%q err=%v", s, detail, err)
	if detail == "" {
		t.Fatal("empty detail")
	}
}
