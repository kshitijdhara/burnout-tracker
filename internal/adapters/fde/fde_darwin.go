//go:build darwin

package fde

import "context"

func status(ctx context.Context, _ string) (State, string, error) {
	// FileVault protects the whole boot volume group, so the path doesn't matter.
	out, err := output(ctx, "/usr/bin/fdesetup", "status")
	if err != nil {
		return Unknown, "could not run fdesetup", err
	}
	s, detail := parseFdesetup(out)
	return s, detail, nil
}
