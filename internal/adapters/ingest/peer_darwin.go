//go:build darwin

package ingest

import "golang.org/x/sys/unix"

// peerCred reads the connecting process's UID via LOCAL_PEERCRED and its PID
// via LOCAL_PEERPID. The UID is cached at connect time, so it is always
// available. The PID is not: LOCAL_PEERPID fails with ENOTCONN once the
// peer has closed, which hooks do right after writing. The PID only feeds
// per-source rate limiting, so it falls back to 0 (a shared bucket) rather
// than rejecting the connection.
func peerCred(fd int) (uid uint32, pid int, err error) {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, 0, err
	}
	pid, err = unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	if err != nil {
		pid = 0
	}
	return cred.Uid, pid, nil
}
