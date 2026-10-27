//go:build !e2e

package main

import (
	"errors"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

// The tray arrives in Phase 1. Until then there is nothing to show while
// collecting, so the daemon must not run (privacy & security §5.2, AC-1).
func newIndicator() (ports.Indicator, error) {
	return nil, errors.New("no tray icon available, so nothing may be recorded (AC-1); collection is disabled until the tray ships in Phase 1")
}
