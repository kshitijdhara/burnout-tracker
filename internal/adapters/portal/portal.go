// Package portal opens files through the XDG desktop portal on Linux
// (org.freedesktop.portal.OpenURI). The portal gets an open file
// descriptor, not a path: sandboxed browsers (snap, flatpak) can read the
// file through the document portal, and nothing names it on a command
// line. Other systems have no portal; OpenFile reports ErrUnavailable
// there.
package portal

import "errors"

var (
	// ErrUnavailable means there is no desktop portal to ask, so the caller
	// opens the file another way.
	ErrUnavailable = errors.New("portal: no desktop portal")
	// ErrCancelled means the user closed the portal's app chooser.
	ErrCancelled = errors.New("portal: cancelled")
	// ErrNoAnswer means the portal took the request but didn't say how it
	// went in time; it may still open the file, so the caller must not open
	// it again another way.
	ErrNoAnswer = errors.New("portal: no answer")
)
