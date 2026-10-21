//go:build windows

package procinfo

import (
	"errors"
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// snapshotLookup answers queries from one Toolhelp snapshot taken at
// construction. Windows has no per-pid parent lookup, and a snapshot of
// hundreds of processes is costly, so a whole lineage uses one.
type snapshotLookup map[int]windows.ProcessEntry32

func newLookup() (lookup, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("procinfo: snapshot: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	table := make(snapshotLookup)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		table[int(e.ProcessID)] = e
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, fmt.Errorf("procinfo: walk snapshot: %w", err)
	}
	return table, nil
}

func (t snapshotLookup) entry(pid int) (windows.ProcessEntry32, error) {
	e, ok := t[pid]
	if !ok {
		return windows.ProcessEntry32{}, fmt.Errorf("%w: %d", ErrNoProcess, pid)
	}
	return e, nil
}

func (t snapshotLookup) parent(pid int) (int, error) {
	e, err := t.entry(pid)
	if err != nil {
		return 0, err
	}
	return int(e.ParentProcessID), nil
}

func (t snapshotLookup) name(pid int) (string, error) {
	e, err := t.entry(pid)
	if err != nil {
		return "", err
	}
	return filepath.Base(windows.UTF16ToString(e.ExeFile[:])), nil
}
