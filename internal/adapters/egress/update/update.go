// Package update finds, verifies and installs burnout releases (ADR-0005,
// "burnout update"). Checking for one is one of the three opt-in network
// calls (privacy & security §6): HTTPS GETs of this repository's GitHub
// Releases API and of release assets, with certificate checks, no cookies,
// no token and a generic User-Agent. Every request, redirects included,
// must be https to one of a fixed set of GitHub hosts. Errors name only
// the host.
//
// Nothing is trusted until it is verified: checksums.txt must carry a
// signature from a key pinned in internal/release, and an asset must match
// its line in checksums.txt, before anything on disk changes.
package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/release"
)

// Repo is the GitHub repository releases come from.
const Repo = "kshitijdhara/burnout-tracker"

// Hosts are the only hosts the updater connects to. github.com redirects
// asset downloads to a githubusercontent.com CDN host: objects… was used
// until 2025, release-assets… since.
var Hosts = []string{
	"api.github.com",
	"github.com",
	"objects.githubusercontent.com",
	"release-assets.githubusercontent.com",
}

// Limits on one check or download.
const (
	Timeout       = 10 * time.Minute // the whole update, downloads included
	maxAPIBytes   = 4 << 20
	maxSumsBytes  = 64 << 10
	maxSigBytes   = 4 << 10
	MaxAssetBytes = 256 << 20 // the binary is ~20 MB; the dmg a little more
	maxRedirects  = 5
	userAgent     = "burnout-tracker"
	betaPageSize  = 30
)

// Channel picks which releases count: stable ones, or pre-releases too.
type Channel string

// The channels.
const (
	Stable Channel = "stable"
	Beta   Channel = "beta"
)

// ErrNoRelease means the channel has no release yet, or no such tag.
var ErrNoRelease = errors.New("update: no such release")

// Updater talks to GitHub. The zero value isn't usable: call New.
type Updater struct {
	client *http.Client
	api    string // base URL of the Releases API
	repo   string
	verify func(checksums, sig []byte) error
}

// New returns an Updater for this repository's releases, verified against
// the pinned keys.
func New() *Updater {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &Updater{
		client: newClient(tr, Hosts),
		api:    "https://api.github.com",
		repo:   Repo,
		verify: release.VerifyChecksums,
	}
}

// newClient wraps tr so it only reaches hosts over https, and follows
// redirects only within them.
func newClient(tr http.RoundTripper, hosts []string) *http.Client {
	allowed := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		allowed[h] = true
	}
	return &http.Client{
		Transport: guard{next: tr, hosts: allowed},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			return nil // guard checks where it goes
		},
	}
}

// guard refuses any request that isn't https to an allowed host.
type guard struct {
	next  http.RoundTripper
	hosts map[string]bool
}

func (g guard) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || !g.hosts[req.URL.Hostname()] {
		return nil, fmt.Errorf("refusing to connect to %s: only https to GitHub is allowed", req.URL.Hostname())
	}
	return g.next.RoundTrip(req)
}

// Release is one GitHub release, as far as the updater needs it.
type Release struct {
	Version Version
	Tag     string
	assets  map[string]asset
}

type asset struct {
	url  string
	size int64
}

// Has reports whether the release has an asset with this name.
func (r Release) Has(name string) bool {
	_, ok := r.assets[name]
	return ok
}

// ghRelease is the part of the API's release object we read.
type ghRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

func (g ghRelease) release() (Release, error) {
	v, err := ParseVersion(g.TagName)
	if err != nil || !strings.HasPrefix(g.TagName, "v") {
		return Release{}, fmt.Errorf("update: release tag %.40q isn't a version", g.TagName)
	}
	r := Release{Version: v, Tag: g.TagName, assets: make(map[string]asset, len(g.Assets))}
	for _, a := range g.Assets {
		r.assets[a.Name] = asset{url: a.URL, size: a.Size}
	}
	return r, nil
}

// Latest returns the newest release on ch: GitHub's latest release for
// Stable, or the highest version among recent releases, pre-releases
// included, for Beta.
func (u *Updater) Latest(ctx context.Context, ch Channel) (Release, error) {
	if ch != Beta {
		var g ghRelease
		if err := u.getJSON(ctx, "/releases/latest", &g); err != nil {
			return Release{}, err
		}
		return g.release()
	}
	var list []ghRelease
	if err := u.getJSON(ctx, fmt.Sprintf("/releases?per_page=%d", betaPageSize), &list); err != nil {
		return Release{}, err
	}
	var best Release
	found := false
	for _, g := range list {
		r, err := g.release()
		if g.Draft || err != nil {
			continue
		}
		if !found || r.Version.Compare(best.Version) > 0 {
			best, found = r, true
		}
	}
	if !found {
		return Release{}, ErrNoRelease
	}
	return best, nil
}

// Tag returns the release of version v.
func (u *Updater) Tag(ctx context.Context, v Version) (Release, error) {
	var g ghRelease
	if err := u.getJSON(ctx, "/releases/tags/v"+url.PathEscape(v.String()), &g); err != nil {
		return Release{}, err
	}
	return g.release()
}

func (u *Updater) getJSON(ctx context.Context, path string, into any) error {
	body, err := u.get(ctx, u.api+"/repos/"+u.repo+path, "application/vnd.github+json", maxAPIBytes)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	if err := json.NewDecoder(body).Decode(into); err != nil {
		if errors.Is(err, errTooLarge) {
			return err
		}
		return errors.New("update: GitHub sent a release list that couldn't be read")
	}
	return nil
}

// Checksums fetches r's checksums.txt and signature, verifies the
// signature against the pinned keys and returns the SHA-256 of each file.
func (u *Updater) Checksums(ctx context.Context, r Release) (Sums, error) {
	sums, err := u.fetchAsset(ctx, r, release.ChecksumsFile, maxSumsBytes)
	if err != nil {
		return nil, err
	}
	sig, err := u.fetchAsset(ctx, r, release.SignatureFile, maxSigBytes)
	if err != nil {
		return nil, err
	}
	if err := u.verify(sums, sig); err != nil {
		return nil, err
	}
	return ParseSums(sums)
}

func (u *Updater) fetchAsset(ctx context.Context, r Release, name string, limit int64) ([]byte, error) {
	a, ok := r.assets[name]
	if !ok {
		return nil, fmt.Errorf("update: release %s has no %s", r.Tag, name)
	}
	body, err := u.get(ctx, a.url, "application/octet-stream", limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(body)
}

// Download saves r's asset name as a new file in dir and returns its
// path. The file is removed again unless its SHA-256 matches sums, so a
// path returned is a verified asset.
func (u *Updater) Download(ctx context.Context, r Release, sums Sums, name, dir string) (path string, err error) {
	want, ok := sums[name]
	if !ok {
		return "", fmt.Errorf("update: %s isn't listed in %s", name, release.ChecksumsFile)
	}
	a, ok := r.assets[name]
	if !ok {
		return "", fmt.Errorf("update: release %s has no %s", r.Tag, name)
	}
	if a.size > MaxAssetBytes {
		return "", errTooLarge
	}
	body, err := u.get(ctx, a.url, "application/octet-stream", MaxAssetBytes)
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()
	f, err := os.CreateTemp(dir, ".burnout-download-*")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), body); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", fmt.Errorf("%w: %s", ErrChecksum, name)
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// ErrChecksum means a download doesn't match its signed checksum.
var ErrChecksum = errors.New("update: the download doesn't match its signed checksum")

// errTooLarge means a response is over its limit.
var errTooLarge = errors.New("update: GitHub sent more data than expected")

func (u *Updater) get(ctx context.Context, rawURL, accept string, limit int64) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errors.New("update: bad release URL")
	}
	host := req.URL.Hostname()
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", accept)
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: connecting to %s failed: %w", host, withoutURL(err))
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		_ = resp.Body.Close()
		return nil, ErrNoRelease
	case resp.StatusCode != http.StatusOK:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("update: %s answered %s", host, resp.Status)
	case resp.ContentLength > limit:
		_ = resp.Body.Close()
		return nil, errTooLarge
	}
	return limitedBody{r: &io.LimitedReader{R: resp.Body, N: limit + 1}, body: resp.Body}, nil
}

// limitedBody reads at most limit bytes and fails on one more.
type limitedBody struct {
	r    *io.LimitedReader // limit+1 bytes
	body io.Closer
}

func (l limitedBody) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	if l.r.N == 0 && n > 0 {
		// The extra byte arrived: the response is over the limit.
		return 0, errTooLarge
	}
	return n, err
}

func (l limitedBody) Close() error { return l.body.Close() }

// withoutURL strips the request URL from an HTTP error, so errors read
// the same as the ICS client's (only the host is named).
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

// Sums maps a release file name to its lowercase hex SHA-256.
type Sums map[string]string

// ParseSums reads checksums.txt: "<sha256 hex>  <name>" per line, as
// sha256sum writes it. Names compare exactly.
func ParseSums(b []byte) (Sums, error) {
	sums := Sums{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		// "<sum>  <name>", or "<sum> *<name>" in sha256sum's binary mode.
		sum, rest, ok := strings.Cut(line, " ")
		ok = ok && (strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "*"))
		name := ""
		if ok {
			name = rest[1:]
		}
		if !ok || len(sum) != sha256.Size*2 || name == "" || filepath.Base(name) != name {
			return nil, fmt.Errorf("update: %s has a malformed line", release.ChecksumsFile)
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return nil, fmt.Errorf("update: %s has a malformed line", release.ChecksumsFile)
		}
		sums[name] = strings.ToLower(sum)
	}
	return sums, sc.Err()
}
