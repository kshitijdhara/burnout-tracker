package logfile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRotationKeepsBoundedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "daemon.log")
	w, err := Open(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 39) + "\n" // 40 bytes: two lines fit in 100
	for range 20 {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(path + "*")
	if len(files) != 3 {
		t.Fatalf("files = %v, want daemon.log, .1 and .2", files)
	}
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size() > 100 {
			t.Errorf("%s is %d bytes, over the cap", f, fi.Size())
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", f, fi.Mode().Perm())
		}
	}
	if fi, _ := os.Stat(filepath.Dir(path)); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
		t.Errorf("log dir mode %v, want 0700", fi.Mode().Perm())
	}
}

func TestAppendsAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	for _, s := range []string{"first\n", "second\n"} {
		w, err := Open(path, DefaultMaxBytes, DefaultFiles)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
		_ = w.Close()
	}
	if b, _ := os.ReadFile(path); string(b) != "first\nsecond\n" {
		t.Fatalf("log = %q", b)
	}
}

func TestOversizedLineStillWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	w, err := Open(path, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if n, err := w.Write([]byte(strings.Repeat("y", 50))); err != nil || n != 50 {
		t.Fatalf("wrote %d, %v", n, err)
	}
}
