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

// New returns a Store backed by the OS keyring if it works, otherwise by
// files under <dir>/keys.
func New(dir string, opts ...Option) (*Store, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if !o.fileOnly && probeKeyring() {
		return &Store{backend: osKeyring{}}, nil
	}
	fb, err := newFileBackend(filepath.Join(dir, "keys"))
	if err != nil {
		return nil, err
	}
	return &Store{backend: fb}, nil
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

// probeKeyring checks the OS keyring end to end. Linux without a Secret
// Service, or a locked keychain in a headless session, fails here.
func probeKeyring() bool {
	if err := keyring.Set(Service, probeName, "ok"); err != nil {
		return false
	}
	v, err := keyring.Get(Service, probeName)
	_ = keyring.Delete(Service, probeName)
	return err == nil && v == "ok"
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
// goes through an os.Root so names can never escape the directory.
type fileBackend struct {
	dir  string
	root *os.Root
}

func newFileBackend(dir string) (fileBackend, error) {
	if err := paths.EnsurePrivateDir(dir); err != nil {
		return fileBackend{}, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fileBackend{}, fmt.Errorf("keystore: open %s: %w", dir, err)
	}
	return fileBackend{dir: dir, root: root}, nil
}

func (f fileBackend) get(name string) (string, error) {
	b, err := f.root.ReadFile(name)
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
	fh, err := f.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("keystore: write %s: %w", name, err)
	}
	if _, err := fh.WriteString(value); err != nil {
		_ = fh.Close()
		return fmt.Errorf("keystore: write %s: %w", name, err)
	}
	if err := fh.Close(); err != nil {
		return fmt.Errorf("keystore: write %s: %w", name, err)
	}
	return nil
}

func (f fileBackend) del(name string) error {
	err := f.root.Remove(name)
	if errors.Is(err, fs.ErrNotExist) {
		return errNotFound
	}
	if err != nil {
		return fmt.Errorf("keystore: delete %s: %w", name, err)
	}
	return nil
}

func (fileBackend) name() string { return BackendFile }
