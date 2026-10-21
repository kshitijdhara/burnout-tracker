//go:build !darwin

package paths

// ExcludeFromBackups is a no-op outside macOS. Windows File History skips
// %LOCALAPPDATA% by default; Linux backup tools are configured by the user
// (ADR-0007).
func ExcludeFromBackups(string) error { return nil }
