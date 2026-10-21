//go:build windows

package fde

import (
	"context"
	"path/filepath"
	"regexp"
)

var reDrive = regexp.MustCompile(`^[A-Za-z]:$`)

func status(ctx context.Context, path string) (State, string, error) {
	drive := filepath.VolumeName(path)
	if !reDrive.MatchString(drive) {
		// UNC paths and the like: we can't tell.
		return Unknown, "data directory is not on a local drive", nil
	}
	// The drive letter is validated above, so interpolating it is safe.
	script := "(New-Object -ComObject Shell.Application).NameSpace('" + drive + "').Self.ExtendedProperty('System.Volume.BitLockerProtection')"
	out, err := output(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	if err != nil {
		return Unknown, "could not query BitLocker", err
	}
	s, detail := parseBitLockerProtection(out)
	return s, detail, nil
}
