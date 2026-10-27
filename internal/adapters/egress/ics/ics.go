// Package ics opens a calendar feed: an HTTPS URL or a local .ics file.
// Fetching a URL is one of the three opt-in network calls (privacy &
// security §6): an HTTPS GET of the user's own feed, with certificate
// checks, no cookies and a generic User-Agent. The URL is a bearer secret
// to the whole calendar, so it is never put in an error or a log: errors
// name only the host.
package ics

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/domain/model"
)

// Limits on one fetch.
const (
	MaxBytes     = 20 << 20 // a busy calendar's feed is a few MB
	Timeout      = 60 * time.Second
	maxRedirects = 5
	userAgent    = "burnout-tracker"
)

// ErrTooLarge means the feed is bigger than MaxBytes.
var ErrTooLarge = fmt.Errorf("ics: the feed is larger than %d MB", MaxBytes>>20)

// Source is a validated feed location.
type Source struct {
	url  *url.URL // https, or nil for a file
	path string   // absolute path of a local file
}

// Parse validates a feed location the user gave: https:// or webcal://
// (fetched over HTTPS), or a path to a local file. Plain http:// is
// refused: the URL and the calendar would travel unencrypted.
func Parse(s model.Sensitive[string]) (Source, error) {
	raw := strings.TrimSpace(s.Reveal())
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "webcal://"):
		raw = "https://" + raw[len("webcal://"):]
		fallthrough
	case strings.HasPrefix(lower, "https://"):
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil {
			return Source{}, errors.New("ics: that isn't a valid https:// calendar URL")
		}
		return Source{url: u}, nil
	case strings.HasPrefix(lower, "http://"):
		return Source{}, errors.New("ics: only https:// (or webcal://) calendar URLs are used; http:// would send your calendar unencrypted")
	case strings.Contains(lower, "://"):
		return Source{}, errors.New("ics: give an https:// calendar URL or the path to an .ics file")
	}
	path := raw
	if rest, ok := strings.CutPrefix(path, "~"+string(filepath.Separator)); ok || path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Source{}, err
		}
		path = filepath.Join(home, rest)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Source{}, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return Source{}, errors.New("ics: no such calendar file")
	}
	if !fi.Mode().IsRegular() {
		return Source{}, errors.New("ics: the calendar file isn't a regular file")
	}
	return Source{path: abs}, nil
}

// Remote reports whether opening the source uses the network.
func (s Source) Remote() bool { return s.url != nil }

// Describe names the source without its secret parts: the host for a URL
// (never the path or query, which carry the token), the file name for a
// file.
func (s Source) Describe() string {
	if s.url != nil {
		return s.url.Hostname()
	}
	return filepath.Base(s.path)
}

// Value is the canonical form to store (webcal:// becomes https://).
func (s Source) Value() model.Sensitive[string] {
	if s.url != nil {
		return model.NewSensitive(s.url.String())
	}
	return model.NewSensitive(s.path)
}

// Client returns the HTTP client used for feeds: TLS 1.2 or later with
// certificate checks, proxies from the environment, no cookie jar, and
// redirects only to https.
func Client() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &http.Client{
		Transport: tr,
		Timeout:   Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("redirected away from https")
			}
			return nil
		},
	}
}

// Open returns the feed's contents, at most MaxBytes of them: reading past
// that returns ErrTooLarge. client is used for URLs (nil: Client()).
func Open(ctx context.Context, s Source, client *http.Client) (io.ReadCloser, error) {
	if s.url == nil {
		f, err := os.Open(s.path)
		if err != nil {
			return nil, errors.New("ics: can't read the calendar file")
		}
		return limited(f), nil
	}
	if client == nil {
		client = Client()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("ics: %s: bad request", s.Describe())
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/calendar, */*;q=0.5")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ics: fetching from %s failed: %w", s.Describe(), withoutURL(err))
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("ics: %s answered %s", s.Describe(), resp.Status)
	}
	if resp.ContentLength > MaxBytes {
		_ = resp.Body.Close()
		return nil, ErrTooLarge
	}
	return limited(resp.Body), nil
}

// withoutURL strips the request URL (with its token) from an HTTP error.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return errors.New("timed out")
		}
		return ue.Err
	}
	return err
}

type limitedReader struct {
	r    io.ReadCloser
	left int64
}

func limited(r io.ReadCloser) io.ReadCloser { return &limitedReader{r: r, left: MaxBytes} }

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		// One more byte means the feed is too large; exactly MaxBytes is fine.
		var one [1]byte
		if n, _ := l.r.Read(one[:]); n > 0 {
			return 0, ErrTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}

func (l *limitedReader) Close() error { return l.r.Close() }
