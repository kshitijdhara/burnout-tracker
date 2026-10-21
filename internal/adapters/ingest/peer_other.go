//go:build unix && !linux && !darwin

package ingest

import "errors"

// peerCred is unsupported on other Unix systems, so every peer is rejected
// rather than trusted.
func peerCred(int) (uint32, int, error) {
	return 0, 0, errors.New("ingest: peer credentials unsupported on this OS")
}
