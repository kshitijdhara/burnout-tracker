//go:build !darwin && !windows

package main

import (
	"errors"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

// Linux desktop collectors (X11, GNOME, KDE) arrive in Phase 1.
func newCollector(collectorOptions) (ports.Collector, error) {
	return nil, errors.New("no desktop collector for this platform yet (Linux arrives in Phase 1)")
}
