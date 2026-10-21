//go:build windows

package windows

import (
	"context"
	"fmt"
	"sync"
	"time"

	w32 "golang.org/x/sys/windows"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

// Collector reads desktop state through Win32. Snapshot is safe to call
// from any goroutine; calls are serialised.
type Collector struct {
	opts Options

	mu        sync.Mutex
	enumWinCB uintptr // EnumWindows callback, created once (Go callbacks are never freed)
	enumMonCB uintptr // EnumDisplayMonitors callback
	handles   []w32.HWND
	monitors  []model.Rect
	apps      map[uint32]string // pid → exe name, pruned to live pids each snapshot
	lastX     int
	lastY     int
	havePoint bool
	input     *inputCounter // nil unless input counting is on and started
}

var _ ports.Collector = (*Collector)(nil)

// New creates a collector. If input counting is requested but Raw Input
// can't be registered (for example in a non-interactive session), the
// collector still works and reports InputCounts as unavailable.
func New(opts Options) (*Collector, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	enablePerMonitorDPI()
	c := &Collector{opts: opts, apps: map[uint32]string{}}
	c.enumWinCB = w32.NewCallback(func(h w32.HWND, _ uintptr) uintptr {
		c.handles = append(c.handles, h)
		return 1 // continue enumeration
	})
	c.enumMonCB = w32.NewCallback(func(_ uintptr, _ uintptr, r *w32.Rect, _ uintptr) uintptr {
		c.monitors = append(c.monitors, rectFromLTRB(r.Left, r.Top, r.Right, r.Bottom))
		return 1
	})
	if opts.InputCounts {
		if in, err := startInputCounter(); err == nil {
			c.input = in
		}
	}
	return c, nil
}

// Capabilities implements ports.Collector.
func (c *Collector) Capabilities() ports.Capabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	return capabilities(c.opts, c.input != nil)
}

// Close stops input counting, if running.
func (c *Collector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.input != nil {
		c.input.close()
		c.input = nil
	}
	return nil
}

// Snapshot implements ports.Collector.
func (c *Collector) Snapshot(ctx context.Context) (model.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return model.Snapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	s := rawState{at: c.opts.Now()}
	if err := c.readWindows(&s); err != nil {
		return model.Snapshot{}, err
	}
	if err := c.readMonitors(&s); err != nil {
		return model.Snapshot{}, err
	}
	c.readPointer(&s)
	c.readIdleAndLock(&s)
	c.readInputCounts(&s)
	return buildSnapshot(s), nil
}

func (c *Collector) readWindows(s *rawState) error {
	c.handles = c.handles[:0]
	if err := w32.EnumWindows(c.enumWinCB, nil); err != nil {
		return fmt.Errorf("windows: enumerate windows: %w", err)
	}
	shell := w32.GetShellWindow()
	live := make(map[uint32]string, len(c.apps))
	for _, h := range c.handles {
		if h == shell || !isCandidate(h) {
			continue
		}
		r, ok := windowBounds(h)
		if !ok {
			continue
		}
		bounds := rectFromLTRB(r.Left, r.Top, r.Right, r.Bottom)
		if bounds.Area() == 0 {
			continue
		}
		var pid uint32
		_, _ = w32.GetWindowThreadProcessId(h, &pid)
		w := rawWindow{
			handle: uint64(h),
			pid:    int(pid),
			appID:  c.appID(pid, live),
			bounds: bounds,
		}
		if c.opts.Titles {
			w.title = model.NewSensitive(windowText(h))
		}
		s.windows = append(s.windows, w)
	}
	c.apps = live
	s.foreground = uint64(rootOf(w32.GetForegroundWindow()))
	return nil
}

// isCandidate keeps visible, non-minimised, non-cloaked, unowned top-level
// windows that would appear in the taskbar (tool windows are skipped unless
// they explicitly opt in with WS_EX_APPWINDOW).
func isCandidate(h w32.HWND) bool {
	if !w32.IsWindowVisible(h) || isIconic(h) || owner(h) != 0 {
		return false
	}
	ex := exStyle(h)
	if ex&wsExToolWindow != 0 && ex&wsExAppWindow == 0 {
		return false
	}
	return !isCloaked(h)
}

// appID looks pid up in the previous snapshot's cache, falling back to a
// process query. Only pids seen this snapshot are kept, so a recycled pid
// is re-queried once its old window is gone.
func (c *Collector) appID(pid uint32, live map[uint32]string) string {
	if name, ok := live[pid]; ok {
		return name
	}
	name, ok := c.apps[pid]
	if !ok {
		name = exeName(pid)
	}
	live[pid] = name
	return name
}

func (c *Collector) readMonitors(s *rawState) error {
	c.monitors = c.monitors[:0]
	r, _, err := procEnumDisplayMonitors.Call(0, 0, c.enumMonCB, 0)
	if r == 0 {
		return fmt.Errorf("windows: enumerate monitors: %w", err)
	}
	s.monitors = append([]model.Rect(nil), c.monitors...)
	return nil
}

func (c *Collector) readPointer(s *rawState) {
	x, y, ok := cursorPos()
	if !ok {
		return
	}
	s.pointerMoved = c.havePoint && (x != c.lastX || y != c.lastY)
	c.lastX, c.lastY, c.havePoint = x, y, true
	s.pointerRoot = uint64(rootOf(windowFromPoint(x, y)))
}

func (c *Collector) readIdleAndLock(s *rawState) {
	if last, ok := lastInputTick(); ok {
		s.idleFor = idleFor(tickCount64(), last)
	}
	var fgApp string
	var pid uint32
	if fg := w32.HWND(s.foreground); fg != 0 {
		_, _ = w32.GetWindowThreadProcessId(fg, &pid)
		fgApp = c.apps[pid]
		if fgApp == "" {
			fgApp = exeName(pid)
		}
	}
	s.locked = isLocked(inputDesktopAvailable(), fgApp)
}

func (c *Collector) readInputCounts(s *rawState) {
	if c.input == nil {
		return
	}
	s.inputCountsOn = true
	s.keys, s.scrolls = c.input.drain()
}
