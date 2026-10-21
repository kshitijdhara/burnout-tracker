//go:build darwin && cgo

package darwin

/*
#cgo LDFLAGS: -framework CoreAudio -framework CoreMediaIO -framework IOKit

#include <stdint.h>
#include <stdlib.h>
#include <dlfcn.h>
#include <Availability.h>
#include <CoreAudio/CoreAudio.h>
#include <CoreMediaIO/CMIOHardware.h>
#include <IOKit/pwr_mgt/IOPMLib.h>
#import <AppKit/AppKit.h>

// Media state is read without any permission and without touching audio or
// video: only whether a device or process is running, and which process
// asked the system to keep the display awake.

typedef pid_t (*bt_responsible_fn)(pid_t);

// bt_responsible maps a helper process (a browser's audio or GPU process,
// WebKit's content processes) to the app responsible for it, which owns the
// windows. The lookup is resolved at run time; without it the pid is kept.
static pid_t bt_responsible(pid_t pid) {
	static bt_responsible_fn fn = NULL;
	static int looked = 0;
	if (!looked) {
		fn = (bt_responsible_fn)dlsym(RTLD_DEFAULT, "responsibility_get_pid_responsible_for_pid");
		looked = 1;
	}
	if (fn == NULL) {
		return pid;
	}
	pid_t r = fn(pid);
	return r > 0 ? r : pid;
}

// bt_regular_app reports whether pid is an ordinary app with a Dock icon,
// as opposed to a background agent (a voice trigger, a menu-bar utility).
static int bt_regular_app(pid_t pid) {
	@autoreleasepool {
		NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
		return app != nil && app.activationPolicy == NSApplicationActivationPolicyRegular;
	}
}

static int bt_has_responsible(void) {
	return dlsym(RTLD_DEFAULT, "responsibility_get_pid_responsible_for_pid") != NULL;
}

// bt_mic_pids writes the regular apps whose processes are capturing audio
// input. Background agents that keep the microphone open (voice triggers,
// dictation utilities) are left out: they are not calls.
// *supported is 0 where macOS lacks per-process audio objects (before 14.2).
static int bt_mic_pids(int32_t *out, int max, int *supported) {
	*supported = 0;
#if defined(__MAC_14_2) && __MAC_OS_X_VERSION_MAX_ALLOWED >= __MAC_14_2
	if (__builtin_available(macOS 14.2, *)) {
		AudioObjectPropertyAddress list = {
			kAudioHardwarePropertyProcessObjectList, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
		UInt32 size = 0;
		if (AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &list, 0, NULL, &size) != noErr) {
			return 0;
		}
		*supported = 1;
		if (size == 0) {
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
			if (AudioObjectGetPropertyData(ids[i], &pa, 0, NULL, &sz, &pid) != noErr || pid <= 0) {
				continue;
			}
			pid_t app = bt_responsible(pid);
			if (bt_regular_app(app)) {
				out[count++] = (int32_t)app;
			}
		}
		free(ids);
		return count;
	}
#endif
	return 0;
}

// bt_default_input_running reports whether the default input device is
// running for any process. A fallback before macOS 14.2: it cannot say
// which app, and a headset's device also runs for playback.
static int bt_default_input_running(void) {
	AudioObjectPropertyAddress a = {
		kAudioHardwarePropertyDefaultInputDevice, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
	AudioDeviceID dev = kAudioObjectUnknown;
	UInt32 sz = sizeof dev;
	if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &a, 0, NULL, &sz, &dev) != noErr || dev == kAudioObjectUnknown) {
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

// bt_display_awake_pids writes the apps holding an assertion that keeps the
// display awake, which browsers and players take while video plays.
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
				out[count++] = (int32_t)bt_responsible((pid_t)pid);
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

// maxMediaPIDs bounds the processes read per media poll.
const maxMediaPIDs = 64

// mediaReader polls media state. It reuses its buffers across polls.
type mediaReader struct {
	pids [maxMediaPIDs]C.int32_t
}

// read returns the current media state.
func (m *mediaReader) read() mediaState {
	st := mediaState{OK: true, Responsible: C.bt_has_responsible() == 1}
	var supported C.int
	n := int(C.bt_mic_pids(&m.pids[0], C.int(len(m.pids)), &supported))
	st.PerProcessMic = supported == 1
	st.MicPIDs = m.copyPIDs(n)
	if !st.PerProcessMic {
		st.MicAny = C.bt_default_input_running() == 1
	}
	st.CameraOn = C.bt_camera_running() == 1
	n = int(C.bt_display_awake_pids(&m.pids[0], C.int(len(m.pids))))
	st.DisplayAwakePIDs = m.copyPIDs(n)
	return st
}

func (m *mediaReader) copyPIDs(n int) []int {
	if n <= 0 {
		return nil
	}
	out := make([]int, n)
	for i := range n {
		out[i] = int(m.pids[i])
	}
	return out
}
