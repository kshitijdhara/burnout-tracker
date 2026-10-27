package wayland

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// shortDir is a private directory with a path short enough for a Unix
// socket (t.TempDir on macOS is longer than sun_path allows).
func shortDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the bridge is Linux-only")
	}
	dir, err := os.MkdirTemp("", "b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // directories need the search bit
		t.Fatal(err)
	}
	return dir
}

// fakeExtension serves the bridge protocol the way extension.js does,
// answering each request with reply(req) after a good hello.
func fakeExtension(t *testing.T, dir string, reply func(request) string) func() []request {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(dir, SocketName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var (
		mu   sync.Mutex
		seen []request
	)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			r := bufio.NewReader(conn)
			for {
				line, err := r.ReadBytes('\n')
				if err != nil {
					break
				}
				var req request
				_ = json.Unmarshal(line, &req)
				mu.Lock()
				seen = append(seen, req)
				mu.Unlock()
				out := reply(req)
				if req.Op == "hello" {
					want, _ := os.ReadFile(filepath.Join(dir, TokenName)) //nolint:gosec // G304: test dir
					if req.Token != string(want) {
						out = `{"v":1,"op":"error","error":"bad token"}`
					}
				}
				if _, err := conn.Write([]byte(out + "\n")); err != nil {
					break
				}
			}
			_ = conn.Close()
		}
	}()
	return func() []request {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
}

const helloReply = `{"v":1,"op":"hello","extension":"1","shell":"48.2"}`

func standard(req request) string {
	if req.Op == "hello" {
		return helloReply
	}
	return `{"v":1,"op":"snapshot","windows":[],"monitors":[{"x":0,"y":0,"w":10,"h":10}],"focus":0,"pointer":{"x":1,"y":2}}`
}

func TestClientHelloAndSnapshot(t *testing.T) {
	dir := shortDir(t)
	seen := fakeExtension(t, dir, standard)
	c, err := dial(context.Background(), dir, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.close() }()
	if c.hello.Shell != "48.2" || c.hello.Extension != "1" {
		t.Errorf("hello = %+v", c.hello)
	}
	for range 2 {
		r, err := c.snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if r.Pointer.X != 1 || len(r.Monitors) != 1 {
			t.Errorf("reply = %+v", r)
		}
	}
	got := seen()
	if len(got) != 3 || got[0].Op != "hello" || !got[0].Titles || got[0].V != Version || got[1].Op != "snapshot" || got[1].Token != "" {
		t.Errorf("requests = %+v", got)
	}
}

func TestClientTitlesOffByDefault(t *testing.T) {
	dir := shortDir(t)
	seen := fakeExtension(t, dir, standard)
	c, err := dial(context.Background(), dir, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.close()
	if seen()[0].Titles {
		t.Error("hello asked for titles with title classification off")
	}
}

func TestClientRefusedAndVersions(t *testing.T) {
	tests := []struct {
		name  string
		hello string
		check func(error) bool
	}{
		{"refused", `{"v":1,"op":"error","error":"bad token"}`, func(err error) bool {
			var re *RefusedError
			return errors.As(err, &re) && re.Reason == "bad token"
		}},
		{"too new", `{"v":99,"op":"hello"}`, func(err error) bool {
			var ve *VersionError
			return errors.As(err, &ve) && ve.Got == 99
		}},
		{"wrong op", standard(request{Op: "snapshot"}), func(err error) bool { return errors.Is(err, errMalformed) }},
		{"oversized", `{"v":1,"op":"hello","shell":"` + strings.Repeat("x", MaxReplyBytes) + `"}`, func(err error) bool {
			return errors.Is(err, errMalformed)
		}},
	}
	for _, tc := range tests {
		dir := shortDir(t)
		fakeExtension(t, dir, func(request) string { return tc.hello })
		_, err := dial(context.Background(), dir, false, nil)
		if err == nil || !tc.check(err) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// TestClientReadsTokenEachDial: every dial reads the token file, so a
// token another agent process wrote is used rather than overwritten.
func TestClientReadsTokenEachDial(t *testing.T) {
	dir := shortDir(t)
	fakeExtension(t, dir, standard)
	c, err := dial(context.Background(), dir, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.close()
	if err := os.WriteFile(filepath.Join(dir, TokenName), []byte(strings.Repeat("0", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := dial(context.Background(), dir, false, nil); err != nil {
		t.Fatalf("a valid replacement token should be used as is: %v", err)
	} else {
		_ = c.close()
	}
}

func TestClientNotRunning(t *testing.T) {
	dir := shortDir(t)
	if _, err := dial(context.Background(), dir, false, nil); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("no listener: %v", err)
	}
}

func TestClientVerifyRejects(t *testing.T) {
	dir := shortDir(t)
	seen := fakeExtension(t, dir, standard)
	_, err := dial(context.Background(), dir, false, func(*net.UnixConn) error { return errors.New("not gnome-shell") })
	if err == nil || !strings.Contains(err.Error(), "not gnome-shell") {
		t.Fatalf("verify ignored: %v", err)
	}
	if len(seen()) != 0 {
		t.Error("the token was sent before the peer was verified")
	}
}

func TestEnsureToken(t *testing.T) {
	dir := shortDir(t)
	a, err := ensureToken(dir)
	if err != nil || !reToken.MatchString(a) {
		t.Fatalf("token %q, %v", a, err)
	}
	fi, err := os.Lstat(filepath.Join(dir, TokenName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v, %v", fi.Mode(), err)
	}
	if b, _ := ensureToken(dir); b != a {
		t.Error("a valid token was rotated")
	}
	// Loosened permissions or damage: replaced.
	path := filepath.Join(dir, TokenName)
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // testing a world-readable token
		t.Fatal(err)
	}
	c, err := ensureToken(dir)
	if err != nil || c == a {
		t.Errorf("world-readable token reused: %v", err)
	}
	if err := os.WriteFile(path, []byte("not a token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if d, err := ensureToken(dir); err != nil || !reToken.MatchString(d) {
		t.Errorf("damaged token not replaced: %q, %v", d, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".token-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}
