//go:build windows

package ingest

import (
	"context"
	"fmt"
	"net"
	"os/user"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// Address returns the daemon's named pipe: \\.\pipe\burnout-<user>.
func Address() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("ingest: current user: %w", err)
	}
	return `\\.\pipe\burnout-` + sanitise(u.Username), nil
}

// sanitise keeps pipe names to a conservative character set; Windows
// usernames arrive as DOMAIN\user.
func sanitise(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, s)
}

// Listen opens the daemon's named pipe. Its DACL grants access only to the
// current user's SID; go-winio always sets PIPE_REJECT_REMOTE_CLIENTS.
func Listen(ctx context.Context) (*Server, error) {
	addr, err := Address()
	if err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, staleDialLimit)
	defer cancel()
	if c, err := dial(dctx, addr); err == nil {
		_ = c.Close()
		return nil, ErrAlreadyRunning
	}
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	ln, err := winio.ListenPipe(addr, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;" + sid + ")",
		InputBufferSize:    8192,
		OutputBufferSize:   512,
	})
	if err != nil {
		return nil, fmt.Errorf("ingest: listen %s: %w", addr, err)
	}
	// The DACL restricts peers, so no per-connection check is needed.
	return newServer(ln, nil), nil
}

func currentUserSID() (string, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("ingest: token user: %w", err)
	}
	return tu.User.Sid.String(), nil
}

func dial(ctx context.Context, addr string) (net.Conn, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Second)
		defer cancel()
	}
	return winio.DialPipeContext(ctx, addr)
}
