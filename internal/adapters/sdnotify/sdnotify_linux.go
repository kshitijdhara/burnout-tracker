package sdnotify

import (
	"fmt"
	"net"
	"os"
)

// Send sends state to the socket in NOTIFY_SOCKET: a path, or an abstract
// socket written with a leading @ (Go maps it to the leading NUL).
func Send(state string) error {
	addr := os.Getenv(EnvSocket)
	if addr == "" {
		return nil
	}
	if addr[0] != '/' && addr[0] != '@' {
		return fmt.Errorf("sdnotify: unsupported %s %q", EnvSocket, addr)
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("sdnotify: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(state)); err != nil {
		return fmt.Errorf("sdnotify: %w", err)
	}
	return nil
}
