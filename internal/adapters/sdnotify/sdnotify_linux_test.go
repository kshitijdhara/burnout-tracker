package sdnotify

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// listen binds a fake systemd notification socket at addr.
func listen(t *testing.T, addr string) *net.UnixConn {
	t.Helper()
	c, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func receive(t *testing.T, c *net.UnixConn) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 256)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return string(buf[:n])
}

func TestPathSocket(t *testing.T) {
	// Not t.TempDir: socket paths are limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "sd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	addr := filepath.Join(dir, "notify")
	c := listen(t, addr)
	t.Setenv(EnvSocket, addr)

	if err := Ready(); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, c); got != "READY=1" {
		t.Fatalf("got %q", got)
	}
	if err := Watchdog(); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, c); got != "WATCHDOG=1" {
		t.Fatalf("got %q", got)
	}
}

func TestAbstractSocket(t *testing.T) {
	addr := "@burnout-sdnotify-test-" + strconv.Itoa(os.Getpid())
	c := listen(t, addr)
	t.Setenv(EnvSocket, addr)
	if err := Ready(); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, c); got != "READY=1" {
		t.Fatalf("got %q", got)
	}
	// The name really is abstract: nothing appears on disk.
	if _, err := os.Lstat(addr); err == nil {
		t.Fatalf("%s exists on disk", addr)
	}
}

func TestUnsetIsNoOp(t *testing.T) {
	t.Setenv(EnvSocket, "")
	if err := Ready(); err != nil {
		t.Fatal(err)
	}
}

func TestBadSocket(t *testing.T) {
	t.Setenv(EnvSocket, "vsock:2:1")
	if Ready() == nil {
		t.Fatal("unsupported address accepted")
	}
	t.Setenv(EnvSocket, "/nonexistent/notify")
	if Ready() == nil {
		t.Fatal("missing socket accepted")
	}
}
