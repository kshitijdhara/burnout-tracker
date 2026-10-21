// Package keystore keeps small secrets (the identity HMAC key, the ICS URL)
// in the OS keystore, falling back to owner-only files when no keystore is
// available (for example Linux without a Secret Service).
// See docs/adr/0007-data-protection-at-rest.md.
package keystore

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// Service is the keyring service name.
const Service = "burnout"

// SecretSize is the size of generated secrets in bytes.
const SecretSize = 32

// BackendFile names the file fallback.
const BackendFile = "file"

const probeName = "probe"

var reName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// Store implements ports.KeyStore.
type Store struct {
	mu      sync.Mutex
	backend backend
}

type backend interface {
	get(name string) (string, error) // returns errNotFound when missing
	set(name, value string) error
	del(name string) error
	name() string
}

var errNotFound = errors.New("keystore: not found")

// Option configures New.
type Option func(*options)

type options struct{ fileOnly bool }

// WithFileOnly skips the OS keyring and always uses the file backend.
func WithFileOnly() Option { return func(o *options) { o.fileOnly = true } }

// backendMarker records which backend was chosen the first time, so later
// runs never silently switch (a switch would mean a new HMAC key and a break
// in every project and window identity).
const backendMarker = "backend"

// Marker values.
const (
	markerOS   = "os"
	markerFile = "file"
)

// probeTimeout bounds the first-run keyring probe. A locked keychain in a
// headless session can block indefinitely waiting for a prompt.
const probeTimeout = 3 * time.Second

// New returns a Store backed by the OS keyring if it works, otherwise by
// files under <dir>/keys. The first choice is recorded and reused.
func New(dir string, opts ...Option) (*Store, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	fb, err := newFileBackend(filepath.Join(dir, "keys"))
	if err != nil {
		return nil, err
	}
	if o.fileOnly {
		return &Store{backend: fb}, nil
	}
	marker, err := fb.get(backendMarker)
	switch {
	case err == nil && marker == markerOS:
		return &Store{backend: osKeyring{}}, nil
	case err == nil:
		return &Store{backend: fb}, nil
	case !errors.Is(err, errNotFound):
		return nil, err
	}
	choice, be := markerFile, backend(fb)
	if probeKeyring(probeTimeout) {
		choice, be = markerOS, osKeyring{}
	}
	if err := fb.set(backendMarker, choice); err != nil {
		return nil, err
	}
	return &Store{backend: be}, nil
}

// GetOrCreate returns the named secret, generating a random one on first use.
func (s *Store) GetOrCreate(name string) (model.Sensitive[[]byte], error) {
	if !reName.MatchString(name) {
		return model.Sensitive[[]byte]{}, fmt.Errorf("keystore: invalid name %q", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	enc, err := s.backend.get(name)
	if errors.Is(err, errNotFound) {
		secret := make([]byte, SecretSize)
		if _, err := rand.Read(secret); err != nil {
			return model.Sensitive[[]byte]{}, fmt.Errorf("keystore: generate: %w", err)
		}
		enc = base64.StdEncoding.EncodeToString(secret)
		if err := s.backend.set(name, enc); err != nil {
			return model.Sensitive[[]byte]{}, err
		}
		// Read back so a concurrent creator (another process) wins consistently.
		enc, err = s.backend.get(name)
	}
	if err != nil {
		return model.Sensitive[[]byte]{}, err
	}
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return model.Sensitive[[]byte]{}, fmt.Errorf("keystore: decode %s: %w", name, err)
	}
	return model.NewSensitive(b), nil
}

// Delete removes the named secret. Deleting a missing secret is not an error.
func (s *Store) Delete(name string) error {
	if !reName.MatchString(name) {
		return fmt.Errorf("keystore: invalid name %q", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.backend.del(name)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

// Backend names the storage in use: "keychain", "wincred", "secret-service"
// or "file".
func (s *Store) Backend() string { return s.backend.name() }

// probeKeyring checks the OS keyring end to end within timeout. Linux
// without a Secret Service, or a locked keychain in a headless session,
// fails here. On timeout the probe goroutine is abandoned; it only runs once
// per install.
func probeKeyring(timeout time.Duration) bool {
	ok := make(chan bool, 1)
	go func() {
		if err := keyring.Set(Service, probeName, "ok"); err != nil {
			ok <- false
			return
		}
		v, err := keyring.Get(Service, probeName)
		_ = keyring.Delete(Service, probeName)
		ok <- err == nil && v == "ok"
	}()
	select {
	case r := <-ok:
		return r
	case <-time.After(timeout):
		return false
	}
}

type osKeyring struct{}

func (osKeyring) get(name string) (string, error) {
	v, err := keyring.Get(Service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errNotFound
	}
	if err != nil {
		return "", fmt.Errorf("keystore: get %s: %w", name, err)
	}
	return v, nil
}

func (osKeyring) set(name, value string) error {
	if err := keyring.Set(Service, name, value); err != nil {
		return fmt.Errorf("keystore: set %s: %w", name, err)
	}
	return nil
}

func (osKeyring) del(name string) error {
	err := keyring.Delete(Service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return errNotFound
	}
	if err != nil {
		return fmt.Errorf("keystore: delete %s: %w", name, err)
	}
	return nil
}

func (osKeyring) name() string {
	switch runtime.GOOS {
	case "darwin":
		return "keychain"
	case "windows":
		return "wincred"
	default:
		return "secret-service"
	}
}

// fileBackend stores one secret per 0600 file in a 0700 directory. It only
// protects against other local users; the doctor command says so. Access
// goes through an os.Root so names can never escape the directory. The root
// is opened per operation, so no handle outlives a call (Windows can't
// delete a directory with an open handle).
type fileBackend struct {
	dir string
}

func newFileBackend(dir string) (fileBackend, error) {
	if err := paths.EnsurePrivateDir(dir); err != nil {
		return fileBackend{}, err
	}
	return fileBackend{dir: dir}, nil
}

func (f fileBackend) withRoot(fn func(*os.Root) error) error {
	root, err := os.OpenRoot(f.dir)
	if err != nil {
		return fmt.Errorf("keystore: open %s: %w", f.dir, err)
	}
	defer func() { _ = root.Close() }()
	return fn(root)
}

func (f fileBackend) get(name string) (string, error) {
	var b []byte
	err := f.withRoot(func(r *os.Root) (err error) { b, err = r.ReadFile(name); return err })
	if errors.Is(err, fs.ErrNotExist) {
		return "", errNotFound
	}
	if err != nil {
		return "", fmt.Errorf("keystore: read %s: %w", name, err)
	}
	if err := paths.CheckPrivate(filepath.Join(f.dir, name)); err != nil {
		return "", fmt.Errorf("keystore: %w", err)
	}
	return string(b), nil
}

// set creates the file exclusively so two processes racing to create the
// same secret can't overwrite each other; the loser reads the winner's value.
func (f fileBackend) set(name, value string) error {
	err := f.withRoot(func(r *os.Root) error {
		fh, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := fh.WriteString(value); err != nil {
			_ = fh.Close()
			return err
		}
		return fh.Close()
	})
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("keystore: write %s: %w", name, err)
	}
	return nil
}

func (f fileBackend) del(name string) error {
	err := f.withRoot(func(r *os.Root) error { return r.Remove(name) })
	if errors.Is(err, fs.ErrNotExist) {
		return errNotFound
	}
	if err != nil {
		return fmt.Errorf("keystore: delete %s: %w", name, err)
	}
	return nil
}

func (fileBackend) name() string { return BackendFile }
