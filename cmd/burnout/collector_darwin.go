//go:build darwin

package main

import (
	"context"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/collector/darwin"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

func newCollector(_ context.Context, o collectorOptions) (ports.Collector, error) {
	return darwin.New(darwin.Options{Titles: o.Titles, InputCounts: o.InputCounts, Now: o.Now})
}
