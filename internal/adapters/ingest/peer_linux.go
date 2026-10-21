//go:build linux

package ingest

import "golang.org/x/sys/unix"

// peerCred reads the connecting process's UID and PID via SO_PEERCRED.
func peerCred(fd int) (uid uint32, pid int, err error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, 0, err
	}
	return cred.Uid, int(cred.Pid), nil
}
