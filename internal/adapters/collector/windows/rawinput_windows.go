//go:build windows

package windows

import (
	"fmt"
	"runtime"
	"sync/atomic"
	"unsafe"

	w32 "golang.org/x/sys/windows"
)

// Raw Input counts keyboard and mouse activity without the low-level hooks
// keyloggers use: no system-wide input latency, and a lower EDR profile
// (docs/privacy-and-security.md §8.6).
//
// The line we never cross: for keyboard events we read ONLY the
// RAWINPUTHEADER (RID_HEADER), which says "a keyboard event happened". The
// RAWKEYBOARD payload — MakeCode, VKey, Message, Flags — is never requested,
// so key identities never enter this process's memory. For mouse events we
// read the payload only to look at usButtonFlags (wheel and button-down).

const (
	wmClose     = 0x0010
	wmDestroy   = 0x0002
	wmInput     = 0x00FF
	ridHeader   = 0x10000005
	ridInput    = 0x10000003
	rimMouse    = 0
	rimKeyboard = 1
	ridevSink   = 0x00000100 // RIDEV_INPUTSINK: receive input while not in the foreground
	ridevRemove = 0x00000001
	usagePage   = 0x01 // HID generic desktop
	usageMouse  = 0x02
	usageKbd    = 0x06
	hwndMessage = ^uintptr(2) // HWND_MESSAGE (-3): a message-only window
)

type rawInputDevice struct {
	UsagePage uint16
	Usage     uint16
	Flags     uint32
	Target    uintptr
}

type rawInputHeader struct {
	Type   uint32
	Size   uint32
	Device uintptr
	WParam uintptr
}

// rawMouse mirrors RAWMOUSE. The usButtonFlags/usButtonData pair overlays
// the ulButtons union at offset 4.
type rawMouse struct {
	Flags       uint16
	_           uint16
	ButtonFlags uint16
	ButtonData  uint16
	RawButtons  uint32
	LastX       int32
	LastY       int32
	Extra       uint32
}

type rawMouseInput struct {
	Header rawInputHeader
	Mouse  rawMouse
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	Private uint32
}

// inputCounter owns a message-only window on a dedicated, locked OS thread
// and counts WM_INPUT events into atomics that Snapshot drains.
type inputCounter struct {
	keyEvents atomic.Int64
	scrolls   atomic.Int64
	hwnd      uintptr // set before startInputCounter returns; read-only afterwards
	done      chan struct{}
}

// startInputCounter starts the Raw Input thread and waits until it is
// registered or has failed.
func startInputCounter() (*inputCounter, error) {
	ic := &inputCounter{done: make(chan struct{})}
	ready := make(chan error, 1)
	go ic.run(ready)
	if err := <-ready; err != nil {
		<-ic.done
		return nil, err
	}
	return ic, nil
}

// drain returns key presses and scroll/click events since the last call.
func (ic *inputCounter) drain() (keys, scrolls int) {
	presses, carry := pressesFromEvents(ic.keyEvents.Swap(0))
	ic.keyEvents.Add(carry)
	return int(presses), int(ic.scrolls.Swap(0))
}

// close stops the message loop and waits for the thread to clean up.
func (ic *inputCounter) close() {
	_, _, _ = procPostMessageW.Call(ic.hwnd, wmClose, 0, 0)
	<-ic.done
}

func (ic *inputCounter) run(ready chan<- error) {
	// The window, its messages and the device registration all belong to
	// this OS thread. We never unlock it: when the goroutine returns, the
	// runtime retires the thread with it.
	runtime.LockOSThread()
	defer close(ic.done)

	className, err := w32.UTF16PtrFromString(fmt.Sprintf("BurntoutRawInput%p", ic))
	if err != nil {
		ready <- fmt.Errorf("windows: raw input class name: %w", err)
		return
	}
	instance := moduleHandle()
	wc := wndClassEx{
		WndProc:   w32.NewCallback(ic.wndProc),
		Instance:  instance,
		ClassName: className,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 { //nolint:gosec // G103: Win32 interop; the pointer is converted inside the Call expression
		ready <- fmt.Errorf("windows: register raw input class: %w", err)
		return
	}
	defer func() {
		_, _, _ = procUnregisterClassW.Call(uintptr(unsafe.Pointer(className)), instance) //nolint:gosec // G103: Win32 interop; the pointer is converted inside the Call expression
	}()

	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), 0, 0, //nolint:gosec // G103: Win32 interop; the pointer is converted inside the Call expression
		0, 0, 0, 0, hwndMessage, 0, instance, 0)
	if hwnd == 0 {
		ready <- fmt.Errorf("windows: create raw input window: %w", err)
		return
	}
	if err := registerRawInput(hwnd); err != nil {
		_, _, _ = procDestroyWindow.Call(hwnd)
		ready <- err
		return
	}
	defer unregisterRawInput()

	ic.hwnd = hwnd
	ready <- nil
	messageLoop()
}

func (ic *inputCounter) wndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmInput:
		ic.onRawInput(lParam)
		// DefWindowProc must still run so the system frees the input buffer.
	case wmDestroy:
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

func (ic *inputCounter) onRawInput(handle uintptr) {
	var hdr rawInputHeader
	size := uint32(unsafe.Sizeof(hdr))
	r, _, _ := procGetRawInputData.Call(handle, ridHeader,
		uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&size)), unsafe.Sizeof(hdr)) //nolint:gosec // G103: Win32 interop; the pointer is converted inside the Call expression
	if r == 0 || r == ^uintptr(0) {
		return
	}
	switch hdr.Type {
	case rimKeyboard:
		// Header only; see the comment at the top of this file.
		ic.keyEvents.Add(1)
	case rimMouse:
		var in rawMouseInput
		size = uint32(unsafe.Sizeof(in))
		r, _, _ = procGetRawInputData.Call(handle, ridInput,
			uintptr(unsafe.Pointer(&in)), uintptr(unsafe.Pointer(&size)), unsafe.Sizeof(in.Header)) //nolint:gosec // G103: Win32 interop; the pointer is converted inside the Call expression
		if r == 0 || r == ^uintptr(0) {
			return
		}
		if n := countMouseFlags(in.Mouse.ButtonFlags); n > 0 {
			ic.scrolls.Add(int64(n))
		}
	}
}

func registerRawInput(hwnd uintptr) error {
	devs := [...]rawInputDevice{
		{UsagePage: usagePage, Usage: usageKbd, Flags: ridevSink, Target: hwnd},
		{UsagePage: usagePage, Usage: usageMouse, Flags: ridevSink, Target: hwnd},
	}
	r, _, err := procRegisterRawInputDevices.Call(uintptr(unsafe.Pointer(&devs[0])), //nolint:gosec // G103: Win32 interop; the pointer is converted inside the Call expression
		uintptr(len(devs)), unsafe.Sizeof(devs[0]))
	if r == 0 {
		return fmt.Errorf("windows: register raw input devices: %w", err)
	}
	return nil
}

func unregisterRawInput() {
	devs := [...]rawInputDevice{
		{UsagePage: usagePage, Usage: usageKbd, Flags: ridevRemove},
		{UsagePage: usagePage, Usage: usageMouse, Flags: ridevRemove},
	}
	_, _, _ = procRegisterRawInputDevices.Call(uintptr(unsafe.Pointer(&devs[0])), //nolint:gosec // G103: Win32 interop; the pointer is converted inside the Call expression
		uintptr(len(devs)), unsafe.Sizeof(devs[0]))
}

// messageLoop pumps messages until WM_QUIT (GetMessage returns 0) or an
// error (-1).
func messageLoop() {
	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0) //nolint:gosec // G103: Win32 interop; the pointer is converted inside the Call expression
		if int32(r) <= 0 {                                                    //nolint:gosec // G115: GetMessage returns a BOOL that is -1 on error
			return
		}
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m))) //nolint:gosec // G103: Win32 interop; the pointer is converted inside the Call expression
	}
}
