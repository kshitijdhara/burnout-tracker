//go:build darwin

package paths

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// ExcludeFromBackups adds a sticky Time Machine exclusion for dir. Backup
// drives are often unencrypted, so the data directory stays out of them
// (ADR-0007, privacy & security §7.3). Sticky exclusions travel with the
// directory and need no admin rights.
func ExcludeFromBackups(dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Fixed binary, no shell; dir is the app's own data directory passed as
	// a single argument.
	if out, err := exec.CommandContext(ctx, "/usr/bin/tmutil", "addexclusion", dir).CombinedOutput(); err != nil { //nolint:gosec // see above
		return fmt.Errorf("paths: tmutil addexclusion: %w (%s)", err, out)
	}
	return nil
}
