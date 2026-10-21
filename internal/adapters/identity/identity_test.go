package identity

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

func TestHasher(t *testing.T) {
	h1 := NewHasher(model.NewSensitive([]byte("key-one")))
	h2 := NewHasher(model.NewSensitive([]byte("key-two")))
	const path = "/home/alice/secret-project"

	a := h1.Key(path)
	if len(a) != KeyLen {
		t.Fatalf("key length %d", len(a))
	}
	if a != h1.Key(path) {
		t.Fatal("key is not deterministic")
	}
	if a == h2.Key(path) {
		t.Fatal("different install keys produced the same identity")
	}
	if h1.Key("ab", "c") == h1.Key("a", "bc") {
		t.Fatal("part boundaries are ambiguous")
	}
	if strings.Contains(a, "alice") || strings.Contains(a, "secret") {
		t.Fatal("key contains the input")
	}
}

func TestHasherConcurrent(t *testing.T) {
	h := NewHasher(model.NewSensitive([]byte("k")))
	want := h.Key("x")
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 200 {
				if h.Key("x") != want {
					t.Error("inconsistent key under concurrency")
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestProjectRoot(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	deep := filepath.Join(repo, "a", "b")
	worktree := filepath.Join(tmp, "worktree")
	plain := filepath.Join(tmp, "plain", "x")
	for _, d := range []string{filepath.Join(repo, ".git"), deep, worktree, plain} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Git worktrees and submodules have a .git file, not a directory.
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: x"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewRoots()
	tests := map[string]string{deep: repo, repo: repo, worktree: worktree, plain: plain}
	for in, want := range tests {
		if got := r.ProjectRoot(in); got != want {
			t.Errorf("ProjectRoot(%s) = %s, want %s", in, got, want)
		}
		// Second call is served from the cache.
		if got := r.ProjectRoot(in); got != want {
			t.Errorf("cached ProjectRoot(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestProjectRootCacheBounded(t *testing.T) {
	r := NewRoots()
	dir := t.TempDir()
	for i := range rootCacheSize + 10 {
		r.ProjectRoot(filepath.Join(dir, strings.Repeat("x", i%7), string(rune('a'+i%26)), filepath.Base(dir)+string(rune(i))))
	}
	if len(r.cache) > rootCacheSize {
		t.Fatalf("cache grew to %d", len(r.cache))
	}
}
