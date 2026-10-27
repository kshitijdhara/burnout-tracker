//go:build !linux

package main

import (
	"context"
	"io"
)

// reportDesktopBridge: the GNOME Shell bridge exists only on Linux.
func reportDesktopBridge(context.Context, io.Writer, bool) {}
