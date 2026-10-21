package keystore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestFileBackend(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, WithFileOnly())
	if err != nil {
		t.Fatal(err)
	}
	if s.Backend() != BackendFile {
		t.Fatalf("backend = %s", s.Backend())
	}
	a, err := s.GetOrCreate("hmac")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Reveal()) != SecretSize {
		t.Fatalf("secret size %d", len(a.Reveal()))
	}
	// A second store (another process) sees the same secret.
	s2, err := New(dir, WithFileOnly())
	if err != nil {
		t.Fatal(err)
	}
	b, err := s2.GetOrCreate("hmac")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Reveal(), b.Reveal()) {
		t.Fatal("secret changed between reads")
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{
			filepath.Join(dir, "keys"): 0o700, filepath.Join(dir, "keys", "hmac"): 0o600,
		} {
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != want {
				t.Errorf("%s mode %v, want %v", path, fi.Mode().Perm(), want)
			}
		}
		// A secret readable by others is refused rather than used.
		if err := os.Chmod(filepath.Join(dir, "keys", "hmac"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetOrCreate("hmac"); err == nil {
			t.Fatal("expected an error for a world-readable key file")
		}
		if err := os.Chmod(filepath.Join(dir, "keys", "hmac"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Delete("hmac"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("hmac"); err != nil {
		t.Fatalf("deleting a missing secret: %v", err)
	}
	c, err := s.GetOrCreate("hmac")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.Reveal(), c.Reveal()) {
		t.Fatal("a recreated secret should be new")
	}
}

func TestInvalidNames(t *testing.T) {
	s, err := New(t.TempDir(), WithFileOnly())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "../x", "Upper", "a/b", "this-name-is-much-too-long-to-be-valid"} {
		if _, err := s.GetOrCreate(name); err == nil {
			t.Errorf("GetOrCreate(%q) accepted", name)
		}
		if err := s.Delete(name); err == nil {
			t.Errorf("Delete(%q) accepted", name)
		}
	}
}

func TestOSKeyringBackend(t *testing.T) {
	keyring.MockInit()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.Backend() == BackendFile {
		t.Fatal("expected the OS keyring backend with a working keyring")
	}
	a, err := s.GetOrCreate("hmac")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.GetOrCreate("hmac")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Reveal(), b.Reveal()) {
		t.Fatal("secret changed between reads")
	}
	if err := s.Delete("hmac"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("hmac"); err != nil {
		t.Fatal(err)
	}
}

func TestFallsBackWhenKeyringFails(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service"))
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.Backend() != BackendFile {
		t.Fatalf("backend = %s, want file", s.Backend())
	}
}
