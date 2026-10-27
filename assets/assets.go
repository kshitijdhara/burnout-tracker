// Package assets holds the files built into the binary: the tray icons.
// The PNGs are rendered from the SVGs next to them (see icon/README.md).
package assets

import _ "embed" // for the icons below

// Menu-bar template images for macOS: black and transparent, recoloured
// by the system for light and dark menu bars. Retina size (36 px for 18 pt).
var (
	//go:embed icon/tray/trayTemplate@2x.png
	TrayTemplate []byte
	//go:embed icon/tray/trayPausedTemplate@2x.png
	TrayPausedTemplate []byte
)

// Colour tray icons for Windows and Linux, whose taskbars and panels can
// be light or dark, at 16, 32 and 64 px.
var (
	//go:embed icon/tray/trayColor-16.png
	TrayColor16 []byte
	//go:embed icon/tray/trayColor-32.png
	TrayColor32 []byte
	//go:embed icon/tray/trayColor-64.png
	TrayColor64 []byte
	//go:embed icon/tray/trayPausedColor-16.png
	TrayPausedColor16 []byte
	//go:embed icon/tray/trayPausedColor-32.png
	TrayPausedColor32 []byte
	//go:embed icon/tray/trayPausedColor-64.png
	TrayPausedColor64 []byte
)
