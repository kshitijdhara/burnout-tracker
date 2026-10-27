//go:build linux

package main

import (
	"context"
	"fmt"
	"io"

	"github.com/kshitijdhara/burntout/internal/adapters/collector/wayland"
)

// reportDesktopBridge reports the GNOME Shell extension on GNOME Wayland:
// whether GNOME has it installed and enabled, and whether it answers the
// handshake.
func reportDesktopBridge(ctx context.Context, w io.Writer, titles bool) {
	if s := currentSession(); !s.wayland() || !s.gnome() {
		return
	}
	answering, detail := false, ""
	if c, err := wayland.New(ctx, wayland.Options{Titles: titles}); err == nil {
		ext, shell := c.Hello()
		_ = c.Close()
		answering, detail = true, fmt.Sprintf(" (extension %s, protocol v%d, GNOME Shell %s)", ext, wayland.Version, shell)
	}
	info, ok := wayland.ExtensionInfo(ctx)
	if !ok && !answering {
		_, _ = fmt.Fprintln(w, "  ?  GNOME Shell bridge: GNOME Shell did not answer")
		return
	}
	good, line := info.Describe(answering)
	check(w, "GNOME Shell bridge", good, line+detail)
}
