//go:build darwin

package main

import (
	"context"

	"github.com/kshitijdhara/burntout/internal/adapters/collector/darwin"
	"github.com/kshitijdhara/burntout/internal/domain/ports"
)

func newCollector(_ context.Context, o collectorOptions) (ports.Collector, error) {
	return darwin.New(darwin.Options{Titles: o.Titles, InputCounts: o.InputCounts, Now: o.Now})
}
