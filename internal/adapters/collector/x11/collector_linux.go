//go:build linux

package x11

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/screensaver"
	"github.com/jezek/xgb/xproto"

	"github.com/kshitijdhara/burntout/internal/adapters/collector/desktopbus"
	"github.com/kshitijdhara/burntout/internal/adapters/procinfo"
	"github.com/kshitijdhara/burntout/internal/domain/model"
	"github.com/kshitijdhara/burntout/internal/domain/ports"
)

// Property reads are capped, in 32-bit units: 4096 windows in the client
// list, 16 KiB for a title.
const (
	maxListLen = 4096
	maxPropLen = 4096
)

// atomNames are the EWMH atoms the collector reads, interned once per
// connection. WM_CLASS and WM_NAME are predefined.
var atomNames = []string{
	"_NET_CLIENT_LIST_STACKING", "_NET_ACTIVE_WINDOW", "_NET_CURRENT_DESKTOP",
	"_NET_WM_DESKTOP", "_NET_WM_STATE", "_NET_WM_STATE_HIDDEN", "_NET_WM_WINDOW_TYPE",
	typeDesktop, typeDock, "_NET_WM_PID", "_NET_FRAME_EXTENTS", "_NET_WM_NAME",
}

// quietXGB stops xgb logging to stderr, which is the daemon's crash log.
var quietXGB sync.Once

// Collector reads desktop state from the X server named by DISPLAY.
// Snapshot is safe to call from any goroutine; calls are serialised.
type Collector struct {
	opts Options

	mu        sync.Mutex
	x         *display // nil after the connection drops; reopened by the next Snapshot
	idle      bool     // the server has the MIT-SCREEN-SAVER extension
	bus       desktopbus.Reader
	apps      map[int]string // pid → comm for windows without WM_CLASS, pruned to live pids
	lastX     int
	lastY     int
	havePoint bool
}

var _ ports.Collector = (*Collector)(nil)

// display is one connection to the X server with what was learnt about it
// on connecting.
type display struct {
	conn   *xgb.Conn
	root   xproto.Window
	screen model.Rect
	atoms  map[string]xproto.Atom
	names  map[xproto.Atom]string // window-type atoms back to their names
	randr  bool                   // RandR 1.5 monitors are available
	saver  bool
}

// New connects to the X server. It fails if none is reachable, so the
// caller can say why there is no desktop collector.
func New(opts Options) (*Collector, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	quietXGB.Do(func() { xgb.Logger = log.New(io.Discard, "", 0) })
	x, err := connect()
	if err != nil {
		return nil, err
	}
	return &Collector{opts: opts, x: x, idle: x.saver, apps: map[int]string{}}, nil
}

func connect() (*display, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("x11: connect: %w", err)
	}
	scr := xproto.Setup(conn).DefaultScreen(conn)
	x := &display{
		conn:   conn,
		root:   scr.Root,
		screen: model.Rect{W: int(scr.WidthInPixels), H: int(scr.HeightInPixels)},
		atoms:  make(map[string]xproto.Atom, len(atomNames)),
		names:  map[xproto.Atom]string{},
	}
	cookies := make([]xproto.InternAtomCookie, len(atomNames))
	for i, n := range atomNames {
		cookies[i] = xproto.InternAtom(conn, false, uint16(len(n)), n) //nolint:gosec // G115: atom names are short constants
	}
	for i, ck := range cookies {
		r, err := ck.Reply()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("x11: intern %s: %w", atomNames[i], err)
		}
		x.atoms[atomNames[i]] = r.Atom
	}
	x.names[x.atoms[typeDesktop]] = typeDesktop
	x.names[x.atoms[typeDock]] = typeDock

	// Extensions are optional: without RandR 1.5 the whole screen is one
	// monitor, and without MIT-SCREEN-SAVER there is no idle time.
	if randr.Init(conn) == nil {
		if v, err := randr.QueryVersion(conn, 1, 5).Reply(); err == nil {
			x.randr = v.MajorVersion > 1 || v.MinorVersion >= 5
		}
	}
	x.saver = screensaver.Init(conn) == nil
	return x, nil
}

// Capabilities implements ports.Collector.
func (c *Collector) Capabilities() ports.Capabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	return capabilities(c.opts, c.idle)
}

// Close disconnects from the X server and the D-Bus buses.
func (c *Collector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.x != nil {
		c.x.conn.Close()
		c.x = nil
	}
	_ = c.bus.Close()
	return nil
}

// Snapshot implements ports.Collector.
func (c *Collector) Snapshot(ctx context.Context) (model.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return model.Snapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.x == nil {
		x, err := connect()
		if err != nil {
			return model.Snapshot{}, err
		}
		c.x, c.idle = x, x.saver
	}
	s := rawState{at: c.opts.Now()}
	err := c.read(&s)
	if errors.Is(err, io.EOF) {
		// The X server went away (logout, crash). Reconnect next time.
		c.x.conn.Close()
		c.x = nil
		return model.Snapshot{}, fmt.Errorf("x11: connection closed: %w", err)
	}
	if err != nil {
		return model.Snapshot{}, err
	}
	s.locked = c.bus.Locked(ctx)
	return buildSnapshot(s), nil
}

func (c *Collector) read(s *rawState) error {
	x := c.x
	stackCk := x.prop(x.root, x.atoms["_NET_CLIENT_LIST_STACKING"], maxListLen)
	activeCk := x.prop(x.root, x.atoms["_NET_ACTIVE_WINDOW"], 1)
	deskCk := x.prop(x.root, x.atoms["_NET_CURRENT_DESKTOP"], 1)
	pointerCk := xproto.QueryPointer(x.conn, x.root)

	stack, err := stackCk.Reply()
	if err != nil {
		return fmt.Errorf("x11: read client list: %w", err)
	}
	if active, err := activeCk.Reply(); err == nil {
		if v := cardinals(active.Value); len(v) > 0 {
			s.active = v[0]
		}
	}
	s.currentDesktop = noDesktop
	if desk, err := deskCk.Reply(); err == nil {
		if v := cardinals(desk.Value); len(v) > 0 {
			s.currentDesktop = int64(v[0])
		}
	}
	if err := c.readWindows(s, cardinals(stack.Value)); err != nil {
		return err
	}
	s.monitors = x.monitors()

	if p, err := pointerCk.Reply(); err == nil && p.SameScreen {
		x, y := int(p.RootX), int(p.RootY)
		s.pointerX, s.pointerY, s.havePointer = x, y, true
		s.pointerMoved = c.havePoint && (x != c.lastX || y != c.lastY)
		c.lastX, c.lastY, c.havePoint = x, y, true
	} else if errors.Is(err, io.EOF) {
		return err
	}
	if x.saver {
		if info, err := screensaver.QueryInfo(x.conn, xproto.Drawable(x.root)).Reply(); err == nil {
			s.idleFor = time.Duration(info.MsSinceUserInput) * time.Millisecond
		}
	}
	return nil
}

// windowCookies are the pipelined requests for one client window. Sending
// every request before reading any reply makes a snapshot a single round
// trip however many windows there are.
type windowCookies struct {
	attrs                                     xproto.GetWindowAttributesCookie
	geom                                      xproto.GetGeometryCookie
	origin                                    xproto.TranslateCoordinatesCookie
	pid, class, state, types, desk, frame, ti xproto.GetPropertyCookie
	legacyTitle                               xproto.GetPropertyCookie
}

func (c *Collector) readWindows(s *rawState, ids []uint32) error {
	x := c.x
	cookies := make([]windowCookies, len(ids))
	for i, id := range ids {
		w := xproto.Window(id)
		ck := &cookies[i]
		ck.attrs = xproto.GetWindowAttributes(x.conn, w)
		ck.geom = xproto.GetGeometry(x.conn, xproto.Drawable(w))
		ck.origin = xproto.TranslateCoordinates(x.conn, w, x.root, 0, 0)
		ck.pid = x.prop(w, x.atoms["_NET_WM_PID"], 1)
		ck.class = x.prop(w, xproto.AtomWmClass, maxPropLen)
		ck.state = x.prop(w, x.atoms["_NET_WM_STATE"], maxPropLen)
		ck.types = x.prop(w, x.atoms["_NET_WM_WINDOW_TYPE"], maxPropLen)
		ck.desk = x.prop(w, x.atoms["_NET_WM_DESKTOP"], 1)
		ck.frame = x.prop(w, x.atoms["_NET_FRAME_EXTENTS"], 4)
		if c.opts.Titles {
			ck.ti = x.prop(w, x.atoms["_NET_WM_NAME"], maxPropLen)
			ck.legacyTitle = x.prop(w, xproto.AtomWmName, maxPropLen)
		}
	}
	live := make(map[int]string, len(c.apps))
	s.stacking = make([]rawWindow, 0, len(ids))
	for i, id := range ids {
		w, err := c.window(id, &cookies[i], live)
		if errors.Is(err, io.EOF) {
			return err
		}
		if err != nil {
			continue // the window closed between the list read and now
		}
		s.stacking = append(s.stacking, w)
	}
	c.apps = live
	return nil
}

// window collects one window's replies. Every reply is read, even after an
// error, so none is left queued on the connection.
func (c *Collector) window(id uint32, ck *windowCookies, live map[int]string) (rawWindow, error) {
	x := c.x
	attrs, errA := ck.attrs.Reply()
	geom, errG := ck.geom.Reply()
	origin, errO := ck.origin.Reply()
	pid, _ := ck.pid.Reply()
	class, _ := ck.class.Reply()
	state, _ := ck.state.Reply()
	types, _ := ck.types.Reply()
	desk, _ := ck.desk.Reply()
	frame, _ := ck.frame.Reply()
	var title, legacy *xproto.GetPropertyReply
	if c.opts.Titles {
		title, _ = ck.ti.Reply()
		legacy, _ = ck.legacyTitle.Reply()
	}
	if err := errors.Join(errA, errG, errO); err != nil {
		return rawWindow{}, err
	}

	w := rawWindow{
		id:       id,
		bounds:   frameBounds(int(origin.DstX), int(origin.DstY), int(geom.Width), int(geom.Height), values(frame)),
		viewable: attrs.MapState == xproto.MapStateViewable,
		desktop:  noDesktop,
	}
	if v := values(pid); len(v) > 0 {
		w.pid = int(v[0])
	}
	if v := values(desk); len(v) > 0 {
		w.desktop = int64(v[0])
	}
	for _, a := range values(state) {
		if xproto.Atom(a) == x.atoms["_NET_WM_STATE_HIDDEN"] {
			w.hidden = true
		}
	}
	for _, a := range values(types) {
		if n, ok := x.names[xproto.Atom(a)]; ok {
			w.types = append(w.types, n)
		}
	}
	if class != nil {
		w.appID = appIDFromClass(class.Value)
	}
	if w.appID == "" && w.pid > 0 {
		w.appID = c.comm(w.pid, live)
	}
	if c.opts.Titles {
		t := title
		if t == nil || len(t.Value) == 0 {
			t = legacy
		}
		if t != nil {
			w.title = model.NewSensitive(string(t.Value))
		}
	}
	return w, nil
}

// comm looks a pid's process name up in the previous snapshot's cache,
// falling back to /proc. Only pids seen this snapshot are kept, so a
// recycled pid is re-read once its old window is gone.
func (c *Collector) comm(pid int, live map[int]string) string {
	if name, ok := live[pid]; ok {
		return name
	}
	name, ok := c.apps[pid]
	if !ok {
		name, _ = procinfo.Info{}.Name(pid)
	}
	live[pid] = name
	return name
}

func (x *display) prop(w xproto.Window, a xproto.Atom, maxLen uint32) xproto.GetPropertyCookie {
	return xproto.GetProperty(x.conn, false, w, a, xproto.GetPropertyTypeAny, 0, maxLen)
}

// values decodes a 32-bit property reply; a missing property or one of
// another format has no values.
func values(r *xproto.GetPropertyReply) []uint32 {
	if r == nil || r.Format != 32 {
		return nil
	}
	return cardinals(r.Value)
}

// monitors returns the active RandR monitors, or the whole screen when the
// server has no RandR 1.5.
func (x *display) monitors() []model.Rect {
	if x.randr {
		if r, err := randr.GetMonitors(x.conn, x.root, true).Reply(); err == nil && len(r.Monitors) > 0 {
			out := make([]model.Rect, len(r.Monitors))
			for i, m := range r.Monitors {
				out[i] = model.Rect{X: int(m.X), Y: int(m.Y), W: int(m.Width), H: int(m.Height)}
			}
			return out
		}
	}
	return []model.Rect{x.screen}
}
