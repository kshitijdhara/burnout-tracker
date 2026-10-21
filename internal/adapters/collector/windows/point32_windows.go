//go:build windows && !(amd64 || arm64)

package windows

import w32 "golang.org/x/sys/windows"

// windowFromPoint wraps WindowFromPoint on 32-bit ABIs, where the POINT
// argument is pushed as two 32-bit words. We don't ship 32-bit builds; this
// keeps the package compiling for them.
func windowFromPoint(x, y int) w32.HWND {
	r, _, _ := procWindowFromPoint.Call(uintptr(int32(x)), uintptr(int32(y)))
	return w32.HWND(r)
}
