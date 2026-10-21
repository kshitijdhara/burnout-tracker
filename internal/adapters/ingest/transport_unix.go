//go:build unix

package ingest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
)

const socketName = "burnout.sock"

// errPeerRejected is returned by peer checks for connections from another user.
var errPeerRejected = errors.New("ingest: peer is not the current user")

// Address returns the daemon's socket path: <runtime dir>/burnout.sock.
func Address() (string, error) {
	dir, err := paths.RuntimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, socketName), nil
}

// Listen opens the daemon's socket at Address.
func Listen(ctx context.Context) (*Server, error) {
	addr, err := Address()
	if err != nil {
		return nil, err
	}
	return listenAt(ctx, addr)
}

func listenAt(ctx context.Context, path string) (*Server, error) {
	if err := paths.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := clearStale(ctx, path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("ingest: listen %s: %w", path, err)
	}
	// The directory is already 0700; the socket mode is defence in depth.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("ingest: chmod socket: %w", err)
	}
	return newServer(ln, checkPeer), nil
}

// clearStale removes a socket left behind by a crashed daemon, but refuses
// to touch a live daemon's socket or anything that isn't a socket.
func clearStale(ctx context.Context, path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ingest: stat %s: %w", path, err)
	}
	dctx, cancel := context.WithTimeout(ctx, staleDialLimit)
	defer cancel()
	if c, err := dial(dctx, path); err == nil {
		_ = c.Close()
		return ErrAlreadyRunning
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("ingest: %s exists and is not a socket", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("ingest: remove stale socket: %w", err)
	}
	return nil
}

// checkAddress refuses to send to a socket whose directory another user
// could control (for example a misconfigured XDG_RUNTIME_DIR=/tmp), so hook
// data never reaches someone else's listener.
func checkAddress(addr string) error {
	return paths.CheckOwnedPrivateDir(filepath.Dir(addr))
}

func dial(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", addr)
}

// checkPeer rejects connections from any user other than the daemon's own
// and returns the peer's PID for per-source limits.
func checkPeer(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errPeerRejected
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var (
		uid     uint32
		pid     int
		credErr error
	)
	if err := raw.Control(func(fd uintptr) { uid, pid, credErr = peerCred(int(fd)) }); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, fmt.Errorf("%w: %w", errPeerRejected, credErr)
	}
	return pid, sameUser(uid, os.Getuid())
}

func sameUser(peer uint32, self int) error {
	if self < 0 || int64(peer) != int64(self) {
		return errPeerRejected
	}
	return nil
}
