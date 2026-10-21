//go:build windows

package procinfo

import (
	"errors"
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// entry finds pid in a fresh process snapshot. Windows has no per-pid
// parent lookup, so each call walks the snapshot; ancestor chains are short.
func entry(pid int) (windows.ProcessEntry32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return windows.ProcessEntry32{}, fmt.Errorf("procinfo: snapshot: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if int(e.ProcessID) == pid {
			return e, nil
		}
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return windows.ProcessEntry32{}, fmt.Errorf("%w: %d", ErrNoProcess, pid)
	}
	return windows.ProcessEntry32{}, fmt.Errorf("procinfo: walk snapshot: %w", err)
}

func parentOf(pid int) (int, error) {
	e, err := entry(pid)
	if err != nil {
		return 0, err
	}
	return int(e.ParentProcessID), nil
}

func nameOf(pid int) (string, error) {
	e, err := entry(pid)
	if err != nil {
		return "", err
	}
	return filepath.Base(windows.UTF16ToString(e.ExeFile[:])), nil
}
