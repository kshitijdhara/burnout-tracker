//go:build !darwin

package tray

import (
	"context"
	"runtime"
	"time"

	"fyne.io/systray"
)

const (
	// readyWait bounds the wait for the icon to appear.
	readyWait = 10 * time.Second
	// quitWait bounds the wait for the icon to go.
	quitWait = 2 * time.Second
)

// run runs the tray on a goroutine of its own (Windows and Linux don't
// need the main thread) and work on the calling one.
func run(ctx context.Context, m Menu, work Work) error {
	if why := noHost(ctx); why != "" {
		m.Log.Warn("no tray icon: "+why+", so the screen isn't watched", "platform", runtime.GOOS)
		return work(ctx, false)
	}
	ready, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		defer func() {
			// On Linux, systray panics on quit if it lost the bus it found at start.
			if r := recover(); r != nil {
				m.Log.Warn("the tray icon stopped badly", "panic", r)
			}
		}()
		runtime.LockOSThread() // Windows: the icon's window and its messages share a thread
		systray.Run(func() { close(ready) }, nil)
	}()
	select {
	case <-ready:
	case <-time.After(readyWait):
		m.Log.Error("the tray icon didn't appear, so the screen isn't watched")
		return work(ctx, false)
	case <-ctx.Done():
		return work(ctx, false)
	}
	ctx, cancel := context.WithCancel(ctx)
	go show(ctx, m, platformIcons())
	err := work(ctx, true)
	cancel()
	systray.Quit()
	select {
	case <-stopped:
	case <-time.After(quitWait):
	}
	return err
}
