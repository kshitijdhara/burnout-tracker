//go:build windows

package main

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
)

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procFreeConsole           = kernel32.NewProc("FreeConsole")
)

// serviceStdio sets up the service daemon's output under Task Scheduler,
// which keeps none: Go's crash output (a panic, a fatal error) goes to the
// crash log, and the console window Windows opens for a console program
// at logon is closed. A console shared with others, a terminal the daemon
// was started from, is left alone.
func serviceStdio(logDir string) {
	f, err := os.OpenFile(filepath.Join(logDir, paths.CrashLogFile), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) //nolint:gosec // our own log directory
	if err == nil {
		_ = debug.SetCrashOutput(f, debug.CrashOptions{})
		_ = f.Close() // SetCrashOutput keeps its own duplicate
	}
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids))) //nolint:gosec // a Win32 out-parameter
	if n == 1 {
		_, _, _ = procFreeConsole.Call()
	}
}
