// Package logfile is the daemon's log file when it runs as a background
// service: size-capped with rotation (system design §11.3, privacy &
// security §10: 3 × 10 MB by default), owner-only. Logs are PII-free by
// construction; this only bounds how much of them is kept.
package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Defaults from the design: three files of 10 MB.
const (
	DefaultMaxBytes = 10 << 20
	DefaultFiles    = 3
)

// Writer appends to path and rotates it to path.1, path.2, … once it would
// exceed maxBytes, keeping files in total. It is safe for concurrent use.
type Writer struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	files    int
	f        *os.File // nil after a failed reopen; the next Write retries
	size     int64
	closed   bool
}

// Open creates the directory (owner-only) and opens path for appending.
func Open(path string, maxBytes int64, files int) (*Writer, error) {
	if maxBytes <= 0 || files < 1 {
		return nil, fmt.Errorf("logfile: invalid limits %d bytes × %d files", maxBytes, files)
	}
	w := &Writer{path: path, maxBytes: maxBytes, files: files}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

// open creates the directory (owner-only) if it is missing and opens path
// for appending.
func (w *Writer) open() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return fmt.Errorf("logfile: %w", err)
	}
	f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("logfile: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("logfile: %w", err)
	}
	w.f, w.size = f, fi.Size()
	return nil
}

// Write appends p, rotating first if p would take the file past the cap.
// If the file can't be reopened after a rotation (the folder was removed,
// say), the line is dropped and the next Write tries again.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	if w.f != nil && w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		_ = w.rotate()
	}
	if w.f == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate shifts path.N-1 → path.N … path → path.1 and starts a new path.
func (w *Writer) rotate() error {
	_ = w.f.Close()
	w.f = nil
	for i := w.files - 1; i >= 1; i-- {
		from := w.path
		if i > 1 {
			from = fmt.Sprintf("%s.%d", w.path, i-1)
		}
		if err := os.Rename(from, fmt.Sprintf("%s.%d", w.path, i)); err != nil && !os.IsNotExist(err) {
			break // keep going with what we have
		}
	}
	if w.files == 1 {
		_ = os.Remove(w.path)
	}
	return w.open()
}

// Close closes the current file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
