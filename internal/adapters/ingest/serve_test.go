package ingest

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/kshitijdhara/burntout/internal/wire"
)

// flakyListener fails Accept with a temporary error a few times, then
// reports closed.
type flakyListener struct {
	failures atomic.Int32
	closed   chan struct{}
}

func (l *flakyListener) Accept() (net.Conn, error) {
	if l.failures.Add(-1) >= 0 {
		return nil, syscall.EMFILE
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *flakyListener) Close() error   { close(l.closed); return nil }
func (l *flakyListener) Addr() net.Addr { return &net.UnixAddr{} }

func TestServeRetriesTemporaryAcceptErrors(t *testing.T) {
	ln := &flakyListener{closed: make(chan struct{})}
	ln.failures.Store(3)
	srv := newServer(ln, func(net.Conn) (int, error) { return 1, nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, func(wire.Message) {}) }()

	deadline := time.Now().Add(2 * time.Second)
	for srv.Stats().AcceptErrors < 3 {
		if time.Now().After(deadline) {
			t.Fatal("Serve stopped retrying after a temporary accept error")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("Serve returned early: %v", err)
	default:
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Serve: %v", err)
	}
}

func TestNextBackoff(t *testing.T) {
	d := time.Duration(0)
	for range 20 {
		d = nextBackoff(d)
	}
	if d != acceptBackoffCap {
		t.Fatalf("backoff = %v, want cap %v", d, acceptBackoffCap)
	}
	if nextBackoff(0) != acceptBackoffFirst {
		t.Fatal("first backoff wrong")
	}
}
