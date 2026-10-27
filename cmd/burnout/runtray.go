//go:build !e2e

package main

import (
	"context"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/tray"
)

// runTray shows the tray icon and runs work beside it.
func runTray(ctx context.Context, m tray.Menu, work tray.Work) error {
	return tray.Run(ctx, m, work)
}
