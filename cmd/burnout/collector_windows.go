//go:build windows

package main

import (
	"github.com/kshitijdhara/burnout-tracker/internal/adapters/collector/windows"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

func newCollector(o collectorOptions) (ports.Collector, error) {
	return windows.New(windows.Options{Titles: o.Titles, InputCounts: o.InputCounts, Now: o.Now})
}
