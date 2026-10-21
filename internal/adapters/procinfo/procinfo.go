// Package procinfo answers questions about the process tree: a process's
// ancestors and executable name. The daemon uses it to map a shell or an
// agent to the terminal window that hosts it.
package procinfo

import (
	"errors"
	"fmt"
)

// Info implements ports.ProcessInfo for the current OS.
type Info struct{}

// ErrNoProcess means the process does not exist (or is not visible).
var ErrNoProcess = errors.New("procinfo: no such process")

// Ancestors returns pid's ancestors, nearest first, stopping at pid 1 (or 0),
// on a cycle, or after limit entries. pid itself is not included.
func (Info) Ancestors(pid, limit int) ([]int, error) {
	return ancestors(pid, limit, parentOf)
}

// Name returns the executable base name of pid.
func (Info) Name(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("%w: %d", ErrNoProcess, pid)
	}
	return nameOf(pid)
}

func ancestors(pid, limit int, parent func(int) (int, error)) ([]int, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrNoProcess, pid)
	}
	var out []int
	seen := map[int]bool{pid: true}
	for cur := pid; len(out) < limit; {
		p, err := parent(cur)
		if err != nil {
			if len(out) == 0 {
				return nil, err
			}
			// The chain broke above us (a parent exited); what we have is still useful.
			return out, nil
		}
		if p <= 0 || seen[p] {
			return out, nil
		}
		out = append(out, p)
		if p == 1 {
			return out, nil
		}
		seen[p] = true
		cur = p
	}
	return out, nil
}
