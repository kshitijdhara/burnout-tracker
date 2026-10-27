package wayland

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/adapters/paths"
)

// requestTimeout bounds one request and its reply, so a busy or hung
// GNOME Shell can't stall the 1 s tick. helloTimeout allows for the
// extension reading the token file.
const (
	requestTimeout = 250 * time.Millisecond
	helloTimeout   = time.Second
)

// ErrNotRunning means nothing listens on the bridge socket: the extension
// isn't installed, isn't enabled, or GNOME hasn't loaded it yet.
var ErrNotRunning = errors.New("gnome bridge: the extension is not answering")

var reToken = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ensureToken returns the bridge token in dir, creating it if needed. The
// token is shared by every agent process (the daemon, doctor), so it is
// created once, never rotated while valid; $XDG_RUNTIME_DIR is cleared at
// logout, which gives a new one per login. It is published with a hard
// link, so a reader never sees a half-written file.
func ensureToken(dir string) (string, error) {
	if err := paths.EnsurePrivateDir(dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, TokenName)
	for range 3 {
		if tok, err := readToken(path); err == nil {
			return tok, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			// Wrong mode, a symlink or a damaged file: replace it.
			_ = os.Remove(path)
		}
		err := writeToken(dir, path)
		if err == nil || errors.Is(err, os.ErrExist) {
			continue // ours, or another process won the race: read it back
		}
		return "", err
	}
	return "", errors.New("gnome bridge: cannot create a token")
}

func readToken(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 || fi.Size() != 64 {
		return "", errors.New("gnome bridge: token file is not an owner-only 64-byte file")
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: inside our own 0700 runtime dir
	if err != nil {
		return "", err
	}
	if !reToken.Match(b) {
		return "", errors.New("gnome bridge: token file is damaged")
	}
	return string(b), nil
}

func writeToken(dir, path string) error {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	_, werr := f.WriteString(hex.EncodeToString(raw[:]))
	if err := errors.Join(werr, f.Close()); err != nil {
		return err
	}
	return os.Link(tmp, path)
}

// client is one authenticated connection to the extension.
type client struct {
	conn  net.Conn
	r     *bufio.Reader
	hello reply
}

// dial connects to the bridge, checks who is listening, and says hello.
// verify checks the listening peer; it is nil only in tests.
func dial(ctx context.Context, dir string, titles bool, verify func(*net.UnixConn) error) (*client, error) {
	// The token goes first: the extension reads it to check the hello.
	token, err := ensureToken(dir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", filepath.Join(dir, SocketName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, ErrNotRunning
		}
		return nil, fmt.Errorf("gnome bridge: %w", err)
	}
	if verify != nil {
		uc, ok := conn.(*net.UnixConn)
		if !ok {
			_ = conn.Close()
			return nil, errors.New("gnome bridge: not a unix socket")
		}
		if err := verify(uc); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	c := &client{conn: conn, r: bufio.NewReaderSize(conn, MaxReplyBytes+1)}
	r, err := c.roundTrip(ctx, request{V: Version, Op: "hello", Token: token, Titles: titles})
	if err == nil && r.Op != "hello" {
		err = fmt.Errorf("%w: %s in reply to hello", errMalformed, r.Op)
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	c.hello = r
	return c, nil
}

// snapshot asks for one snapshot.
func (c *client) snapshot(ctx context.Context) (reply, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	r, err := c.roundTrip(ctx, request{V: Version, Op: "snapshot"})
	if err == nil && r.Op != "snapshot" {
		err = fmt.Errorf("%w: %s in reply to snapshot", errMalformed, r.Op)
	}
	return r, err
}

// roundTrip sends one request and reads its reply. Any error leaves the
// connection out of step, so the caller closes it.
func (c *client) roundTrip(ctx context.Context, req request) (reply, error) {
	deadline, _ := ctx.Deadline()
	if err := c.conn.SetDeadline(deadline); err != nil {
		return reply{}, err
	}
	b, err := json.Marshal(req)
	if err != nil {
		return reply{}, err
	}
	if _, err := c.conn.Write(append(b, '\n')); err != nil {
		return reply{}, fmt.Errorf("gnome bridge: send: %w", err)
	}
	line, err := c.r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return reply{}, fmt.Errorf("%w: longer than %d bytes", errMalformed, MaxReplyBytes)
	}
	if err != nil {
		return reply{}, fmt.Errorf("gnome bridge: read: %w", err)
	}
	r, err := decodeReply(line[:len(line)-1])
	if err != nil {
		return reply{}, err
	}
	if r.Op == "error" {
		return reply{}, &RefusedError{Reason: r.Error}
	}
	return r, nil
}

func (c *client) close() error { return c.conn.Close() }
