//go:build windows

package paths

// CheckTrustedExecutable is not checked on Windows yet: ACLs apply, and
// there is no background service there (see ErrUnverified).
func CheckTrustedExecutable(string) error { return nil }
