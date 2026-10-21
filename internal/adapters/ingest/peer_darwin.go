//go:build darwin

package ingest

import "golang.org/x/sys/unix"

// peerCred reads the connecting process's UID via LOCAL_PEERCRED and its PID
// via LOCAL_PEERPID.
func peerCred(fd int) (uid uint32, pid int, err error) {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, 0, err
	}
	pid, err = unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	if err != nil {
		return 0, 0, err
	}
	return cred.Uid, pid, nil
}
