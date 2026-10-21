//go:build darwin && cgo

package darwin

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework CoreGraphics -framework AppKit -framework Foundation -framework ApplicationServices

#include <stdint.h>
#include <string.h>
#include <stdatomic.h>
#include <pthread.h>
#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#include <CoreGraphics/CoreGraphics.h>
#include <ApplicationServices/ApplicationServices.h>

// Plain C records filled by the functions below and copied into Go. They
// hold no pointers, so Go-allocated arrays of them may be passed to C.
typedef struct {
	uint32_t number;
	int32_t  pid;
	int32_t  layer;
	double   x, y, w, h;
	char     owner[128];
	char     title[256]; // filled only when titles are requested
} bt_window;

typedef struct {
	double x, y, w, h;
} bt_rect;

// bt_copy_str copies a CFString into out as UTF-8, truncating if needed.
// Go repairs a multi-byte sequence cut at the end.
static void bt_copy_str(CFTypeRef s, char *out, size_t max) {
	out[0] = '\0';
	if (s == NULL || CFGetTypeID(s) != CFStringGetTypeID()) {
		return;
	}
	if (CFStringGetCString((CFStringRef)s, out, (CFIndex)max, kCFStringEncodingUTF8)) {
		return;
	}
	const char *u = [(__bridge NSString *)s UTF8String];
	if (u != NULL) {
		strlcpy(out, u, max);
	} else {
		out[0] = '\0';
	}
}

static int bt_get_i64(CFDictionaryRef d, CFStringRef key, int64_t *out) {
	CFTypeRef v = CFDictionaryGetValue(d, key);
	if (v == NULL || CFGetTypeID(v) != CFNumberGetTypeID()) {
		return 0;
	}
	return CFNumberGetValue((CFNumberRef)v, kCFNumberSInt64Type, out) ? 1 : 0;
}

// bt_list_windows copies on-screen layer-0 windows, front to back.
static int bt_list_windows(bt_window *out, int max, int want_titles) {
	@autoreleasepool {
		CFArrayRef list = CGWindowListCopyWindowInfo(
			kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements, kCGNullWindowID);
		if (list == NULL) {
			return 0;
		}
		int count = 0;
		CFIndex n = CFArrayGetCount(list);
		for (CFIndex i = 0; i < n && count < max; i++) {
			CFDictionaryRef d = (CFDictionaryRef)CFArrayGetValueAtIndex(list, i);
			if (d == NULL || CFGetTypeID(d) != CFDictionaryGetTypeID()) {
				continue;
			}
			int64_t layer = 0, number = 0, pid = 0;
			if (!bt_get_i64(d, kCGWindowLayer, &layer) || layer != 0) {
				continue;
			}
			if (!bt_get_i64(d, kCGWindowNumber, &number) || !bt_get_i64(d, kCGWindowOwnerPID, &pid)) {
				continue;
			}
			CFTypeRef bounds = CFDictionaryGetValue(d, kCGWindowBounds);
			CGRect r;
			if (bounds == NULL || CFGetTypeID(bounds) != CFDictionaryGetTypeID() ||
				!CGRectMakeWithDictionaryRepresentation((CFDictionaryRef)bounds, &r)) {
				continue;
			}
			bt_window *w = &out[count];
			memset(w, 0, sizeof *w);
			w->number = (uint32_t)number;
			w->pid = (int32_t)pid;
			w->layer = (int32_t)layer;
			w->x = r.origin.x;
			w->y = r.origin.y;
			w->w = r.size.width;
			w->h = r.size.height;
			bt_copy_str(CFDictionaryGetValue(d, kCGWindowOwnerName), w->owner, sizeof w->owner);
			if (want_titles) {
				bt_copy_str(CFDictionaryGetValue(d, kCGWindowName), w->title, sizeof w->title);
			}
			count++;
		}
		CFRelease(list);
		return count;
	}
}

// bt_list_displays copies active display bounds in global coordinates
// (top-left origin, the same space as kCGWindowBounds).
static int bt_list_displays(bt_rect *out, int max) {
	CGDirectDisplayID ids[32];
	uint32_t n = 0;
	if (CGGetActiveDisplayList(32, ids, &n) != kCGErrorSuccess) {
		return 0;
	}
	int count = 0;
	for (uint32_t i = 0; i < n && count < max; i++) {
		CGRect r = CGDisplayBounds(ids[i]);
		out[count].x = r.origin.x;
		out[count].y = r.origin.y;
		out[count].w = r.size.width;
		out[count].h = r.size.height;
		count++;
	}
	return count;
}

// bt_bundle_id writes pid's bundle identifier into out; returns 0 if none.
// Reading bundleIdentifier off the main thread is safe: it is fixed for the
// life of the process and doesn't depend on the main run loop (unlike
// NSWorkspace.frontmostApplication, which this collector no longer uses).
static int bt_bundle_id(int32_t pid, char *out, int max) {
	out[0] = '\0';
	@autoreleasepool {
		NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:(pid_t)pid];
		NSString *b = app.bundleIdentifier;
		if (b == nil) {
			return 0;
		}
		const char *u = b.UTF8String;
		if (u == NULL) {
			return 0;
		}
		strlcpy(out, u, (size_t)max);
		return 1;
	}
}

// bt_pointer reads the pointer location without any permission.
static int bt_pointer(double *x, double *y) {
	CGEventRef e = CGEventCreate(NULL);
	if (e == NULL) {
		return 0;
	}
	CGPoint p = CGEventGetLocation(e);
	CFRelease(e);
	*x = p.x;
	*y = p.y;
	return 1;
}

static double bt_idle_seconds(void) {
	return CGEventSourceSecondsSinceLastEventType(kCGEventSourceStateHIDSystemState, kCGAnyInputEventType);
}

static int bt_flag(CFDictionaryRef d, CFStringRef key, int *out) {
	CFTypeRef v = CFDictionaryGetValue(d, key);
	if (v == NULL) {
		return 0;
	}
	if (CFGetTypeID(v) == CFBooleanGetTypeID()) {
		*out = CFBooleanGetValue((CFBooleanRef)v) ? 1 : 0;
		return 1;
	}
	if (CFGetTypeID(v) == CFNumberGetTypeID()) {
		int64_t n = 0;
		if (CFNumberGetValue((CFNumberRef)v, kCFNumberSInt64Type, &n)) {
			*out = n != 0;
			return 1;
		}
	}
	return 0;
}

// bt_screen_locked reports a locked screen or a session that is not on the
// console (fast user switching). No session at all also counts as locked.
static int bt_screen_locked(void) {
	CFDictionaryRef d = CGSessionCopyCurrentDictionary();
	if (d == NULL) {
		return 1;
	}
	int locked = 0, v = 0;
	if (bt_flag(d, kCGSessionOnConsoleKey, &v) && !v) {
		locked = 1;
	}
	if (bt_flag(d, CFSTR("CGSSessionScreenIsLocked"), &v) && v) {
		locked = 1;
	}
	CFRelease(d);
	return locked;
}

static int bt_preflight_screen_capture(void) {
	return CGPreflightScreenCaptureAccess() ? 1 : 0;
}

// App Nap would throttle our sampling timer; a user-initiated activity
// keeps ticks on time while still allowing idle system sleep.
static void *bt_begin_activity(void) {
	@autoreleasepool {
		id a = [[NSProcessInfo processInfo]
			beginActivityWithOptions:NSActivityUserInitiatedAllowingIdleSystemSleep
			                  reason:@"burnout: sample desktop activity"];
		return (void *)CFBridgingRetain(a);
	}
}

static void bt_end_activity(void *token) {
	if (token == NULL) {
		return;
	}
	@autoreleasepool {
		id a = CFBridgingRelease(token);
		[[NSProcessInfo processInfo] endActivity:a];
	}
}

// Input counting. The tap is process-wide: one Collector per process.
static _Atomic uint64_t bt_keys;
static _Atomic uint64_t bt_scrolls;
static _Atomic int bt_tap_stopping;
static CFMachPortRef bt_tap;
static pthread_t bt_tap_thread;

static CGEventRef bt_tap_cb(CGEventTapProxy proxy, CGEventType type, CGEventRef event, void *info) {
	(void)proxy;
	(void)info;
	// Count events by type only. Never read key codes, characters or
	// modifiers from the event: that is the line between counting activity
	// and keylogging, and we don't cross it.
	switch (type) {
	case kCGEventKeyDown:
		atomic_fetch_add_explicit(&bt_keys, 1, memory_order_relaxed);
		break;
	case kCGEventScrollWheel:
	case kCGEventLeftMouseDown:
	case kCGEventRightMouseDown:
	case kCGEventOtherMouseDown:
		atomic_fetch_add_explicit(&bt_scrolls, 1, memory_order_relaxed);
		break;
	case kCGEventTapDisabledByTimeout:
	case kCGEventTapDisabledByUserInput:
		if (bt_tap != NULL) {
			CGEventTapEnable(bt_tap, true);
		}
		break;
	default:
		break;
	}
	return event;
}

static void *bt_tap_main(void *arg) {
	(void)arg;
	CFRunLoopSourceRef src = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, bt_tap, 0);
	if (src == NULL) {
		return NULL;
	}
	CFRunLoopRef loop = CFRunLoopGetCurrent();
	CFRunLoopAddSource(loop, src, kCFRunLoopCommonModes);
	CGEventTapEnable(bt_tap, true);
	// Poll the stop flag between short run-loop slices: there is no race
	// between "stop requested" and "loop started", and stopping waits at
	// most one slice.
	while (!atomic_load(&bt_tap_stopping)) {
		CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.25, false);
	}
	CFRunLoopRemoveSource(loop, src, kCFRunLoopCommonModes);
	CFRelease(src);
	return NULL;
}

// bt_tap_start returns 1 when the tap runs, 0 when macOS refused it
// (no Input Monitoring permission).
static int bt_tap_start(void) {
	if (bt_tap != NULL) {
		return 1;
	}
	CGEventMask mask = CGEventMaskBit(kCGEventKeyDown) | CGEventMaskBit(kCGEventScrollWheel) |
		CGEventMaskBit(kCGEventLeftMouseDown) | CGEventMaskBit(kCGEventRightMouseDown) |
		CGEventMaskBit(kCGEventOtherMouseDown);
	CFMachPortRef tap = CGEventTapCreate(kCGSessionEventTap, kCGTailAppendEventTap,
		kCGEventTapOptionListenOnly, mask, bt_tap_cb, NULL);
	if (tap == NULL) {
		return 0;
	}
	bt_tap = tap;
	atomic_store(&bt_tap_stopping, 0);
	if (pthread_create(&bt_tap_thread, NULL, bt_tap_main, NULL) != 0) {
		CFMachPortInvalidate(bt_tap);
		CFRelease(bt_tap);
		bt_tap = NULL;
		return 0;
	}
	return 1;
}

static void bt_tap_stop(void) {
	if (bt_tap == NULL) {
		return;
	}
	CGEventTapEnable(bt_tap, false);
	atomic_store(&bt_tap_stopping, 1);
	pthread_join(bt_tap_thread, NULL);
	CFMachPortInvalidate(bt_tap);
	CFRelease(bt_tap);
	bt_tap = NULL;
}

static void bt_tap_counts(uint64_t *keys, uint64_t *scrolls) {
	*keys = atomic_load_explicit(&bt_keys, memory_order_relaxed);
	*scrolls = atomic_load_explicit(&bt_scrolls, memory_order_relaxed);
}
*/
import "C"

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"
	"unsafe"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/ports"
)

// Buffer sizes for one tick. Windows beyond maxWindows (far past the
// 50-window stress case) are the furthest back in z-order and are dropped.
const (
	maxWindows  = 512
	maxDisplays = 32
)

var errClosed = errors.New("darwin: collector is closed")

// Collector reads desktop state through CoreGraphics and AppKit.
// Use one Collector per process: the input-counting event tap and App Nap
// activity are process-wide resources.
type Collector struct {
	mu       sync.Mutex
	now      func() time.Time
	titles   bool
	tapOn    bool
	caps     ports.Capabilities
	tr       *translator
	windows  []C.bt_window
	displays []C.bt_rect
	activity unsafe.Pointer // C-owned NSProcessInfo activity token
	closed   bool
}

var _ ports.Collector = (*Collector)(nil)

// New starts the collector. It never fails for missing permissions: those
// features are switched off and listed in Capabilities().MissingPermits.
func New(opts Options) (*Collector, error) {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	c := &Collector{
		now:      now,
		windows:  make([]C.bt_window, maxWindows),
		displays: make([]C.bt_rect, maxDisplays),
		caps: ports.Capabilities{
			Name: "darwin", Windows: true, Pointer: true, Idle: true,
			Limitations: []string{
				"video and call detection not implemented",
				"focus is inferred from window z-order (no Accessibility permission)",
			},
		},
	}
	c.tr = newTranslator(c.bundleID)

	if opts.Titles {
		if C.bt_preflight_screen_capture() == 1 {
			c.titles = true
			c.caps.Titles = true
		} else {
			c.caps.MissingPermits = append(c.caps.MissingPermits, "Screen Recording")
		}
	}
	if opts.InputCounts {
		if C.bt_tap_start() == 1 {
			c.tapOn = true
			c.caps.InputCounts = true
		} else {
			c.caps.MissingPermits = append(c.caps.MissingPermits, "Input Monitoring")
		}
	}
	c.activity = C.bt_begin_activity()
	return c, nil
}

// Capabilities reports what this collector can observe, including its
// known Phase 0 limitations.
func (c *Collector) Capabilities() ports.Capabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	caps := c.caps
	caps.MissingPermits = append([]string(nil), c.caps.MissingPermits...)
	caps.Limitations = append([]string(nil), c.caps.Limitations...)
	return caps
}

// Snapshot reads one tick of desktop state.
func (c *Collector) Snapshot(ctx context.Context) (model.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return model.Snapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return model.Snapshot{}, errClosed
	}
	st := c.readRaw()
	var cnt inputCounts
	if c.tapOn {
		var keys, scrolls C.uint64_t
		C.bt_tap_counts(&keys, &scrolls)
		cnt = inputCounts{On: true, Keys: uint64(keys), Scrolls: uint64(scrolls)}
	}
	return c.tr.snapshot(c.now(), st, cnt), nil
}

// Close stops the event tap and ends the App Nap activity. It is idempotent.
func (c *Collector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.tapOn {
		C.bt_tap_stop()
		c.tapOn = false
	}
	C.bt_end_activity(c.activity)
	c.activity = nil
	return nil
}

func (c *Collector) readRaw() rawState {
	var st rawState

	n := int(C.bt_list_windows(&c.windows[0], C.int(len(c.windows)), boolInt(c.titles)))
	st.Windows = make([]rawWindow, 0, n)
	for i := range n {
		w := &c.windows[i]
		rw := rawWindow{
			Number: uint32(w.number),
			PID:    int(w.pid),
			Layer:  int(w.layer),
			Bounds: toRect(float64(w.x), float64(w.y), float64(w.w), float64(w.h)),
			Owner:  C.GoString(&w.owner[0]),
		}
		if c.titles {
			rw.Title, rw.HasTitle = C.GoString(&w.title[0]), true
			clear(w.title[:]) // don't leave titles in the reusable buffer
		}
		st.Windows = append(st.Windows, rw)
	}

	nd := int(C.bt_list_displays(&c.displays[0], C.int(len(c.displays))))
	for i := range nd {
		d := c.displays[i]
		st.Displays = append(st.Displays, toRect(float64(d.x), float64(d.y), float64(d.w), float64(d.h)))
	}

	var px, py C.double
	if C.bt_pointer(&px, &py) == 1 {
		st.Pointer = point{X: int(math.Floor(float64(px))), Y: int(math.Floor(float64(py)))}
		st.PointerOK = true
	}
	st.IdleSeconds = float64(C.bt_idle_seconds())
	st.Locked = C.bt_screen_locked() == 1
	return st
}

func (c *Collector) bundleID(pid int) string {
	var buf [256]C.char
	if C.bt_bundle_id(C.int32_t(pid), &buf[0], C.int(len(buf))) == 0 {
		return ""
	}
	return C.GoString(&buf[0])
}

// toRect rounds CoreGraphics' fractional points outward to whole points.
func toRect(x, y, w, h float64) model.Rect {
	x0, y0 := math.Floor(x), math.Floor(y)
	x1, y1 := math.Ceil(x+w), math.Ceil(y+h)
	return model.Rect{X: int(x0), Y: int(y0), W: int(x1 - x0), H: int(y1 - y0)}
}

func boolInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}
