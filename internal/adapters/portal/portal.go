// Package portal opens files through the XDG desktop portal on Linux
// (org.freedesktop.portal.OpenURI). The portal gets an open file
// descriptor, not a path: sandboxed browsers (snap, flatpak) can read the
// file through the document portal, and nothing names it on a command
// line. Other systems have no portal; Open reports ErrUnavailable there.
package portal

import "errors"

// ErrUnavailable means there is no desktop portal to ask, so the caller
// opens the file another way.
var ErrUnavailable = errors.New("portal: no desktop portal")
