// Package keystore keeps small secrets (the identity HMAC key, the ICS URL)
// in the OS keystore, falling back to owner-only files when no keystore is
// available (for example Linux without a Secret Service).
// See docs/adr/0007-data-protection-at-rest.md.
package keystore

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/kshitijdhara/burntout/internal/adapters/paths"
	"github.com/kshitijdhara/burntout/internal/domain/model"
)

// Service is the keyring service name.
const Service = "burntout"

// LegacyService is the keyring service name from before the rename to
// BurntOut.
const LegacyService = "burnout"

// DeleteLegacy removes the named secrets from LegacyService in the OS
// keyring, for `uninstall --purge`. Missing secrets, or no OS keyring, are
// not errors.
func DeleteLegacy(names ...string) error {
	var errs []error
	for _, name := range names {
		err := keyring.Delete(LegacyService, name)
		if err != nil && !errors.Is(err, keyring.ErrNotFound) && probeKeyring(probeTimeout) {
			errs = append(errs, fmt.Errorf("keystore: delete old %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// SecretSize is the size of generated secrets in bytes.
const SecretSize = 32

// BackendFile names the file fallback.
const BackendFile = "file"

const probeName = "probe"

var reName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// Store holds named secrets in one backend, chosen once per install.
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

// ErrCorruptMarker means the recorded backend choice is unreadable. It is
// never treated as a choice, because guessing could switch backends and
// with them the HMAC key.
var ErrCorruptMarker = errors.New("keystore: corrupt backend marker")

// New returns a Store backed by the OS keyring if it works, otherwise by
// files under <dir>/keys. The first choice is recorded and reused.
func New(dir string, opts ...Option) (*Store, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return newStore(dir, o, func() bool { return probeKeyring(probeTimeout) })
}

func newStore(dir string, o options, probe func() bool) (*Store, error) {
	fb, err := newFileBackend(filepath.Join(dir, "keys"))
	if err != nil {
		return nil, err
	}
	if o.fileOnly {
		return &Store{backend: fb}, nil
	}
	marker, err := fb.get(backendMarker)
	if errors.Is(err, errNotFound) {
		choice := markerFile
		if probe() {
			choice = markerOS
		}
		// set keeps an existing marker, so when two processes race on first
		// run both end up reading (and using) whichever choice won.
		if err := fb.set(backendMarker, choice); err != nil {
			return nil, err
		}
		marker, err = fb.get(backendMarker)
	}
	if err != nil {
		return nil, err
	}
	switch marker {
	case markerOS:
		return &Store{backend: osKeyring{}}, nil
	case markerFile:
		return &Store{backend: fb}, nil
	default:
		return nil, ErrCorruptMarker
	}
}

// Exists reports whether a store was set up in dir (a backend was chosen),
// so read-only views can skip New, whose first run probes the OS keyring
// and may prompt.
func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "keys", backendMarker))
	return err == nil
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
	// Never hand out a short or empty key: an empty HMAC key turns every
	// identity hash into a guessable plain hash (privacy & security §4.5).
	if len(b) != SecretSize {
		return model.Sensitive[[]byte]{}, fmt.Errorf("keystore: %s is %d bytes, want %d; refusing to use it", name, len(b), SecretSize)
	}
	return model.NewSensitive(b), nil
}

// maxValue bounds what Put stores; a calendar feed URL is well under it.
const maxValue = 4096

// Put stores a secret the user supplied (such as an ICS URL) under name,
// replacing any earlier value.
func (s *Store) Put(name string, value model.Sensitive[string]) error {
	if !reName.MatchString(name) {
		return fmt.Errorf("keystore: invalid name %q", name)
	}
	if v := value.Reveal(); v == "" || len(v) > maxValue {
		return fmt.Errorf("keystore: %s must be 1 to %d bytes", name, maxValue)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.backend.del(name); err != nil && !errors.Is(err, errNotFound) {
		return err
	}
	return s.backend.set(name, value.Reveal())
}

// Get returns the named secret; ok is false when there is none.
func (s *Store) Get(name string) (model.Sensitive[string], bool, error) {
	if !reName.MatchString(name) {
		return model.Sensitive[string]{}, false, fmt.Errorf("keystore: invalid name %q", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.backend.get(name)
	if errors.Is(err, errNotFound) {
		return model.Sensitive[string]{}, false, nil
	}
	if err != nil {
		return model.Sensitive[string]{}, false, err
	}
	return model.NewSensitive(v), true, nil
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
		return adoptLegacy(name)
	}
	if err != nil {
		return "", fmt.Errorf("keystore: get %s: %w", name, err)
	}
	return v, nil
}

// adoptLegacy moves name from LegacyService on first use after the rename
// to BurntOut, so a key is never made anew while the old one exists (a new
// HMAC key would change every stored identity). It copies first and
// deletes the old entry only once the copy is stored; a leftover old entry
// is harmless and `uninstall --purge` removes it.
func adoptLegacy(name string) (string, error) {
	v, err := keyring.Get(LegacyService, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errNotFound
	}
	if err != nil {
		return "", fmt.Errorf("keystore: get %s from before the rename: %w", name, err)
	}
	if err := keyring.Set(Service, name, v); err != nil {
		return "", fmt.Errorf("keystore: move %s from before the rename: %w", name, err)
	}
	_ = keyring.Delete(LegacyService, name)
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
	fb := fileBackend{dir: dir}
	fb.removeStaleTemps(time.Now())
	return fb, nil
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
	if err := paths.CheckPrivate(filepath.Join(f.dir, name)); err != nil && !errors.Is(err, paths.ErrUnverified) {
		return "", fmt.Errorf("keystore: %w", err)
	}
	return string(b), nil
}

// tmpPrefix marks in-progress writes. Names with it never match reName.
const tmpPrefix = ".tmp-"

// set writes the value to a private temp file, fsyncs it, then hard-links
// it into place. The link fails if the name exists, so two processes racing
// to create the same secret can't overwrite each other (the loser reads the
// winner's value), and a crash never leaves a half-written secret under the
// real name.
func (f fileBackend) set(name, value string) error {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("keystore: write %s: %w", name, err)
	}
	tmp := tmpPrefix + name + "-" + hex.EncodeToString(suffix)
	err := f.withRoot(func(r *os.Root) error {
		defer func() { _ = r.Remove(tmp) }()
		if err := writeSynced(r, tmp, value); err != nil {
			return err
		}
		return r.Link(tmp, name)
	})
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("keystore: write %s: %w", name, err)
	}
	syncDir(f.dir)
	return nil
}

func writeSynced(r *os.Root, name, value string) error {
	fh, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := fh.WriteString(value); err != nil {
		_ = fh.Close()
		return err
	}
	if err := fh.Sync(); err != nil {
		_ = fh.Close()
		return err
	}
	return fh.Close()
}

// syncDir makes the new directory entry durable. Best effort: Windows can't
// open a directory for syncing, and NTFS journals the entry anyway.
func syncDir(dir string) {
	d, err := os.Open(dir) //nolint:gosec // our own private keys directory
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// removeStaleTemps deletes temp files left by a crash mid-write. Only files
// older than a minute go, so another process's in-flight write is safe.
func (f fileBackend) removeStaleTemps(now time.Time) {
	_ = f.withRoot(func(r *os.Root) error {
		entries, err := fs.ReadDir(r.FS(), ".")
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), tmpPrefix) {
				continue
			}
			if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > time.Minute {
				_ = r.Remove(e.Name())
			}
		}
		return nil
	})
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
