// Package darwin is the macOS desktop collector. It reads the on-screen
// window list, display layout, pointer, idle time and lock state through
// public CoreGraphics and AppKit APIs, and turns them into model.Snapshot.
//
// Framework calls live in a thin cgo layer (collector_darwin.go). Everything
// else (filtering, occlusion, focus and pointer attribution, counter deltas)
// is plain Go in translate.go, so it is tested on every platform.
package darwin

import "time"

// Options configures the collector. The zero value collects no titles and
// no input counts, which needs no macOS permissions at all.
type Options struct {
	// Titles reads window titles for in-memory classification. It needs the
	// Screen Recording permission; titles are never persisted.
	Titles bool
	// InputCounts counts key presses, scrolls and clicks (never which keys)
	// through a listen-only event tap. It needs the Input Monitoring
	// permission and is opt-in.
	InputCounts bool
	// Now is the clock used to stamp snapshots. Nil means time.Now.
	Now func() time.Time
}
