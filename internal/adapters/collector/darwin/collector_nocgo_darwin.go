//go:build darwin && !cgo

package darwin

import (
	"context"
	"errors"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

var errNoCgo = errors.New("darwin: the macOS collector needs cgo; build with CGO_ENABLED=1")

// Collector is unavailable without cgo; New always fails.
type Collector struct{}

var _ ports.Collector = (*Collector)(nil)

// New fails: CoreGraphics and AppKit are only reachable through cgo.
func New(Options) (*Collector, error) { return nil, errNoCgo }

// Snapshot always fails.
func (*Collector) Snapshot(context.Context) (model.Snapshot, error) {
	return model.Snapshot{}, errNoCgo
}

// Capabilities reports nothing.
func (*Collector) Capabilities() ports.Capabilities { return ports.Capabilities{Name: "darwin"} }

// Close does nothing.
func (*Collector) Close() error { return nil }
