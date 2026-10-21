//go:build windows && (amd64 || arm64)

package windows

import w32 "golang.org/x/sys/windows"

// windowFromPoint wraps WindowFromPoint, which takes a POINT by value. On
// the 64-bit ABIs we ship (amd64, arm64) an 8-byte struct travels in one
// register, so x and y are packed into a single argument.
func windowFromPoint(x, y int) w32.HWND {
	packed := uintptr(uint32(int32(x))) | uintptr(uint32(int32(y)))<<32 //nolint:gosec // G115: screen coordinates fit in a LONG
	r, _, _ := procWindowFromPoint.Call(packed)
	return w32.HWND(r)
}
