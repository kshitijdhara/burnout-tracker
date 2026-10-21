//go:build darwin || windows

package fde

import (
	"context"
	"os/exec"
	"strings"
)

// output runs a fixed command (no shell) and returns trimmed stdout.
func output(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output() //nolint:gosec // fixed binaries; arguments are constants or validated
	return strings.TrimSpace(string(out)), err
}
