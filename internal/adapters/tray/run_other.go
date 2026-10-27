//go:build !darwin

package tray

import (
	"context"
	"runtime"
	"sync"
	"time"

	"fyne.io/systray"
)

const (
	// readyWait bounds the wait for the icon to appear.
	readyWait = 10 * time.Second
	// quitWait bounds the wait for the icon to go.
	quitWait = 2 * time.Second
	// stopWait bounds how long work gets to write out what it holds once
	// the icon has gone without work asking.
	stopWait = 10 * time.Second
)

// run runs the tray on a goroutine of its own (Windows and Linux don't
// need the main thread) and work beside it, and stops work if the icon
// goes away first.
func run(ctx context.Context, m Menu, work Work) error {
	if why := noHost(ctx); why != "" {
		m.Log.Warn("no tray icon: "+why+", so the screen isn't watched", "platform", runtime.GOOS)
		return work(ctx, false)
	}
	registered := watchRegistered() // before systray starts, so the announcement can't be missed
	ready, stopped, gone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	// systray calls onExit when the icon is removed: on Windows at
	// WM_ENDSESSION, even for a logoff that is then cancelled, and at
	// WM_CLOSE; and when the loop ends.
	var goneOnce sync.Once
	onExit := func() { goneOnce.Do(func() { close(gone) }) }
	go func() {
		defer close(stopped)
		defer func() {
			// On Linux, systray panics on quit if it lost the bus it found at start.
			if r := recover(); r != nil {
				m.Log.Warn("the tray icon stopped badly", "panic", r)
			}
		}()
		runtime.LockOSThread() // Windows: the icon's window and its messages share a thread
		systray.Run(func() { close(ready) }, onExit)
	}()
	select {
	case <-ready:
	case <-time.After(readyWait):
		m.Log.Error("the tray icon didn't appear, so the screen isn't watched")
		return work(ctx, false)
	case <-ctx.Done():
		return work(ctx, false)
	}
	if !registered(ctx, readyWait) {
		m.Log.Debug("the tray host didn't announce the icon; showing the menu anyway")
	}
	ctx, cancel := context.WithCancel(ctx)
	go show(ctx, m, platformIcons())
	done := make(chan error, 1)
	go func() { done <- work(ctx, true) }()
	var err error
	select {
	case err = <-done:
	case <-gone:
		// Nothing may be recorded without the icon (privacy & security
		// §5.2 AC-1): stop work, and let it write out what it holds.
		m.Log.Warn("the tray icon went away, so recording stops")
		cancel()
		select {
		case err = <-done:
		case <-time.After(stopWait):
		}
		if err == nil {
			err = ErrIconGone
		}
	}
	cancel()
	systray.Quit()
	select {
	case <-stopped:
	case <-time.After(quitWait):
	}
	return err
}
