//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// silenceStdio redirects the standard output and error handles to NUL, so
// nothing the hook process does can reach Claude Code or the terminal.
func silenceStdio() {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	h := windows.Handle(null.Fd())
	_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, h)
	_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, h)
	os.Stdout, os.Stderr = null, null
}
