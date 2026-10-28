//go:build linux

package portal

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// busConfig is a session bus with no service directories, so nothing (a
// real xdg-desktop-portal on the developer's machine) can be started on it.
const busConfig = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:path=%s</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

// sessionBus starts a private, isolated session bus for the test, or skips
// the test where dbus-daemon isn't installed.
func sessionBus(t *testing.T) {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon not installed")
	}
	// A short folder of its own: D-Bus addresses can't hold every character
	// a test name puts in t.TempDir, and socket paths are limited in length.
	dir, err := os.MkdirTemp("", "pb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	conf := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(conf, []byte(fmt.Sprintf(busConfig, filepath.Join(dir, "bus"))), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--config-file="+conf, "--nofork", "--print-address")
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

// fakePortal is org.freedesktop.portal.OpenURI: it reads the file it is
// given, then answers on the request object, as the real portal does.
type fakePortal struct {
	conn   *dbus.Conn
	answer *uint32 // nil: never answer

	mu     sync.Mutex
	got    string
	parent string
}

func (p *fakePortal) OpenFile(sender dbus.Sender, parent string, fd dbus.UnixFD, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	f := os.NewFile(uintptr(fd), "portal-fd")
	b, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		return "", dbus.MakeFailedError(err)
	}
	p.mu.Lock()
	p.got, p.parent = string(b), parent
	p.mu.Unlock()
	token, _ := opts["handle_token"].Value().(string)
	path := requestPath(string(sender), token)
	if p.answer != nil {
		code := *p.answer
		go func() {
			time.Sleep(20 * time.Millisecond) // after the reply, as the real portal does
			_ = p.conn.Emit(path, requestIface+".Response", code, map[string]dbus.Variant{})
		}()
	}
	return path, nil
}

func startPortal(t *testing.T, answer *uint32) *fakePortal {
	t.Helper()
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	p := &fakePortal{conn: conn, answer: answer}
	if err := conn.Export(p, object, "org.freedesktop.portal.OpenURI"); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(dest, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request name: %v %v", reply, err)
	}
	return p
}

func launchFile(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dashboard-launch-x.html")
	const content = "<meta http-equiv=refresh content='0;url=http://127.0.0.1:1/launch?t=secret'>"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, content
}

func code(c uint32) *uint32 { return &c }

func TestOpenFile(t *testing.T) {
	t.Run("opened: the portal reads the file, not its path", func(t *testing.T) {
		sessionBus(t)
		p := startPortal(t, code(0))
		path, content := launchFile(t)
		if err := OpenFile(context.Background(), path); err != nil {
			t.Fatal(err)
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.got != content || p.parent != "" {
			t.Fatalf("the portal read %q (parent %q), want the launch file's contents", p.got, p.parent)
		}
	})
	t.Run("the user cancelled the app chooser", func(t *testing.T) {
		sessionBus(t)
		startPortal(t, code(1))
		path, _ := launchFile(t)
		if err := OpenFile(context.Background(), path); !errors.Is(err, ErrCancelled) {
			t.Fatalf("got %v, want ErrCancelled", err)
		}
	})
	t.Run("the portal failed to open it", func(t *testing.T) {
		sessionBus(t)
		startPortal(t, code(2))
		path, _ := launchFile(t)
		err := OpenFile(context.Background(), path)
		if err == nil || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrCancelled) || errors.Is(err, ErrNoAnswer) {
			t.Fatalf("got %v, want a plain failure", err)
		}
	})
	t.Run("the portal never answers", func(t *testing.T) {
		sessionBus(t)
		startPortal(t, nil)
		old := answerTimeout
		answerTimeout = 200 * time.Millisecond
		t.Cleanup(func() { answerTimeout = old })
		path, _ := launchFile(t)
		if err := OpenFile(context.Background(), path); !errors.Is(err, ErrNoAnswer) {
			t.Fatalf("got %v, want ErrNoAnswer", err)
		}
	})
	t.Run("no portal on the bus", func(t *testing.T) {
		sessionBus(t)
		path, _ := launchFile(t)
		if err := OpenFile(context.Background(), path); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("got %v, want ErrUnavailable", err)
		}
	})
	t.Run("no session bus", func(t *testing.T) {
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "none"))
		path, _ := launchFile(t)
		if err := OpenFile(context.Background(), path); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("got %v, want ErrUnavailable", err)
		}
	})
}
