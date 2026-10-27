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
	"strings"
	"sync"
	"time"

	"github.com/kshitijdhara/burntout/internal/domain/model"
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

// maxDirLen bounds the directories Resolve accepts; anything longer is not
// a real working directory.
const maxDirLen = 4096

// maxLookups bounds concurrent filesystem walks in Resolve, so a hung mount
// can strand at most this many goroutines.
const maxLookups = 4

// Roots finds project roots, caching results because shell commands in the
// same directory are frequent.
type Roots struct {
	mu    sync.Mutex
	cache map[string]string

	find    func(dir string) string // findRoot, replaceable in tests
	lookups chan struct{}           // semaphore for Resolve
}

// NewRoots returns an empty project-root cache.
func NewRoots() *Roots {
	return &Roots{cache: make(map[string]string), find: findRoot, lookups: make(chan struct{}, maxLookups)}
}

// Resolve is ProjectRoot for untrusted input on a latency-sensitive path:
// it never blocks longer than timeout. A cache hit returns at once.
// Otherwise the filesystem walk runs in a goroutine; if it doesn't finish in
// time, or maxLookups walks are already in flight (say, a hung NFS mount),
// Resolve returns the cleaned dir itself. The walk keeps running and fills
// the cache when it completes. Relative or oversized dirs return "", which
// callers treat as "no project".
func (r *Roots) Resolve(dir string, timeout time.Duration) string {
	if len(dir) > maxDirLen || !isAbsPath(dir) {
		return ""
	}
	dir = filepath.Clean(dir)
	if root, ok := r.cached(dir); ok {
		return root
	}
	select {
	case r.lookups <- struct{}{}:
	default:
		return dir
	}
	done := make(chan string, 1)
	go func() {
		defer func() { <-r.lookups }()
		done <- r.ProjectRoot(dir)
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case root := <-done:
		return root
	case <-t.C:
		return dir
	}
}

func (r *Roots) cached(dir string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	root, ok := r.cache[dir]
	return root, ok
}

// ProjectRoot returns the nearest ancestor of dir (including dir) that
// contains .git, .hg or .jj, or dir itself when none does. The cache is
// bounded; when full it is cleared rather than tracking recency, which is
// enough for a working set of a few projects.
func (r *Roots) ProjectRoot(dir string) string {
	dir = filepath.Clean(dir)
	if root, ok := r.cached(dir); ok {
		return root
	}

	root := r.find(dir)

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

// isAbsPath accepts native absolute paths and POSIX-style ones ("/c/Users/…"
// from Git Bash on Windows), matching the wire contract's rule.
func isAbsPath(p string) bool {
	return filepath.IsAbs(p) || strings.HasPrefix(p, "/")
}
