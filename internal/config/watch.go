package config

import (
	"bytes"
	"errors"
	"io/fs"
)

// Watcher notices changes to the config file. The daemon checks it every
// second; the file is small, so it is simply read again and compared.
type Watcher struct {
	path string
	last []byte
	err  error // the last read's error: fs.ErrNotExist for no file
}

// NewWatcher watches path from its current contents: the first Check
// reports a change only if the file changes after this.
func NewWatcher(path string) *Watcher {
	w := &Watcher{path: path}
	w.last, w.err = readFile(path)
	return w
}

// Check reports whether the file changed since the last call and, if so,
// its new contents parsed. A file that became invalid or unreadable
// reports the error, and the caller keeps its current settings. A removed
// file means the defaults.
func (w *Watcher) Check() (Config, bool, error) {
	b, err := readFile(w.path)
	if bytes.Equal(b, w.last) && sameErr(err, w.err) {
		return Config{}, false, nil
	}
	w.last, w.err = b, err
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Default(), true, nil
	case err != nil:
		return Config{}, true, err
	}
	c, err := Parse(b)
	return c, true, err
}

func sameErr(a, b error) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return errors.Is(a, fs.ErrNotExist) == errors.Is(b, fs.ErrNotExist)
}
