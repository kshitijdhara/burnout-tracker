//go:build windows

package paths

// CheckTrustedExecutable is not checked on Windows yet: ACLs apply, which
// this package doesn't read yet (see ErrUnverified).
func CheckTrustedExecutable(string) error { return nil }
