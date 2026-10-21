//go:build windows

package windows

import (
	"path/filepath"
	"unsafe"

	w32 "golang.org/x/sys/windows"
)

// Win32 entry points that golang.org/x/sys/windows doesn't wrap. The lazy
// procs are resolved on first use and are read-only afterwards.
var (
	user32   = w32.NewLazySystemDLL("user32.dll")
	kernel32 = w32.NewLazySystemDLL("kernel32.dll")

	procGetWindowRect                 = user32.NewProc("GetWindowRect")
	procGetAncestor                   = user32.NewProc("GetAncestor")
	procGetWindow                     = user32.NewProc("GetWindow")
	procGetWindowLongW                = user32.NewProc("GetWindowLongW")
	procIsIconic                      = user32.NewProc("IsIconic")
	procGetCursorPos                  = user32.NewProc("GetCursorPos")
	procWindowFromPoint               = user32.NewProc("WindowFromPoint")
	procEnumDisplayMonitors           = user32.NewProc("EnumDisplayMonitors")
	procGetWindowTextW                = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW          = user32.NewProc("GetWindowTextLengthW")
	procGetLastInputInfo              = user32.NewProc("GetLastInputInfo")
	procOpenInputDesktop              = user32.NewProc("OpenInputDesktop")
	procCloseDesktop                  = user32.NewProc("CloseDesktop")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procRegisterRawInputDevices       = user32.NewProc("RegisterRawInputDevices")
	procGetRawInputData               = user32.NewProc("GetRawInputData")
	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procUnregisterClassW              = user32.NewProc("UnregisterClassW")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
	procGetMessageW                   = user32.NewProc("GetMessageW")
	procDispatchMessageW              = user32.NewProc("DispatchMessageW")
	procPostMessageW                  = user32.NewProc("PostMessageW")
	procPostQuitMessage               = user32.NewProc("PostQuitMessage")
	procGetTickCount64                = kernel32.NewProc("GetTickCount64")
	procGetModuleHandleW              = kernel32.NewProc("GetModuleHandleW")
)

const (
	gaRoot             = 2 // GetAncestor: root window
	gwOwner            = 4 // GetWindow: owner window
	gwlExStyle         = -20
	wsExToolWindow     = 0x00000080
	wsExAppWindow      = 0x00040000
	desktopSwitch      = 0x0100 // DESKTOP_SWITCHDESKTOP
	maxImagePath       = 1024
	unknownApp         = "unknown"
	dpiPerMonitorAware = -4 // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2
)

// signedArg passes a negative Win32 integer argument (GWL_*, DPI contexts)
// as the sign-extended uintptr the ABI expects.
func signedArg(v int32) uintptr { return uintptr(v) } //nolint:gosec // G115: sign extension is intended

// enablePerMonitorDPI makes every coordinate API report physical pixels, so
// DWM frame bounds, window rects, monitor rects and the cursor position are
// all in the same space. It fails harmlessly if awareness is already set or
// the API is missing (Windows 10 before 1703).
func enablePerMonitorDPI() {
	if procSetProcessDpiAwarenessContext.Find() != nil {
		return
	}
	_, _, _ = procSetProcessDpiAwarenessContext.Call(signedArg(dpiPerMonitorAware))
}

func windowBounds(h w32.HWND) (w32.Rect, bool) {
	var r w32.Rect
	err := w32.DwmGetWindowAttribute(h, w32.DWMWA_EXTENDED_FRAME_BOUNDS, unsafe.Pointer(&r), uint32(unsafe.Sizeof(r))) //nolint:gosec // G103: Win32 interop; the pointer is converted inside the Call expression
	if err == nil {
		return r, true
	}
	ok, _, _ := procGetWindowRect.Call(uintptr(h), uintptr(unsafe.Pointer(&r))) //nolint:gosec // G103: Win32 interop; the pointer is converted inside the Call expression
	return r, ok != 0
}

func isCloaked(h w32.HWND) bool {
	var cloaked uint32
	err := w32.DwmGetWindowAttribute(h, w32.DWMWA_CLOAKED, unsafe.Pointer(&cloaked), uint32(unsafe.Sizeof(cloaked))) //nolint:gosec // G103: Win32 interop; the pointer is converted inside the Call expression
	return err == nil && cloaked != 0
}

func isIconic(h w32.HWND) bool {
	r, _, _ := procIsIconic.Call(uintptr(h))
	return r != 0
}

func owner(h w32.HWND) w32.HWND {
	r, _, _ := procGetWindow.Call(uintptr(h), gwOwner)
	return w32.HWND(r)
}

func exStyle(h w32.HWND) uint32 {
	r, _, _ := procGetWindowLongW.Call(uintptr(h), signedArg(gwlExStyle))
	return uint32(r) //nolint:gosec // G115: GetWindowLongW returns a 32-bit LONG
}

func rootOf(h w32.HWND) w32.HWND {
	if h == 0 {
		return 0
	}
	r, _, _ := procGetAncestor.Call(uintptr(h), gaRoot)
	return w32.HWND(r)
}

func cursorPos() (x, y int, ok bool) {
	var p struct{ X, Y int32 }
	r, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&p))) //nolint:gosec // G103: Win32 interop; the pointer is converted inside the Call expression
	return int(p.X), int(p.Y), r != 0
}

func windowText(h w32.HWND) string {
	n, _, _ := procGetWindowTextLengthW.Call(uintptr(h))
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	got, _, _ := procGetWindowTextW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))) //nolint:gosec // G103: Win32 interop; the pointer is converted inside the Call expression
	return w32.UTF16ToString(buf[:got])
}

func lastInputTick() (uint32, bool) {
	info := struct{ Size, Time uint32 }{Size: 8}
	r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&info))) //nolint:gosec // G103: Win32 interop; the pointer is converted inside the Call expression
	return info.Time, r != 0
}

func tickCount64() uint64 {
	r, _, _ := procGetTickCount64.Call()
	return uint64(r)
}

// inputDesktopAvailable reports whether the interactive input desktop can be
// opened. It can't while the secure desktop (lock screen, UAC) is active.
func inputDesktopAvailable() bool {
	h, _, _ := procOpenInputDesktop.Call(0, 0, desktopSwitch)
	if h == 0 {
		return false
	}
	_, _, _ = procCloseDesktop.Call(h)
	return true
}

// exeName returns the executable base name for pid, or "unknown" when the
// process can't be opened (protected processes, or the pid has exited).
func exeName(pid uint32) string {
	if pid == 0 {
		return unknownApp
	}
	h, err := w32.OpenProcess(w32.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return unknownApp
	}
	defer func() { _ = w32.CloseHandle(h) }()
	buf := make([]uint16, maxImagePath)
	size := uint32(maxImagePath)
	if err := w32.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return unknownApp
	}
	return filepath.Base(w32.UTF16ToString(buf[:size]))
}

func moduleHandle() uintptr {
	r, _, _ := procGetModuleHandleW.Call(0)
	return r
}
