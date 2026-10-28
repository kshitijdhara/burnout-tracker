//go:build windows

package ingest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// Address returns the daemon's named pipe: \\.\pipe\burntout-<user SID>.
// Named pipes share one machine-wide namespace; the SID makes the name
// unique per user (usernames can collide after sanitising) and the client
// still verifies the server's identity before writing anything.
func Address() (string, error) {
	sid, err := currentUserSID()
	if err != nil {
		return "", err
	}
	return `\\.\pipe\burntout-` + sid, nil
}

// checkAddress has nothing to check on Windows: the client verifies the
// server process's user when it dials.
func checkAddress(string) error { return nil }

// Listen opens the daemon's named pipe. Its DACL grants access only to the
// current user's SID; go-winio always sets PIPE_REJECT_REMOTE_CLIENTS and
// creates the first instance with FILE_CREATE, so it fails if the name
// already exists.
func Listen(ctx context.Context) (*Server, error) {
	addr, err := Address()
	if err != nil {
		return nil, err
	}
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, staleDialLimit)
	defer cancel()
	if c, err := dial(dctx, addr); err == nil {
		_ = c.Close()
		return nil, ErrAlreadyRunning
	} else if errors.Is(err, errPipeHeldByOther) {
		return nil, fmt.Errorf("%w (%s)", err, addr)
	}
	ln, err := winio.ListenPipe(addr, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;" + sid + ")",
		InputBufferSize:    8192,
		OutputBufferSize:   512,
	})
	if err != nil {
		return nil, fmt.Errorf("ingest: listen %s (another process may hold the name): %w", addr, err)
	}
	return newServer(ln, clientPID), nil
}

// clientPID identifies the connecting process for per-source limits. The
// pipe's DACL already restricts connections to the current user.
func clientPID(c net.Conn) (int, error) {
	h, err := pipeHandle(c)
	if err != nil {
		return 0, err
	}
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(h, &pid); err != nil {
		return 0, fmt.Errorf("ingest: client pid: %w", err)
	}
	return int(pid), nil
}

func currentUserSID() (string, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("ingest: token user: %w", err)
	}
	return tu.User.Sid.String(), nil
}

// dial connects to the pipe and verifies, before anything is written, that
// the server process runs as the current user.
func dial(ctx context.Context, addr string) (net.Conn, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Second)
		defer cancel()
	}
	conn, err := winio.DialPipeContext(ctx, addr)
	if err != nil {
		return nil, err
	}
	if err := verifyServer(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func verifyServer(c net.Conn) error {
	h, err := pipeHandle(c)
	if err != nil {
		return err
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(h, &pid); err != nil {
		return fmt.Errorf("ingest: server pid: %w", err)
	}
	server, err := processUserSID(pid)
	if err != nil {
		return err
	}
	self, err := currentUserSID()
	if err != nil {
		return err
	}
	return sameSID(server, self)
}

func processUserSID(pid uint32) (string, error) {
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", fmt.Errorf("ingest: open server process: %w", err)
	}
	defer func() { _ = windows.CloseHandle(p) }()
	var tok windows.Token
	if err := windows.OpenProcessToken(p, windows.TOKEN_QUERY, &tok); err != nil {
		return "", fmt.Errorf("ingest: open server token: %w", err)
	}
	defer func() { _ = tok.Close() }()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("ingest: server token user: %w", err)
	}
	return tu.User.Sid.String(), nil
}

// pipeHandle extracts the Win32 handle from a go-winio pipe connection
// (its pipe types embed a file with an Fd method).
func pipeHandle(c net.Conn) (windows.Handle, error) {
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return 0, errors.New("ingest: connection has no pipe handle")
	}
	return windows.Handle(f.Fd()), nil
}
