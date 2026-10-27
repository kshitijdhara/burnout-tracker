package desktopbus

import (
	"math"
	"testing"
	"time"
)

func TestIsLocked(t *testing.T) {
	tests := []struct{ hint, active, want bool }{
		{false, true, false},
		{true, true, true},
		{false, false, true}, // another session is in the foreground
	}
	for _, tc := range tests {
		if got := IsLocked(tc.hint, tc.active); got != tc.want {
			t.Errorf("IsLocked(%v, %v) = %v", tc.hint, tc.active, got)
		}
	}
}

func TestIdleDuration(t *testing.T) {
	if got := IdleDuration(1500); got != 1500*time.Millisecond {
		t.Errorf("IdleDuration(1500) = %v", got)
	}
	if got := IdleDuration(math.MaxUint64); got != math.MaxInt64 {
		t.Errorf("IdleDuration(max) = %v, want saturation", got)
	}
}
