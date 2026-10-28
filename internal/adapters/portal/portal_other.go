//go:build !linux

package portal

import "context"

// OpenFile reports ErrUnavailable: only Linux has the desktop portal.
func OpenFile(context.Context, string) error { return ErrUnavailable }
