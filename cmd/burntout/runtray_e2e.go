//go:build e2e

package main

import (
	"context"

	"github.com/kshitijdhara/burntout/internal/adapters/tray"
)

// runTray in the end-to-end test binary skips the tray, which CI runners
// can't show, and runs work as if the icon were visible. Release builds
// never include this file.
func runTray(ctx context.Context, _ tray.Menu, work tray.Work) error {
	return work(ctx, true)
}
