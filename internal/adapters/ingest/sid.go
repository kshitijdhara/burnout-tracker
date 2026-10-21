package ingest

import (
	"errors"
	"fmt"
	"strings"
)

// errPipeHeldByOther means a process running as a different user holds our
// pipe name. Hooks never write to it, and the daemon refuses to start.
var errPipeHeldByOther = errors.New("ingest: the pipe name is held by a process of another user")

// sameSID reports whether the pipe server runs as the same user as us.
// SID strings are canonical apart from letter case of the "S" prefix.
func sameSID(server, self string) error {
	if server == "" || self == "" || !strings.EqualFold(server, self) {
		return fmt.Errorf("%w: server %s", errPipeHeldByOther, server)
	}
	return nil
}
