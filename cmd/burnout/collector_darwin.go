//go:build darwin

package main

import (
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/collector/darwin"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

func newCollector(o collectorOptions) (ports.Collector, error) {
	return darwin.New(darwin.Options{Titles: o.Titles, InputCounts: o.InputCounts, Now: o.Now})
}
