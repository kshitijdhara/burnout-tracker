//go:build darwin

package procinfo

import (
	"bytes"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func kinfo(pid int) (*unix.KinfoProc, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if errors.Is(err, unix.ESRCH) {
		return nil, fmt.Errorf("%w: %d", ErrNoProcess, pid)
	}
	if err != nil {
		return nil, fmt.Errorf("procinfo: sysctl %d: %w", pid, err)
	}
	// The kernel returns a zeroed struct for pids that don't exist.
	if k.Proc.P_pid == 0 && pid != 0 {
		return nil, fmt.Errorf("%w: %d", ErrNoProcess, pid)
	}
	return k, nil
}

func parentOf(pid int) (int, error) {
	k, err := kinfo(pid)
	if err != nil {
		return 0, err
	}
	return int(k.Eproc.Ppid), nil
}

// nameOf returns p_comm, which the kernel truncates to 16 characters.
func nameOf(pid int) (string, error) {
	k, err := kinfo(pid)
	if err != nil {
		return "", err
	}
	comm := k.Proc.P_comm[:]
	if i := bytes.IndexByte(comm, 0); i >= 0 {
		comm = comm[:i]
	}
	return string(comm), nil
}
