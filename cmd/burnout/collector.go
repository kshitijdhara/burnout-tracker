package main

import "time"

// collectorOptions are the platform-neutral collector settings. Titles and
// input counts are opt-in (privacy & security §3.1): titles follow
// privacy.title_classification; input counts are off.
type collectorOptions struct {
	Titles      bool
	InputCounts bool
	Now         func() time.Time
}
