//go:build darwin && cgo

package tray

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit -framework CoreGraphics

#import <AppKit/AppKit.h>
#include <CoreGraphics/CoreGraphics.h>

// bt_gui_session reports whether this process is in a login session with
// a menu bar: not when started over ssh or before anyone logs in.
static int bt_gui_session(void) {
	CFDictionaryRef d = CGSessionCopyCurrentDictionary();
	if (d == NULL) {
		return 0;
	}
	CFRelease(d);
	return 1;
}

// bt_accessory keeps the daemon out of the Dock and the app switcher.
static void bt_accessory(void) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
}

// bt_stop ends [NSApp run] so it returns, instead of terminating the
// process as systray.Quit does. The loop only notices after one more event.
static void bt_stop(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp stop:nil];
		NSEvent *e = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
		                                location:NSZeroPoint
		                           modifierFlags:0
		                               timestamp:0
		                            windowNumber:0
		                                 context:nil
		                                 subtype:0
		                                   data1:0
		                                   data2:0];
		[NSApp postEvent:e atStart:YES];
	});
}
*/
import "C"

import (
	"context"
	"time"

	"fyne.io/systray"

	"github.com/kshitijdhara/burntout/assets"
)

// exitWait bounds how long a logout waits for work to write out what it
// holds.
const exitWait = 10 * time.Second

// run runs the menu bar on the calling goroutine, which the systray
// package locked to the main thread, and work on another. The icon can't
// be ⌘-dragged off the menu bar: status items aren't removable unless an
// app asks (AC-1).
func run(ctx context.Context, m Menu, work Work) error {
	if C.bt_gui_session() == 0 {
		m.Log.Warn("no menu bar in this session (not logged in at the screen?), so the screen isn't watched")
		return work(ctx, false)
	}
	C.bt_accessory()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	systray.Run(func() {
		go show(ctx, m, icons{recording: assets.TrayTemplate, paused: assets.TrayPausedTemplate, template: true})
		go func() {
			done <- work(ctx, true)
			C.bt_stop()
		}()
	}, func() {
		// macOS is quitting the app, at logout: the process exits when this
		// returns, so let work write out what it holds first.
		cancel()
		select {
		case err := <-done:
			done <- err
		case <-time.After(exitWait):
		}
	})
	return <-done
}
