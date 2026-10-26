package keystore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
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

// Once the OS keyring was chosen, a later failure must surface as an error,
// never as a silent switch to files (which would mean a new HMAC key).
func TestBackendChoiceIsSticky(t *testing.T) {
	dir := t.TempDir()
	keyring.MockInit()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Backend() == BackendFile {
		t.Fatal("expected the OS keyring on first run")
	}
	if _, err := s.GetOrCreate("hmac"); err != nil {
		t.Fatal(err)
	}

	keyring.MockInitWithError(errors.New("keychain locked"))
	s2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Backend() == BackendFile {
		t.Fatal("backend silently switched to files")
	}
	if _, err := s2.GetOrCreate("hmac"); err == nil {
		t.Fatal("expected the keyring error to surface")
	}

	// And a file choice stays a file choice even when the keyring recovers.
	fileDir := t.TempDir()
	if _, err := New(fileDir); err != nil {
		t.Fatal(err)
	}
	keyring.MockInit()
	s3, err := New(fileDir)
	if err != nil {
		t.Fatal(err)
	}
	if s3.Backend() != BackendFile {
		t.Fatalf("backend = %s, want file", s3.Backend())
	}
}

// A truncated or empty key file (say, a crash mid-write on an older build)
// must never become a short HMAC key.
func TestRejectsShortSecrets(t *testing.T) {
	for name, content := range map[string]string{"empty": "", "short": "c2hvcnQ=", "not base64": "!!!"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := New(dir, WithFileOnly())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "keys", "hmac"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetOrCreate("hmac"); err == nil {
				t.Fatal("accepted a bad secret")
			}
		})
	}
}

func TestCorruptMarkerIsAnError(t *testing.T) {
	for _, content := range []string{"", "osx", "FILE"} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "keys"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "keys", backendMarker), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newStore(dir, options{}, func() bool { return true }); !errors.Is(err, ErrCorruptMarker) {
			t.Fatalf("marker %q: got %v, want ErrCorruptMarker", content, err)
		}
	}
}

// Two processes on first run: the other one records "file" while we are
// still probing, and our probe says the keyring works. We must adopt the
// recorded choice, not our own.
func TestFirstRunRaceUsesPersistedChoice(t *testing.T) {
	dir := t.TempDir()
	keyring.MockInit()
	probe := func() bool {
		other, err := newStore(dir, options{}, func() bool { return false })
		if err != nil || other.Backend() != BackendFile {
			t.Fatalf("other process: %v %v", other, err)
		}
		return true
	}
	s, err := newStore(dir, options{}, probe)
	if err != nil {
		t.Fatal(err)
	}
	if s.Backend() != BackendFile {
		t.Fatalf("backend = %s, want the persisted file choice", s.Backend())
	}
}

func TestStaleTempFilesRemoved(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(dir, WithFileOnly()); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "keys", tmpPrefix+"hmac-dead")
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir, WithFileOnly()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale temp file still present: %v", err)
	}
}

func TestPutGet(t *testing.T) {
	for name, opts := range map[string][]Option{"file": {WithFileOnly()}, "os": nil} {
		t.Run(name, func(t *testing.T) {
			if name == "os" {
				keyring.MockInit()
			}
			s, err := New(t.TempDir(), opts...)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok, err := s.Get("ics-url"); ok || err != nil {
				t.Fatalf("empty store: %v %v", ok, err)
			}
			for _, v := range []string{"https://cal.example/a.ics?token=1", "https://cal.example/b.ics?token=2"} {
				if err := s.Put("ics-url", model.NewSensitive(v)); err != nil {
					t.Fatal(err)
				}
				if got, ok, err := s.Get("ics-url"); !ok || err != nil || got.Reveal() != v {
					t.Fatalf("get after put %q: %q %v %v", v, got.Reveal(), ok, err)
				}
			}
			for _, bad := range []string{"", strings.Repeat("x", maxValue+1)} {
				if err := s.Put("ics-url", model.NewSensitive(bad)); err == nil {
					t.Errorf("put of %d bytes accepted", len(bad))
				}
			}
			if err := s.Delete("ics-url"); err != nil {
				t.Fatal(err)
			}
			if _, ok, _ := s.Get("ics-url"); ok {
				t.Error("deleted value still there")
			}
		})
	}
}
