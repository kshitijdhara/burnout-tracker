//go:build linux

package portal

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"
)

// sessionBus starts a private session bus for the test, or skips the test
// where dbus-daemon isn't installed.
func sessionBus(t *testing.T) {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--print-address", "--address=unix:path="+filepath.Join(t.TempDir(), "bus"))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", strings.TrimSpace(addr))
}

// fakePortal is org.freedesktop.portal.OpenURI: it reads what it is given.
type fakePortal struct {
	mu   sync.Mutex
	got  string
	kind string
}

func (p *fakePortal) OpenFile(parent string, fd dbus.UnixFD, _ map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	f := os.NewFile(uintptr(fd), "portal-fd")
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(f)
	if err != nil {
		return "", dbus.MakeFailedError(err)
	}
	p.mu.Lock()
	p.got, p.kind = string(b), parent
	p.mu.Unlock()
	return "/org/freedesktop/portal/desktop/request/1_1/t", nil
}

func TestOpenFileSendsTheFileNotItsPath(t *testing.T) {
	sessionBus(t)
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	p := &fakePortal{}
	if err := conn.Export(p, object, "org.freedesktop.portal.OpenURI"); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(dest, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request name: %v %v", reply, err)
	}

	path := filepath.Join(t.TempDir(), "dashboard-launch-x.html")
	const content = "<meta http-equiv=refresh content='0;url=http://127.0.0.1:1/launch?t=secret'>"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := OpenFile(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.got != content {
		t.Fatalf("the portal read %q, want the launch file's contents", p.got)
	}
	if p.kind != "" {
		t.Fatalf("parent window %q, want none", p.kind)
	}
}

func TestOpenFileWithoutAPortal(t *testing.T) {
	sessionBus(t)
	path := filepath.Join(t.TempDir(), "f.html")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := OpenFile(context.Background(), path); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no portal on the bus: %v, want ErrUnavailable", err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "none"))
	if err := OpenFile(context.Background(), path); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no session bus: %v, want ErrUnavailable", err)
	}
}
