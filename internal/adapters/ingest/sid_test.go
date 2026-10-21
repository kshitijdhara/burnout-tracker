package ingest

import (
	"errors"
	"testing"
)

func TestSameSID(t *testing.T) {
	const me = "S-1-5-21-1004336348-1177238915-682003330-1001"
	tests := []struct {
		server string
		ok     bool
	}{
		{me, true},
		{"s-1-5-21-1004336348-1177238915-682003330-1001", true},
		{"S-1-5-21-1004336348-1177238915-682003330-1002", false}, // another user
		{"S-1-5-18", false}, // LocalSystem
		{"", false},
	}
	for _, tc := range tests {
		err := sameSID(tc.server, me)
		if tc.ok != (err == nil) {
			t.Errorf("sameSID(%q) = %v", tc.server, err)
		}
		if err != nil && !errors.Is(err, errPipeHeldByOther) {
			t.Errorf("wrong error type: %v", err)
		}
	}
}
