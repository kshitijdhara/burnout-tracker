// Package ingest is the local transport between hook processes and the
// daemon: one wire.Message per connection over a Unix socket (macOS, Linux)
// or a named pipe (Windows). Only the same OS user can connect, every
// message is size-capped and validated, and bursts are rate-limited both
// globally and per source process.
// See docs/system-design.md §9 and docs/privacy-and-security.md §8.1.
package ingest

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kshitijdhara/burntout/internal/wire"
)

// ErrAlreadyRunning means another daemon is already listening on the address.
var ErrAlreadyRunning = errors.New("ingest: another daemon is already listening")

const (
	readTimeout    = time.Second
	maxConcurrent  = 64
	ratePerSecond  = 200
	rateBurst      = 1000
	staleDialLimit = 200 * time.Millisecond

	// Accept back-off after temporary errors (for example EMFILE).
	acceptBackoffFirst = 5 * time.Millisecond
	acceptBackoffCap   = time.Second
)

// Handler receives each valid message. It may be called from several
// goroutines at once and should hand the message off quickly.
type Handler func(wire.Message)

// Stats counts what the server accepted and why it dropped the rest.
// Values are cumulative since Listen.
type Stats struct {
	Accepted      uint64
	Pings         uint64 // liveness checks from doctor; not errors
	TooLarge      uint64
	BadVersion    uint64
	Malformed     uint64
	InvalidData   uint64
	PeerRejected  uint64
	RateLimited   uint64 // dropped by the global limit
	SourceLimited uint64 // dropped by a source's own rate limit
	SourceBusy    uint64 // dropped because a source had too many open connections
	AcceptErrors  uint64 // temporary accept failures, retried with back-off
}

type counters struct {
	accepted, pings, tooLarge, badVersion, malformed, invalidData      atomic.Uint64
	peerRejected, rateLimited, sourceLimited, sourceBusy, acceptErrors atomic.Uint64
}

// peerFunc verifies a connection and identifies its source process.
type peerFunc func(net.Conn) (pid int, err error)

// Server accepts hook connections. Create one with Listen.
type Server struct {
	ln       net.Listener
	peer     peerFunc
	limiter  *tokenBucket
	sources  *sourceLimiter
	stats    counters
	sem      chan struct{}
	closeErr error
	once     sync.Once
	wg       sync.WaitGroup
}

func newServer(ln net.Listener, peer peerFunc) *Server {
	return &Server{
		ln:      ln,
		peer:    peer,
		limiter: newTokenBucket(ratePerSecond, rateBurst, time.Now),
		sources: newSourceLimiter(time.Now),
		sem:     make(chan struct{}, maxConcurrent),
	}
}

// Serve accepts connections until ctx is cancelled or Close is called, then
// waits for in-flight connections to finish. It returns nil on a clean stop.
// Temporary accept errors are counted and retried with back-off, so a burst
// that exhausts file descriptors never stops ingest for good.
func (s *Server) Serve(ctx context.Context, h Handler) error {
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	defer s.wg.Wait()

	backoff := time.Duration(0)
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			s.stats.acceptErrors.Add(1)
			backoff = nextBackoff(backoff)
			if !sleepCtx(ctx, backoff) {
				return nil
			}
			continue
		}
		backoff = 0
		s.sem <- struct{}{}
		s.wg.Add(1)
		go func() {
			defer func() { <-s.sem; s.wg.Done() }()
			s.handle(conn, h)
		}()
	}
}

func nextBackoff(d time.Duration) time.Duration {
	if d == 0 {
		return acceptBackoffFirst
	}
	return min(2*d, acceptBackoffCap)
}

// sleepCtx sleeps for d, returning false if ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (s *Server) handle(conn net.Conn, h Handler) {
	defer func() { _ = conn.Close() }()
	pid, err := s.peer(conn)
	if err != nil {
		s.stats.peerRejected.Add(1)
		return
	}
	if !s.limiter.allow() {
		s.stats.rateLimited.Add(1)
		return
	}
	switch s.sources.admit(pid) {
	case sourceRateLimited:
		s.stats.sourceLimited.Add(1)
		return
	case sourceBusy:
		s.stats.sourceBusy.Add(1)
		return
	}
	defer s.sources.release(pid)

	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	m, err := wire.Decode(conn)
	if err != nil {
		s.count(err)
		return
	}
	if m.Kind == wire.KindPing {
		s.stats.pings.Add(1)
		return
	}
	s.stats.accepted.Add(1)
	h(m)
}

func (s *Server) count(err error) {
	switch {
	case errors.Is(err, wire.ErrTooLarge):
		s.stats.tooLarge.Add(1)
	case errors.Is(err, wire.ErrBadVersion):
		s.stats.badVersion.Add(1)
	case errors.Is(err, wire.ErrInvalidData):
		s.stats.invalidData.Add(1)
	default:
		s.stats.malformed.Add(1)
	}
}

// Stats returns a snapshot of the counters.
func (s *Server) Stats() Stats {
	return Stats{
		Accepted:      s.stats.accepted.Load(),
		Pings:         s.stats.pings.Load(),
		TooLarge:      s.stats.tooLarge.Load(),
		BadVersion:    s.stats.badVersion.Load(),
		Malformed:     s.stats.malformed.Load(),
		InvalidData:   s.stats.invalidData.Load(),
		PeerRejected:  s.stats.peerRejected.Load(),
		RateLimited:   s.stats.rateLimited.Load(),
		SourceLimited: s.stats.sourceLimited.Load(),
		SourceBusy:    s.stats.sourceBusy.Load(),
		AcceptErrors:  s.stats.acceptErrors.Load(),
	}
}

// Close stops accepting connections and removes the socket. It is safe to
// call more than once.
func (s *Server) Close() error {
	s.once.Do(func() { s.closeErr = s.ln.Close() })
	return s.closeErr
}

// Send delivers one message to the daemon. Callers bound it with a short
// context deadline (hooks use 50 ms) and ignore the error: hooks fail open.
func Send(ctx context.Context, m wire.Message) error {
	addr, err := Address()
	if err != nil {
		return err
	}
	if err := checkAddress(addr); err != nil {
		return err
	}
	return sendTo(ctx, addr, m)
}

func sendTo(ctx context.Context, addr string, m wire.Message) error {
	b, err := wire.Encode(m)
	if err != nil {
		return err
	}
	conn, err := dial(ctx, addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if _, err := conn.Write(b); err != nil {
		return err
	}
	return closeWrite(conn)
}

// closeWrite signals end-of-message where the transport supports half-close,
// so the server's read returns promptly.
func closeWrite(conn net.Conn) error {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		if err := cw.CloseWrite(); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
	}
	return nil
}

// Ping reports whether a daemon is listening by sending a ping message,
// which the daemon counts as a ping rather than an error.
func Ping(ctx context.Context) error {
	return Send(ctx, wire.Message{V: wire.Version, Kind: wire.KindPing, SentMs: time.Now().UnixMilli()})
}
