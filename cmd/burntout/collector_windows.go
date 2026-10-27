//go:build windows

package main

import (
	"context"

	"github.com/kshitijdhara/burntout/internal/adapters/collector/windows"
	"github.com/kshitijdhara/burntout/internal/domain/ports"
)

func newCollector(_ context.Context, o collectorOptions) (ports.Collector, error) {
	return windows.New(windows.Options{Titles: o.Titles, InputCounts: o.InputCounts, Now: o.Now})
}
