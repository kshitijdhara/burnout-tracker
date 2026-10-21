// Package identity turns identifying values (project paths, window and
// session identifiers) into keyed, non-reversible keys. Plain hashes are
// never used: low-entropy inputs such as paths can be reversed by guessing.
// See docs/privacy-and-security.md §4.5.
package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"os"
	"path/filepath"
	"sync"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// KeyLen is the length of keys returned by Hasher.Key, in hex characters
// (128 bits).
const KeyLen = 32

// Hasher computes HMAC-SHA256 identity keys with a per-install secret.
// It is safe for concurrent use.
type Hasher struct {
	pool sync.Pool
}

// NewHasher returns a Hasher keyed with key.
func NewHasher(key model.Sensitive[[]byte]) *Hasher {
	k := append([]byte(nil), key.Reveal()...)
	return &Hasher{pool: sync.Pool{New: func() any { return hmac.New(sha256.New, k) }}}
}

// Key returns the hex HMAC of parts, joined with NUL separators so
// ("ab", "c") and ("a", "bc") differ.
func (h *Hasher) Key(parts ...string) string {
	m, _ := h.pool.Get().(hash.Hash)
	defer h.pool.Put(m)
	m.Reset()
	for i, p := range parts {
		if i > 0 {
			m.Write([]byte{0})
		}
		m.Write([]byte(p))
	}
	return hex.EncodeToString(m.Sum(nil))[:KeyLen]
}

// projectMarkers identify the root of a project checkout.
var projectMarkers = []string{".git", ".hg", ".jj"}

const rootCacheSize = 1024

// Roots finds project roots, caching results because shell commands in the
// same directory are frequent.
type Roots struct {
	mu    sync.Mutex
	cache map[string]string
}

// NewRoots returns an empty project-root cache.
func NewRoots() *Roots { return &Roots{cache: make(map[string]string)} }

// ProjectRoot returns the nearest ancestor of dir (including dir) that
// contains .git, .hg or .jj, or dir itself when none does. The cache is
// bounded; when full it is cleared rather than tracking recency, which is
// enough for a working set of a few projects.
func (r *Roots) ProjectRoot(dir string) string {
	dir = filepath.Clean(dir)
	r.mu.Lock()
	if root, ok := r.cache[dir]; ok {
		r.mu.Unlock()
		return root
	}
	r.mu.Unlock()

	root := findRoot(dir)

	r.mu.Lock()
	if len(r.cache) >= rootCacheSize {
		clear(r.cache)
	}
	r.cache[dir] = root
	r.mu.Unlock()
	return root
}

func findRoot(dir string) string {
	for d := dir; ; {
		for _, m := range projectMarkers {
			if _, err := os.Lstat(filepath.Join(d, m)); err == nil {
				return d
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		d = parent
	}
}
