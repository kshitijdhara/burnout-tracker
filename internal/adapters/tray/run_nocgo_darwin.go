//go:build darwin && !cgo

package tray

import "context"

// run without cgo can't reach the menu bar. Such builds have no screen
// collector either (internal/adapters/collector/darwin), so only hooks run.
func run(ctx context.Context, m Menu, work Work) error {
	m.Log.Warn("built without cgo: no menu-bar icon, so the screen isn't watched")
	return work(ctx, false)
}
