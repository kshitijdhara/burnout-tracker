//go:build linux

package wayland

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kshitijdhara/burntout/internal/adapters/collector/desktopbus"
	"github.com/kshitijdhara/burntout/internal/adapters/paths"
	"github.com/kshitijdhara/burntout/internal/adapters/procinfo"
	"github.com/kshitijdhara/burntout/internal/domain/model"
	"github.com/kshitijdhara/burntout/internal/domain/ports"
)

// redialAfter spaces out reconnects once the bridge has gone away (the
// screen locked, GNOME Shell restarted, the extension was disabled).
const redialAfter = 2 * time.Second

// Collector reads desktop state from the GNOME Shell extension. Snapshot
// is safe to call from any goroutine; calls are serialised.
type Collector struct {
	opts   Options
	verify func(*net.UnixConn) error

	mu        sync.Mutex
	c         *client // nil until the bridge answers or after it went away; redialled by Snapshot
	lastDial  time.Time
	dialErr   error // why the bridge has not answered since the start; nil once it has
	bus       desktopbus.Reader
	idle      bool // Mutter's idle monitor answered
	lastX     int
	lastY     int
	havePoint bool
}

var _ ports.Collector = (*Collector)(nil)

// New connects to the extension and says hello. It fails, with
// ErrNotRunning when nothing listens, so the caller can say why there is
// no desktop collector.
func New(ctx context.Context, opts Options) (*Collector, error) {
	return newCollector(ctx, opts, verifyShell, false)
}

// NewWaiting is New for the daemon, which runs for the whole session: a
// bridge that doesn't answer yet is not an error. The extension may be
// enabled after the daemon starts, GNOME disables it while the screen is
// locked, and the service may start before GNOME Shell. The Collector then
// starts disconnected, Snapshot redials at most every redialAfter, and
// Capabilities says what is missing until the bridge answers.
func NewWaiting(ctx context.Context, opts Options) (*Collector, error) {
	return newCollector(ctx, opts, verifyShell, true)
}

func newCollector(ctx context.Context, opts Options, verify func(*net.UnixConn) error, wait bool) (*Collector, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Dir == "" {
		d, err := paths.RuntimeDir()
		if err != nil {
			return nil, err
		}
		opts.Dir = d
	}
	c, err := dial(ctx, opts.Dir, opts.Titles, verify)
	if err != nil && !wait {
		return nil, err
	}
	col := &Collector{opts: opts, verify: verify, c: c, lastDial: opts.Now(), dialErr: err}
	_, col.idle = col.bus.MutterIdle(ctx)
	return col, nil
}

// Hello returns the extension's and GNOME Shell's versions from the
// handshake.
func (c *Collector) Hello() (extension, shell string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.c == nil {
		return "", ""
	}
	return c.c.hello.Extension, c.c.hello.Shell
}

// Capabilities implements ports.Collector.
func (c *Collector) Capabilities() ports.Capabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	caps := capabilities(c.opts, c.idle)
	if c.dialErr != nil {
		caps.Limitations = append([]string{notAnswering(c.dialErr)}, caps.Limitations...)
	}
	return caps
}

// notAnswering says why a waiting collector has recorded no windows yet,
// and the fix.
func notAnswering(err error) string {
	if errors.Is(err, ErrNotRunning) {
		return "the BurntOut GNOME Shell extension is not answering yet, so no windows are recorded; BurntOut keeps trying. " +
			"If it is not enabled, run `" + EnableCommand + "`"
	}
	return fmt.Sprintf("the GNOME Shell bridge failed (%v), so no windows are recorded; BurntOut keeps trying", err)
}

// Close disconnects from the extension and the D-Bus buses.
func (c *Collector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drop()
	return c.bus.Close()
}

func (c *Collector) drop() {
	if c.c != nil {
		_ = c.c.close()
		c.c = nil
	}
}

// Snapshot implements ports.Collector. Lock state is read first: while the
// screen is locked GNOME has disabled the extension, and that is not an
// error.
func (c *Collector) Snapshot(ctx context.Context) (model.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return model.Snapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	e := extra{at: c.opts.Now(), titles: c.opts.Titles}
	e.idleFor, c.idle = c.bus.MutterIdle(ctx)
	if e.locked = c.bus.Locked(ctx); e.locked {
		c.drop()
		c.havePoint = false
		return lockedSnapshot(e), nil
	}
	if c.c == nil {
		// Between redials the tick is skipped, as with no collector at
		// all: an empty or locked snapshot would record presence wrongly.
		now := c.opts.Now()
		if now.Sub(c.lastDial) < redialAfter {
			return model.Snapshot{}, ErrNotRunning
		}
		c.lastDial = now
		cl, err := dial(ctx, c.opts.Dir, c.opts.Titles, c.verify)
		if err != nil {
			return model.Snapshot{}, err
		}
		c.c, c.dialErr = cl, nil
	}
	r, err := c.c.snapshot(ctx)
	if err != nil {
		c.drop()
		return model.Snapshot{}, err
	}
	x, y := r.Pointer.X, r.Pointer.Y
	e.pointerMoved = c.havePoint && (x != c.lastX || y != c.lastY)
	c.lastX, c.lastY, c.havePoint = x, y, true
	return buildSnapshot(r, e), nil
}

// shellComm is GNOME Shell's process name, as /proc/<pid>/comm shows it.
const shellComm = "gnome-shell"

// verifyShell checks that the bridge socket is held by GNOME Shell running
// as this user, so nothing else that took the path gets the token.
func verifyShell(c *net.UnixConn) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var (
		cred    *unix.Ucred
		credErr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credErr != nil {
		return fmt.Errorf("gnome bridge: peer credentials: %w", credErr)
	}
	if int64(cred.Uid) != int64(os.Getuid()) {
		return errors.New("gnome bridge: the socket belongs to another user")
	}
	if name, err := (procinfo.Info{}).Name(int(cred.Pid)); err != nil || name != shellComm {
		return fmt.Errorf("gnome bridge: the socket is held by %q, not %s", name, shellComm)
	}
	return nil
}
