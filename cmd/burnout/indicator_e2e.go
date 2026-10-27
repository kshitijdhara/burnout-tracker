//go:build e2e

package main

import "github.com/kshitijdhara/burnout-tracker/internal/domain/ports"

// alwaysVisible stands in for the tray in the end-to-end test binary only.
// Release builds never include this file.
type alwaysVisible struct{}

func (alwaysVisible) Visible() bool { return true }

func newIndicator() (ports.Indicator, error) { return alwaysVisible{}, nil }
