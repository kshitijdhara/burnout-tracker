//go:build unix

package ingest

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/wire"
)

// shortSocketPath keeps the path under the ~104-byte sun_path limit on macOS,
// where t.TempDir paths are long.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "run", socketName)
}

type collector struct {
	mu   sync.Mutex
	msgs []wire.Message
}

func (c *collector) handle(m wire.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
}

func (c *collector) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

func startServer(t *testing.T, path string) (*Server, *collector) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := listenAt(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	col := &collector{}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, col.handle) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return srv, col
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func shellMsg() wire.Message {
	return wire.Message{V: wire.Version, Kind: wire.KindShellCommand, SentMs: 2, Shell: &wire.ShellCommand{
		Argv0: "git", Subcmd: "status", StartMs: 1, EndMs: 2, Cwd: "/tmp",
	}}
}

func rawSend(t *testing.T, path, payload string) {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte(payload))
	_ = c.(*net.UnixConn).CloseWrite()
	_ = c.Close()
}

func TestRoundTrip(t *testing.T) {
	path := shortSocketPath(t)
	srv, col := startServer(t, path)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := sendTo(ctx, path, shellMsg()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return col.len() == 1 })
	if got := col.msgs[0].Shell.Argv0; got != "git" {
		t.Fatalf("argv0 = %q", got)
	}
	if s := srv.Stats(); s.Accepted != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestRejectsBadInput(t *testing.T) {
	path := shortSocketPath(t)
	srv, col := startServer(t, path)

	rawSend(t, path, `{"v":1,"pad":"`+strings.Repeat("a", wire.MaxMessageBytes)+`"}`)
	rawSend(t, path, `not json`)
	rawSend(t, path, `{"v":9,"kind":"shell.cmd"}`)
	rawSend(t, path, `{"v":1,"kind":"shell.cmd","shell":{"argv0":"has space","start_ms":1,"end_ms":1}}`)

	waitFor(t, func() bool {
		s := srv.Stats()
		return s.TooLarge+s.Malformed+s.BadVersion+s.InvalidData == 4
	})
	s := srv.Stats()
	if s.TooLarge != 1 || s.Malformed != 1 || s.BadVersion != 1 || s.InvalidData != 1 {
		t.Fatalf("stats = %+v", s)
	}
	if col.len() != 0 {
		t.Fatalf("handler called for bad input: %+v", col.msgs)
	}
}

func TestSocketIsPrivate(t *testing.T) {
	path := shortSocketPath(t)
	startServer(t, path)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket mode = %v", perm)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Fatalf("runtime dir mode = %v", perm)
	}
}

func TestAlreadyRunning(t *testing.T) {
	path := shortSocketPath(t)
	startServer(t, path)
	if _, err := listenAt(context.Background(), path); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("got %v, want ErrAlreadyRunning", err)
	}
}

func TestStaleSocketRemoved(t *testing.T) {
	path := shortSocketPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// A listener that is closed without unlinking leaves a dead socket file.
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()

	srv, err := listenAt(context.Background(), path)
	if err != nil {
		t.Fatalf("stale socket not cleared: %v", err)
	}
	_ = srv.Close()
}

func TestRefusesNonSocket(t *testing.T) {
	path := shortSocketPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenAt(context.Background(), path); err == nil {
		t.Fatal("listened over a regular file")
	}
	if b, _ := os.ReadFile(path); string(b) != "keep me" {
		t.Fatal("regular file was modified")
	}
}

func TestSameUser(t *testing.T) {
	if err := sameUser(1000, 1000); err != nil {
		t.Fatal(err)
	}
	if err := sameUser(0, 1000); !errors.Is(err, errPeerRejected) {
		t.Fatalf("got %v", err)
	}
	if err := sameUser(0, -1); !errors.Is(err, errPeerRejected) {
		t.Fatalf("got %v", err)
	}
}

func TestSendFailsFastWithoutDaemon(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := sendTo(ctx, shortSocketPath(t), shellMsg()); err == nil {
		t.Fatal("expected an error with no daemon")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("send took %v", d)
	}
}

func TestPingIsCountedAsPing(t *testing.T) {
	path := shortSocketPath(t)
	srv, col := startServer(t, path)
	ping := wire.Message{V: wire.Version, Kind: wire.KindPing, SentMs: 1}
	if err := sendTo(context.Background(), path, ping); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return srv.Stats().Pings == 1 })
	if st := srv.Stats(); st.Malformed != 0 || st.Accepted != 0 || col.len() != 0 {
		t.Fatalf("ping must not reach the handler or count as an error: %+v", st)
	}
}

func TestCheckPeerReportsPID(t *testing.T) {
	path := shortSocketPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := net.Dial("unix", path)
		if err == nil {
			time.Sleep(50 * time.Millisecond)
			_ = c.Close()
		}
	}()
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	pid, err := checkPeer(c)
	if err != nil {
		t.Fatal(err)
	}
	if pid != os.Getpid() {
		t.Fatalf("peer pid = %d, want %d", pid, os.Getpid())
	}
}
