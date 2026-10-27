// Package ports declares the interfaces the domain and application layers
// depend on. Adapters implement them; cmd/ wires them together.
package ports

import (
	"context"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// Clock is injected everywhere so domain code never calls time.Now.
type Clock interface {
	Now() time.Time
}

// Collector produces desktop snapshots. Implementations are per OS.
type Collector interface {
	Snapshot(ctx context.Context) (model.Snapshot, error)
	// Capabilities reports what this collector can see, for doctor.
	Capabilities() Capabilities
	Close() error
}

// Capabilities describes what a collector can observe on this machine.
type Capabilities struct {
	Name           string
	Windows        bool // window list and bounds
	Pointer        bool
	Idle           bool
	InputCounts    bool // opt-in keystroke/scroll counters available
	Titles         bool // window titles available (opt-in, may need a permission)
	MissingPermits []string
	// Limitations lists known gaps on this platform, e.g. "video and call
	// detection not implemented", so doctor can show them.
	Limitations []string
}

// ProcessInfo answers questions about the process tree.
type ProcessInfo interface {
	// Ancestors returns pid's ancestors, nearest first, up to limit.
	Ancestors(pid, limit int) ([]int, error)
}

// Indicator reports whether the "collecting" indicator (the tray or menubar
// icon) is visible right now. Nothing is recorded while it isn't (privacy &
// security §5.2, AC-1). Visible is called from several goroutines and must
// be cheap and safe for concurrent use.
type Indicator interface {
	Visible() bool
}
