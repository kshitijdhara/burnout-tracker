//go:build darwin && cgo

package darwin

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework CoreAudio -framework CoreMediaIO -framework IOKit -framework AppKit -framework Foundation

#include <stdint.h>
#include <stdlib.h>
#include <dlfcn.h>
#include <libproc.h>
#include <dispatch/dispatch.h>
#include <Availability.h>
#include <CoreAudio/CoreAudio.h>
#include <CoreMediaIO/CMIOHardware.h>
#include <IOKit/pwr_mgt/IOPMLib.h>
#import <AppKit/AppKit.h>

// Media state is read without any permission and without touching audio or
// video: only whether a device or process is running, and which process
// asked the system to keep the display awake. Interpretation (which app,
// whether it counts) happens in Go; see resolveMedia.

typedef pid_t (*bt_responsible_fn)(pid_t);

// bt_responsible_lookup resolves, once, the function that maps a helper
// process (a browser's audio or GPU process, WebKit's content processes,
// a command run in a terminal) to the app responsible for it.
static bt_responsible_fn bt_responsible_lookup(void) {
	static bt_responsible_fn fn = NULL;
	static dispatch_once_t once;
	dispatch_once(&once, ^{
		fn = (bt_responsible_fn)dlsym(RTLD_DEFAULT, "responsibility_get_pid_responsible_for_pid");
	});
	return fn;
}

static int32_t bt_responsible(int32_t pid) {
	bt_responsible_fn fn = bt_responsible_lookup();
	if (fn == NULL) {
		return pid;
	}
	pid_t r = fn((pid_t)pid);
	return r > 0 ? (int32_t)r : pid;
}

// bt_regular_app reports whether pid is an ordinary app with a Dock icon,
// as opposed to a background agent (a voice trigger, a menu-bar utility).
static int bt_regular_app(int32_t pid) {
	@autoreleasepool {
		NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:(pid_t)pid];
		return app != nil && app.activationPolicy == NSApplicationActivationPolicyRegular;
	}
}

// bt_pid_path writes pid's executable path; returns 0 if unknown.
static int bt_pid_path(int32_t pid, char *out, int max) {
	out[0] = '\0';
	return proc_pidpath((pid_t)pid, out, (uint32_t)max) > 0;
}

// bt_media_support reports, without any IPC, whether this Mac has
// per-process audio objects (macOS 14.2+) and the responsibility lookup.
static void bt_media_support(int *per_process, int *responsible) {
	*per_process = 0;
#if defined(__MAC_14_2) && __MAC_OS_X_VERSION_MAX_ALLOWED >= __MAC_14_2
	if (__builtin_available(macOS 14.2, *)) {
		*per_process = 1;
	}
#endif
	*responsible = bt_responsible_lookup() != NULL;
}

// bt_mic_pids writes the processes capturing audio input (macOS 14.2+).
static int bt_mic_pids(int32_t *out, int max) {
#if defined(__MAC_14_2) && __MAC_OS_X_VERSION_MAX_ALLOWED >= __MAC_14_2
	if (__builtin_available(macOS 14.2, *)) {
		AudioObjectPropertyAddress list = {
			kAudioHardwarePropertyProcessObjectList, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
		UInt32 size = 0;
		if (AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &list, 0, NULL, &size) != noErr || size == 0) {
			return 0;
		}
		AudioObjectID *ids = (AudioObjectID *)malloc(size);
		if (ids == NULL) {
			return 0;
		}
		if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &list, 0, NULL, &size, ids) != noErr) {
			free(ids);
			return 0;
		}
		UInt32 n = size / sizeof(AudioObjectID);
		int count = 0;
		for (UInt32 i = 0; i < n && count < max; i++) {
			AudioObjectPropertyAddress in = {
				kAudioProcessPropertyIsRunningInput, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
			UInt32 running = 0;
			UInt32 sz = sizeof running;
			if (AudioObjectGetPropertyData(ids[i], &in, 0, NULL, &sz, &running) != noErr || running == 0) {
				continue;
			}
			AudioObjectPropertyAddress pa = {
				kAudioProcessPropertyPID, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
			pid_t pid = 0;
			sz = sizeof pid;
			if (AudioObjectGetPropertyData(ids[i], &pa, 0, NULL, &sz, &pid) == noErr && pid > 0) {
				out[count++] = (int32_t)pid;
			}
		}
		free(ids);
		return count;
	}
#endif
	return 0;
}

// bt_default_input_running reports whether the default input device is
// running for any process: the fallback before macOS 14.2, which can't
// say which app. A device that also plays audio (a headset) is skipped:
// it runs for music too, which is not a call.
static int bt_default_input_running(void) {
	AudioObjectPropertyAddress a = {
		kAudioHardwarePropertyDefaultInputDevice, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
	AudioDeviceID dev = kAudioObjectUnknown;
	UInt32 sz = sizeof dev;
	if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &a, 0, NULL, &sz, &dev) != noErr || dev == kAudioObjectUnknown) {
		return 0;
	}
	AudioObjectPropertyAddress outStreams = {
		kAudioDevicePropertyStreams, kAudioObjectPropertyScopeOutput, kAudioObjectPropertyElementMain};
	UInt32 outSize = 0;
	if (AudioObjectGetPropertyDataSize(dev, &outStreams, 0, NULL, &outSize) == noErr && outSize > 0) {
		return 0;
	}
	AudioObjectPropertyAddress r = {
		kAudioDevicePropertyDeviceIsRunningSomewhere, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
	UInt32 running = 0;
	sz = sizeof running;
	if (AudioObjectGetPropertyData(dev, &r, 0, NULL, &sz, &running) != noErr) {
		return 0;
	}
	return running != 0;
}

// bt_camera_running reports whether any camera is in use by any process.
static int bt_camera_running(void) {
	CMIOObjectPropertyAddress a = {
		kCMIOHardwarePropertyDevices, kCMIOObjectPropertyScopeGlobal, kCMIOObjectPropertyElementMain};
	UInt32 size = 0;
	if (CMIOObjectGetPropertyDataSize(kCMIOObjectSystemObject, &a, 0, NULL, &size) != kCMIOHardwareNoError || size == 0) {
		return 0;
	}
	CMIOObjectID *ids = (CMIOObjectID *)malloc(size);
	if (ids == NULL) {
		return 0;
	}
	UInt32 used = 0;
	if (CMIOObjectGetPropertyData(kCMIOObjectSystemObject, &a, 0, NULL, size, &used, ids) != kCMIOHardwareNoError) {
		free(ids);
		return 0;
	}
	int on = 0;
	UInt32 n = used / sizeof(CMIOObjectID);
	for (UInt32 i = 0; i < n && !on; i++) {
		CMIOObjectPropertyAddress r = {
			kCMIODevicePropertyDeviceIsRunningSomewhere, kCMIOObjectPropertyScopeWildcard, kCMIOObjectPropertyElementWildcard};
		UInt32 running = 0;
		UInt32 got = 0;
		if (CMIOObjectGetPropertyData(ids[i], &r, 0, NULL, sizeof running, &got, &running) == kCMIOHardwareNoError && running) {
			on = 1;
		}
	}
	free(ids);
	return on;
}

// bt_display_awake_pids writes the processes holding an assertion that
// keeps the display awake, which browsers and players take while video
// plays (and caffeinate -d takes on request; Go filters those out).
static int bt_display_awake_pids(int32_t *out, int max) {
	CFDictionaryRef byPID = NULL;
	if (IOPMCopyAssertionsByProcess(&byPID) != kIOReturnSuccess || byPID == NULL) {
		return 0;
	}
	CFIndex n = CFDictionaryGetCount(byPID);
	if (n <= 0) {
		CFRelease(byPID);
		return 0;
	}
	const void **keys = (const void **)malloc((size_t)n * sizeof(void *));
	const void **vals = (const void **)malloc((size_t)n * sizeof(void *));
	if (keys == NULL || vals == NULL) {
		free(keys);
		free(vals);
		CFRelease(byPID);
		return 0;
	}
	CFDictionaryGetKeysAndValues(byPID, keys, vals);
	int count = 0;
	for (CFIndex i = 0; i < n && count < max; i++) {
		int64_t pid = 0;
		if (keys[i] == NULL || CFGetTypeID(keys[i]) != CFNumberGetTypeID() ||
			!CFNumberGetValue((CFNumberRef)keys[i], kCFNumberSInt64Type, &pid) || pid <= 0) {
			continue;
		}
		if (vals[i] == NULL || CFGetTypeID(vals[i]) != CFArrayGetTypeID()) {
			continue;
		}
		CFArrayRef list = (CFArrayRef)vals[i];
		for (CFIndex j = 0; j < CFArrayGetCount(list); j++) {
			CFTypeRef a = CFArrayGetValueAtIndex(list, j);
			if (a == NULL || CFGetTypeID(a) != CFDictionaryGetTypeID()) {
				continue;
			}
			CFTypeRef type = CFDictionaryGetValue((CFDictionaryRef)a, CFSTR("AssertType"));
			if (type != NULL && CFGetTypeID(type) == CFStringGetTypeID() &&
				(CFEqual(type, CFSTR("PreventUserIdleDisplaySleep")) || CFEqual(type, CFSTR("NoDisplaySleepAssertion")))) {
				out[count++] = (int32_t)pid;
				break;
			}
		}
	}
	free(keys);
	free(vals);
	CFRelease(byPID);
	return count;
}

*/
import "C"

import (
	"sync/atomic"
	"time"
)

// maxMediaPIDs bounds the processes read per media poll.
const maxMediaPIDs = 64

// mediaInterval is how often media state is polled. Calls and video change
// slowly; a few seconds of latency costs nothing.
const mediaInterval = 3 * time.Second

// mediaFirstWait bounds how long the first snapshot waits for the first
// media poll, so `doctor` can show it without a slow audio service
// stalling anything.
const mediaFirstWait = time.Second

// mediaSupport reports, without asking any system service, what media
// detection can do on this Mac.
func mediaSupport() (perProcessMic, responsible bool) {
	var p, r C.int
	C.bt_media_support(&p, &r)
	return p == 1, r == 1
}

// maxPathLen sizes the executable-path buffer (PROC_PIDPATHINFO_MAXSIZE).
const maxPathLen = 4096

// mediaPoller reads media state on its own goroutine, so a stalled system
// service (coreaudiod, powerd) only makes media state stale; it never
// blocks a snapshot.
type mediaPoller struct {
	latest atomic.Pointer[mediaState]
	ready  chan struct{} // closed after the first read
	stop   chan struct{}
	done   chan struct{}
	pids   [maxMediaPIDs]C.int32_t
	path   [maxPathLen]C.char
}

func startMediaPoller() *mediaPoller {
	m := &mediaPoller{ready: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{})}
	go m.run()
	return m
}

func (m *mediaPoller) run() {
	defer close(m.done)
	t := time.NewTicker(mediaInterval)
	defer t.Stop()
	for first := true; ; first = false {
		st := resolveMedia(m.readRaw(), m)
		m.latest.Store(&st)
		if first {
			close(m.ready)
		}
		select {
		case <-m.stop:
			return
		case <-t.C:
		}
	}
}

// current returns the latest media state, waiting up to mediaFirstWait for
// the first poll when waitFirst is set.
func (m *mediaPoller) current(waitFirst bool) mediaState {
	if waitFirst {
		select {
		case <-m.ready:
		case <-time.After(mediaFirstWait):
		}
	}
	if st := m.latest.Load(); st != nil {
		return *st
	}
	return mediaState{}
}

// close stops the poller. A poll stuck in a system call can't be
// interrupted, so it waits only briefly.
func (m *mediaPoller) close() {
	close(m.stop)
	select {
	case <-m.done:
	case <-time.After(2 * time.Second):
	}
}

func (m *mediaPoller) readRaw() rawMedia {
	var raw rawMedia
	raw.PerProcessMic, raw.Responsible = mediaSupport()
	raw.MicPIDs = m.copyPIDs(int(C.bt_mic_pids(&m.pids[0], C.int(len(m.pids)))))
	if !raw.PerProcessMic {
		raw.MicAny = C.bt_default_input_running() == 1
	}
	raw.CameraOn = C.bt_camera_running() == 1
	raw.AwakePIDs = m.copyPIDs(int(C.bt_display_awake_pids(&m.pids[0], C.int(len(m.pids)))))
	return raw
}

func (m *mediaPoller) copyPIDs(n int) []int {
	if n <= 0 {
		return nil
	}
	out := make([]int, n)
	for i := range n {
		out[i] = int(m.pids[i])
	}
	return out
}

// The processLookup methods, used by resolveMedia on the poller goroutine.

func (m *mediaPoller) responsible(pid int) int { return int(C.bt_responsible(C.int32_t(pid))) }

func (m *mediaPoller) regularApp(pid int) bool { return C.bt_regular_app(C.int32_t(pid)) == 1 }

func (m *mediaPoller) executable(pid int) string {
	if C.bt_pid_path(C.int32_t(pid), &m.path[0], C.int(len(m.path))) == 0 {
		return ""
	}
	return C.GoString(&m.path[0])
}
