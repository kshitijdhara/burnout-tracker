//go:build !windows

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// silenceStdio points file descriptors 1 and 2 at /dev/null, so nothing the
// hook process does (including a runtime crash message) can reach Claude
// Code's context or the user's terminal.
func silenceStdio() {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	_ = unix.Dup2(int(null.Fd()), 1)
	_ = unix.Dup2(int(null.Fd()), 2)
	os.Stdout, os.Stderr = null, null
}
