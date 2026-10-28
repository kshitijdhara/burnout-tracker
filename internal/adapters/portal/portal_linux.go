//go:build linux

package portal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	dest          = "org.freedesktop.portal.Desktop"
	object        = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	method        = "org.freedesktop.portal.OpenURI.OpenFile"
	requestIface  = "org.freedesktop.portal.Request"
	requestPrefix = "/org/freedesktop/portal/desktop/request/"
)

// Timeouts, variables so tests can shorten them.
var (
	// callTimeout bounds the call itself, which may first have to start
	// xdg-desktop-portal and its backend through D-Bus activation.
	callTimeout = 15 * time.Second
	// answerTimeout bounds the wait for the portal's answer, which comes
	// once the app has been started, or the user has picked one.
	answerTimeout = 30 * time.Second
)

// OpenFile asks the desktop portal to open path with its default app and
// waits for the outcome: nil once it opened, ErrUnavailable when the
// session bus, the portal or descriptor passing is missing, ErrCancelled
// when the user closed the app chooser, ErrNoAnswer when the portal took
// the request but didn't answer in time, and another error when it
// failed.
func OpenFile(ctx context.Context, path string) error {
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = conn.Close() }()
	if !conn.SupportsUnixFDs() {
		return fmt.Errorf("%w: the session bus can't pass files", ErrUnavailable)
	}
	f, err := os.Open(path) //nolint:gosec // our own launch file, from the data folder
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // the portal holds its own copy once it answers
	fd := f.Fd()
	if fd > math.MaxInt32 { // can't happen (descriptors are small); D-Bus carries them as int32
		return fmt.Errorf("%w: descriptor %d out of range", ErrUnavailable, fd)
	}

	// The answer comes as a signal on a request object whose path follows
	// from our bus name and a token we choose. Listen before calling, so the
	// answer can't arrive before we do.
	token, err := handleToken()
	if err != nil {
		return err
	}
	want := requestPath(conn.Names()[0], token)
	answers := make(chan *dbus.Signal, 4)
	conn.Signal(answers)
	if err := conn.AddMatchSignal(dbus.WithMatchInterface(requestIface), dbus.WithMatchMember("Response")); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	var handle dbus.ObjectPath
	call := conn.Object(dest, object).CallWithContext(cctx, method, 0,
		"", dbus.UnixFD(int32(fd)), map[string]dbus.Variant{"handle_token": dbus.MakeVariant(token)})
	if call.Err != nil {
		var e dbus.Error
		if errors.As(call.Err, &e) && (e.Name == "org.freedesktop.DBus.Error.ServiceUnknown" ||
			e.Name == "org.freedesktop.DBus.Error.UnknownMethod" || e.Name == "org.freedesktop.DBus.Error.UnknownObject") {
			return fmt.Errorf("%w: %s", ErrUnavailable, e.Name)
		}
		if errors.Is(call.Err, context.DeadlineExceeded) {
			// It may have got the request and still open the file.
			return fmt.Errorf("%w: %w", ErrNoAnswer, call.Err)
		}
		return fmt.Errorf("portal: open %s: %w", path, call.Err)
	}
	if err := call.Store(&handle); err != nil {
		return fmt.Errorf("portal: open %s: %w", path, err)
	}
	return awaitAnswer(ctx, answers, want, handle)
}

// awaitAnswer waits for the Response signal on want or handle (an older
// portal may name the request differently from what we predicted).
func awaitAnswer(ctx context.Context, answers <-chan *dbus.Signal, want, handle dbus.ObjectPath) error {
	timeout := time.NewTimer(answerTimeout)
	defer timeout.Stop()
	for {
		select {
		case s, ok := <-answers:
			if !ok {
				return ErrNoAnswer
			}
			if s == nil || s.Name != requestIface+".Response" || (s.Path != want && s.Path != handle) || len(s.Body) == 0 {
				continue
			}
			code, _ := s.Body[0].(uint32)
			switch code {
			case 0:
				return nil
			case 1:
				return ErrCancelled
			default:
				return fmt.Errorf("portal: the file couldn't be opened (response %d)", code)
			}
		case <-timeout.C:
			return ErrNoAnswer
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", ErrNoAnswer, ctx.Err())
		}
	}
}

func handleToken() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "burntout" + hex.EncodeToString(b), nil
}

// requestPath is where the portal puts the request for this caller and
// token: the unique bus name without its colon, dots turned into
// underscores.
func requestPath(uniqueName, token string) dbus.ObjectPath {
	sender := strings.ReplaceAll(strings.TrimPrefix(uniqueName, ":"), ".", "_")
	return dbus.ObjectPath(requestPrefix + sender + "/" + token)
}
