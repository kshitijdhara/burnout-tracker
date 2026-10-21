//go:build unix && !linux && !darwin

package ingest

import "errors"

// peerUID is unsupported on other Unix systems, so every peer is rejected
// rather than trusted.
func peerUID(int) (uint32, error) {
	return 0, errors.New("ingest: peer credentials unsupported on this OS")
}
