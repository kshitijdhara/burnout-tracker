//go:build !darwin && !windows && !linux

package main

import (
	"context"
	"errors"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

func newCollector(context.Context, collectorOptions) (ports.Collector, error) {
	return nil, errors.New("no desktop collector for this platform")
}
