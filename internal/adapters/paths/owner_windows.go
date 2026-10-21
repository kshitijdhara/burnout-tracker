//go:build windows

package paths

import "os"

// checkOwner relies on the inherited owner-only ACL of %LOCALAPPDATA% on
// Windows; the ACL itself isn't inspected here (see ErrUnverified).
func checkOwner(string, os.FileInfo) error { return nil }
