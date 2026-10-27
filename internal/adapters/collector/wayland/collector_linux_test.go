//go:build linux

package wayland

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// TestCollectorAgainstFakeExtension runs the collector against a fake
// extension. It needs the screen to read as unlocked, which holds on a CI
// runner (no logind session, no GNOME screensaver).
func TestCollectorAgainstFakeExtension(t *testing.T) {
	dir := shortDir(t)
	n := 0
	fakeExtension(t, dir, func(req request) string {
		if req.Op == "hello" {
			return helloReply
		}
		n++
		return fmt.Sprintf(`{"v":1,"op":"snapshot","windows":[{"id":7,"pid":1,"wm_class":"firefox","app":"","type":"normal","minimised":false,"on_workspace":true,"frame":{"x":0,"y":0,"w":100,"h":100},"title":"secret"}],"monitors":[{"x":0,"y":0,"w":100,"h":100}],"focus":7,"pointer":{"x":%d,"y":10}}`, n)
	})
	at := time.Unix(100, 0)
	c, err := newCollector(context.Background(), Options{Dir: dir, Now: func() time.Time { return at }}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if ext, shell := c.Hello(); ext != "1" || shell != "48.2" {
		t.Errorf("hello = %q, %q", ext, shell)
	}
	if c.bus.Locked(context.Background()) {
		t.Skip("this machine reports a locked session")
	}

	first, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !first.At.Equal(at) || len(first.Windows) != 1 || first.PointerWindow != 7 || first.PointerMoved {
		t.Fatalf("first = %+v", first)
	}
	if title := first.Windows[0].Title.Reveal(); title != "" {
		t.Errorf("title kept with titles off: %q", title)
	}
	second, err := c.Snapshot(context.Background())
	if err != nil || !second.PointerMoved {
		t.Fatalf("second: %+v, %v", second, err)
	}
}

func TestCollectorRedialsAfterTheBridgeGoes(t *testing.T) {
	dir := shortDir(t)
	fakeExtension(t, dir, standard)
	c, err := newCollector(context.Background(), Options{Dir: dir}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if c.bus.Locked(context.Background()) {
		t.Skip("this machine reports a locked session")
	}
	// The extension went away (disabled, GNOME Shell restarted).
	_ = c.c.conn.(*net.UnixConn).CloseRead()
	if _, err := c.Snapshot(context.Background()); err == nil {
		t.Fatal("snapshot on a dead connection succeeded")
	}
	// Redials are spaced out.
	if _, err := c.Snapshot(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("immediate redial: %v", err)
	}
	c.lastDial = time.Now().Add(-redialAfter)
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("redial: %v", err)
	}
}

func TestNewNotRunning(t *testing.T) {
	if _, err := newCollector(context.Background(), Options{Dir: shortDir(t)}, nil, false); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("no extension: %v", err)
	}
}

// The daemon's collector starts before the extension answers (enabled
// after login, the screen locked, GNOME Shell still starting) and picks it
// up once it does, without a restart.
func TestWaitingCollectorDialsLater(t *testing.T) {
	dir := shortDir(t)
	now := time.Unix(100, 0)
	c, err := newCollector(context.Background(), Options{Dir: dir, Now: func() time.Time { return now }}, nil, true)
	if err != nil {
		t.Fatalf("no extension yet: %v", err)
	}
	defer func() { _ = c.Close() }()
	if !strings.Contains(strings.Join(c.Capabilities().Limitations, "\n"), EnableCommand) {
		t.Errorf("no enable hint while waiting: %v", c.Capabilities().Limitations)
	}
	if c.bus.Locked(context.Background()) {
		t.Skip("this machine reports a locked session")
	}
	if _, err := c.Snapshot(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("snapshot with no extension: %v", err)
	}

	seen := fakeExtension(t, dir, standard)
	now = now.Add(redialAfter - time.Millisecond)
	if _, err := c.Snapshot(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("redial before the interval: %v", err)
	}
	if n := len(seen()); n != 0 {
		t.Fatalf("redialled early: %d requests", n)
	}
	now = now.Add(time.Millisecond)
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("redial: %v", err)
	}
	if !snap.At.Equal(now) || snap.Locked {
		t.Errorf("snapshot = %+v", snap)
	}
	if ext, _ := c.Hello(); ext != "1" {
		t.Errorf("hello after redial = %q", ext)
	}
	for _, l := range c.Capabilities().Limitations {
		if strings.Contains(l, EnableCommand) {
			t.Errorf("still says it is waiting: %q", l)
		}
	}
}
