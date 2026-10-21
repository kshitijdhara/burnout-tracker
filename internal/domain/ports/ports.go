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

// KeyStore holds secrets in the OS keystore (or a 0600 file fallback).
type KeyStore interface {
	// GetOrCreate returns the named secret, creating a random 32-byte value on first use.
	GetOrCreate(name string) (model.Sensitive[[]byte], error)
	Delete(name string) error
	// Backend names the storage in use, e.g. "keychain" or "file", for doctor.
	Backend() string
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
}

// ProcessInfo answers questions about the process tree.
type ProcessInfo interface {
	// Ancestors returns pid's ancestors, nearest first, up to limit.
	Ancestors(pid, limit int) ([]int, error)
}
