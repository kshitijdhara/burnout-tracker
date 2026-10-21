//go:build !windows

package procinfo

// liveLookup reads the live process table for every query; on Linux and
// macOS each read is a cheap per-pid file read or sysctl.
type liveLookup struct{}

func newLookup() (lookup, error) { return liveLookup{}, nil }

func (liveLookup) parent(pid int) (int, error)  { return parentOf(pid) }
func (liveLookup) name(pid int) (string, error) { return nameOf(pid) }
