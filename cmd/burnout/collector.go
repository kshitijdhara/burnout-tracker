package main

import "time"

// collectorOptions are the platform-neutral collector settings. Titles and
// input counts are opt-in (privacy & security §3.1): titles follow
// privacy.title_classification; input counts are off.
type collectorOptions struct {
	Titles      bool
	InputCounts bool
	Now         func() time.Time
	// Wait is set by the daemon: a desktop bridge that isn't up yet (the
	// GNOME Shell extension) is redialled by Snapshot instead of failing.
	// doctor leaves it off, so it can report the missing bridge.
	Wait bool
}
