//go:build !darwin && !windows && !linux

package main

import (
	"errors"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

func newCollector(collectorOptions) (ports.Collector, error) {
	return nil, errors.New("no desktop collector for this platform")
}
