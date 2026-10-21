//go:build linux

package procinfo

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

func parentOf(pid int) (int, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("%w: %d", ErrNoProcess, pid)
	}
	if err != nil {
		return 0, fmt.Errorf("procinfo: read stat %d: %w", pid, err)
	}
	return parseStatPPID(string(b))
}

// parseStatPPID extracts the parent pid from /proc/<pid>/stat. The command
// name (field 2) is in parentheses and may itself contain spaces and ')',
// so the remaining fields start after the LAST ')'.
func parseStatPPID(stat string) (int, error) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, errors.New("procinfo: malformed stat")
	}
	fields := strings.Fields(stat[i+1:])
	if len(fields) < 2 {
		return 0, errors.New("procinfo: malformed stat")
	}
	ppid, err := strconv.Atoi(fields[1]) // fields[0] is the state
	if err != nil {
		return 0, fmt.Errorf("procinfo: malformed stat: %w", err)
	}
	return ppid, nil
}

func nameOf(pid int) (string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: %d", ErrNoProcess, pid)
	}
	if err != nil {
		return "", fmt.Errorf("procinfo: read comm %d: %w", pid, err)
	}
	return strings.TrimSpace(string(b)), nil
}
