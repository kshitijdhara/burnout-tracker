package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kshitijdhara/burnout-tracker/internal/release"
)

const testArch = "amd64"

// fakeGitHub serves releases the way the Releases API and asset downloads
// do, over TLS on 127.0.0.1.
type fakeGitHub struct {
	srv      *httptest.Server
	releases []ghRelease
	files    map[string][]byte // tag/name → content
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{files: map[string][]byte{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	const prefix = "/repos/owner/repo/releases"
	switch p := r.URL.Path; {
	case p == prefix:
		_ = json.NewEncoder(w).Encode(f.releases)
	case p == prefix+"/latest":
		for _, g := range f.releases {
			if !g.Draft && !g.Prerelease {
				_ = json.NewEncoder(w).Encode(g)
				return
			}
		}
		http.NotFound(w, r)
	case strings.HasPrefix(p, prefix+"/tags/"):
		for _, g := range f.releases {
			if g.TagName == strings.TrimPrefix(p, prefix+"/tags/") {
				_ = json.NewEncoder(w).Encode(g)
				return
			}
		}
		http.NotFound(w, r)
	case strings.HasPrefix(p, "/redirect/"):
		// Like github.com handing a download to its CDN.
		http.Redirect(w, r, "/download/"+strings.TrimPrefix(p, "/redirect/"), http.StatusFound)
	case strings.HasPrefix(p, "/download/"):
		b, ok := f.files[strings.TrimPrefix(p, "/download/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

// add publishes a release with these files.
func (f *fakeGitHub) add(tag string, prerelease bool, files map[string][]byte) {
	g := ghRelease{TagName: tag, Prerelease: prerelease}
	for name, b := range files {
		f.files[tag+"/"+name] = b
		g.Assets = append(g.Assets, struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		}{name, f.srv.URL + "/redirect/" + tag + "/" + name, int64(len(b))})
	}
	f.releases = append([]ghRelease{g}, f.releases...) // newest first, like the API
}

// updater talks to the fake, trusting key.
func (f *fakeGitHub) updater(key *ecdsa.PublicKey) *Updater {
	return &Updater{
		client: newClient(f.srv.Client().Transport, []string{"127.0.0.1"}),
		api:    f.srv.URL,
		repo:   "owner/repo",
		verify: func(sums, sig []byte) error {
			d := sha256.Sum256(sums)
			if ecdsa.VerifyASN1(key, d[:], sig) {
				return nil
			}
			return release.ErrBadSignature
		},
	}
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// archive packs binary as this platform's release archive.
func archive(t *testing.T, binary []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		for _, name := range []string{"README.md", "burnout.exe"} {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(binary)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"LICENSE", "burnout"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(binary)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// signedFiles is a release's files: the archive for this platform, and
// checksums.txt signed by key.
func signedFiles(t *testing.T, key *ecdsa.PrivateKey, ver string, binary []byte) map[string][]byte {
	t.Helper()
	in := Install{Kind: UserPrefix, GOOS: runtime.GOOS, Arch: testArch}
	v, _ := ParseVersion(ver)
	name, _ := in.Asset(v)
	files := map[string][]byte{name: archive(t, binary)}
	pkg := strings.TrimSuffix(strings.TrimSuffix(name, ".tar.gz"), ".zip") + ".deb"
	files[pkg] = []byte("a package")
	var sums strings.Builder
	for _, n := range slices.Sorted(maps.Keys(files)) {
		h := sha256.Sum256(files[n])
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), n)
	}
	files[release.ChecksumsFile] = []byte(sums.String())
	files[release.SignatureFile] = sign(t, key, files[release.ChecksumsFile])
	return files
}

func sign(t *testing.T, key *ecdsa.PrivateKey, b []byte) []byte {
	t.Helper()
	d := sha256.Sum256(b)
	sig, err := ecdsa.SignASN1(rand.Reader, key, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

// installed is a user-prefix install in a temp dir holding "old binary".
func installed(t *testing.T) Install {
	t.Helper()
	dir := t.TempDir()
	name := "burnout"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	in, err := Detect(path, runtime.GOOS, testArch)
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != UserPrefix {
		t.Fatalf("a temp dir install is a %s", in.Kind)
	}
	return in
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// dirHas fails unless dir holds exactly these names: no leftovers.
func dirHas(t *testing.T, dir string, want ...string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s holds %v, want %v", dir, got, want)
	}
}

func TestUpdateSwapsAtomically(t *testing.T) {
	key := newKey(t)
	gh := newFakeGitHub(t)
	gh.add("v1.2.0", false, signedFiles(t, key, "1.2.0", []byte("new binary")))
	in := installed(t)

	res, err := gh.updater(&key.PublicKey).Run(context.Background(), Options{Current: "1.1.0", Install: in})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Swapped || res.Version.String() != "1.2.0" || res.Previous != in.Previous() {
		t.Errorf("result: %+v", res)
	}
	if got := read(t, in.Path); got != "new binary" {
		t.Errorf("binary: %q", got)
	}
	if got := read(t, in.Previous()); got != "old binary" {
		t.Errorf("previous binary: %q", got)
	}
	if fi, err := os.Stat(in.Path); err == nil && runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("new binary isn't executable: %v", fi.Mode())
	}
	dirHas(t, filepath.Dir(in.Path), filepath.Base(in.Path), filepath.Base(in.Previous()))

	// A second update replaces the kept binary with the one it replaces.
	gh.add("v1.3.0", false, signedFiles(t, key, "1.3.0", []byte("newer binary")))
	if _, err := gh.updater(&key.PublicKey).Run(context.Background(), Options{Current: "1.2.0", Install: in}); err != nil {
		t.Fatal(err)
	}
	if got, prev := read(t, in.Path), read(t, in.Previous()); got != "newer binary" || prev != "new binary" {
		t.Errorf("after a second update: binary %q, previous %q", got, prev)
	}
}

func TestUpdateRefusesBadSignature(t *testing.T) {
	key, other := newKey(t), newKey(t)
	gh := newFakeGitHub(t)
	gh.add("v1.2.0", false, signedFiles(t, other, "1.2.0", []byte("new binary")))
	in := installed(t)

	_, err := gh.updater(&key.PublicKey).Run(context.Background(), Options{Current: "1.1.0", Install: in})
	if !errors.Is(err, release.ErrBadSignature) {
		t.Fatalf("got %v, want ErrBadSignature", err)
	}
	if read(t, in.Path) != "old binary" {
		t.Error("the binary changed")
	}
	dirHas(t, filepath.Dir(in.Path), filepath.Base(in.Path))
}

func TestUpdateRefusesBadChecksum(t *testing.T) {
	key := newKey(t)
	gh := newFakeGitHub(t)
	files := signedFiles(t, key, "1.2.0", []byte("new binary"))
	in := installed(t)
	name, _ := in.Asset(Version{Major: 1, Minor: 2})
	files[name] = archive(t, []byte("evil binary")) // after signing
	gh.add("v1.2.0", false, files)

	_, err := gh.updater(&key.PublicKey).Run(context.Background(), Options{Current: "1.1.0", Install: in})
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("got %v, want ErrChecksum", err)
	}
	if read(t, in.Path) != "old binary" {
		t.Error("the binary changed")
	}
	dirHas(t, filepath.Dir(in.Path), filepath.Base(in.Path))
}

func TestUpdateVersionRules(t *testing.T) {
	key := newKey(t)
	gh := newFakeGitHub(t)
	gh.add("v1.1.0", false, signedFiles(t, key, "1.1.0", []byte("old release")))
	gh.add("v1.2.0", false, signedFiles(t, key, "1.2.0", []byte("new binary")))
	u := gh.updater(&key.PublicKey)
	ctx := context.Background()

	in := installed(t)
	if _, err := u.Run(ctx, Options{Current: "1.3.0", Install: in}); !errors.Is(err, ErrDowngrade) {
		t.Errorf("older latest: got %v, want ErrDowngrade", err)
	}
	if _, err := u.Run(ctx, Options{Current: "1.2.0", To: "v1.1.0", Install: in}); !errors.Is(err, ErrDowngrade) {
		t.Errorf("--to an older version: got %v, want ErrDowngrade", err)
	}
	if _, err := u.Run(ctx, Options{Current: "dev", Install: in}); !errors.Is(err, ErrDevBuild) {
		t.Errorf("dev build: got %v, want ErrDevBuild", err)
	}
	if res, err := u.Run(ctx, Options{Current: "1.2.0", Install: in}); err != nil || !res.UpToDate {
		t.Errorf("same version: %+v, %v", res, err)
	}
	if res, err := u.Run(ctx, Options{Current: "1.1.0", CheckOnly: true, Install: in}); err != nil || res.Swapped || res.Version.String() != "1.2.0" {
		t.Errorf("check only: %+v, %v", res, err)
	}
	dirHas(t, filepath.Dir(in.Path), filepath.Base(in.Path))
	if _, err := u.Run(ctx, Options{Current: "1.0.0", To: "v9.9.9", Install: in}); !errors.Is(err, ErrNoRelease) {
		t.Errorf("no such tag: got %v, want ErrNoRelease", err)
	}

	// Rollback: --to an older version with --allow-downgrade.
	res, err := u.Run(ctx, Options{Current: "1.2.0", To: "v1.1.0", AllowDowngrade: true, Install: in})
	if err != nil || !res.Swapped {
		t.Fatalf("rollback: %+v, %v", res, err)
	}
	if read(t, in.Path) != "old release" {
		t.Error("rollback didn't install 1.1.0")
	}
}

// A validly signed older release relabelled as a newer one must not
// install: asset names carry the version, so its checksums don't list
// the asset the newer version needs.
func TestUpdateRefusesRelabelledRelease(t *testing.T) {
	key := newKey(t)
	gh := newFakeGitHub(t)
	old := signedFiles(t, key, "1.0.0", []byte("old vulnerable binary"))
	in := installed(t)
	oldName, _ := in.Asset(Version{Major: 1})
	newName, _ := in.Asset(Version{Major: 9, Minor: 9, Patch: 9})
	old[newName] = old[oldName]
	gh.add("v9.9.9", false, old)

	_, err := gh.updater(&key.PublicKey).Run(context.Background(), Options{Current: "1.1.0", Install: in})
	if err == nil || !strings.Contains(err.Error(), "isn't listed") {
		t.Fatalf("got %v, want a refusal", err)
	}
	if read(t, in.Path) != "old binary" {
		t.Error("the binary changed")
	}
	dirHas(t, filepath.Dir(in.Path), filepath.Base(in.Path))
}

// A missing asset is a download failure, not "no such release".
func TestMissingAssetIsNotNoRelease(t *testing.T) {
	key := newKey(t)
	gh := newFakeGitHub(t)
	gh.add("v1.2.0", false, signedFiles(t, key, "1.2.0", []byte("new binary")))
	for k := range gh.files {
		if strings.HasSuffix(k, ".sig") {
			delete(gh.files, k)
		}
	}
	_, err := gh.updater(&key.PublicKey).Run(context.Background(), Options{Current: "1.1.0", Install: installed(t)})
	if err == nil || errors.Is(err, ErrNoRelease) || !strings.Contains(err.Error(), "404") {
		t.Errorf("got %v", err)
	}
}

func TestBetaChannel(t *testing.T) {
	key := newKey(t)
	gh := newFakeGitHub(t)
	gh.add("v1.2.0", false, nil)
	gh.add("v1.3.0-beta.2", true, nil)
	gh.add("v1.3.0-beta.10", true, nil)
	gh.releases = append(gh.releases, ghRelease{TagName: "v9.0.0", Draft: true})
	u := gh.updater(&key.PublicKey)
	if r, err := u.Latest(context.Background(), Stable); err != nil || r.Tag != "v1.2.0" {
		t.Errorf("stable: %v, %v", r.Tag, err)
	}
	if r, err := u.Latest(context.Background(), Beta); err != nil || r.Tag != "v1.3.0-beta.10" {
		t.Errorf("beta: %v, %v", r.Tag, err)
	}
}

func TestPackageInstallOnlyDownloads(t *testing.T) {
	key := newKey(t)
	gh := newFakeGitHub(t)
	gh.add("v1.2.0", false, signedFiles(t, key, "1.2.0", []byte("new binary")))
	in := installed(t)
	in.Kind = Package
	in.GOOS = "linux" // a .deb
	downloads := filepath.Join(t.TempDir(), "updates")

	res, err := gh.updater(&key.PublicKey).Run(context.Background(), Options{Current: "1.1.0", Install: in, DownloadDir: downloads})
	if runtime.GOOS != "linux" {
		// The fake only has a .deb under this platform's name; skip the rest.
		if err == nil || !strings.Contains(err.Error(), "has no") {
			t.Fatalf("got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if res.Swapped || filepath.Dir(res.Download) != downloads || read(t, res.Download) != "a package" {
		t.Errorf("result: %+v", res)
	}
	if read(t, in.Path) != "old binary" {
		t.Error("a package install was changed in place")
	}
}

// The release fixture is signed by openssl, the way the release workflow
// signs: the updater must accept the same format install.sh verifies.
func TestOpenSSLSignedFixture(t *testing.T) {
	const dir = "../../../../packaging/testdata"
	b, err := os.ReadFile(filepath.Join(dir, "fixture.pub.pem"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	ents, err := os.ReadDir(filepath.Join(dir, "release"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if files[e.Name()], err = os.ReadFile(filepath.Join(dir, "release", e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	gh := newFakeGitHub(t)
	gh.add("v0.0.0", false, files)
	in := installed(t)

	res, err := gh.updater(pub.(*ecdsa.PublicKey)).Run(context.Background(), Options{Current: "0.0.1", AllowDowngrade: true, Install: in})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Swapped || read(t, in.Path) == "old binary" {
		t.Errorf("fixture not installed: %+v", res)
	}
}

func TestGuardRefusesOtherHosts(t *testing.T) {
	key := newKey(t)
	gh := newFakeGitHub(t)
	u := gh.updater(&key.PublicKey)
	u.api = strings.Replace(gh.srv.URL, "127.0.0.1", "localhost", 1)
	if _, err := u.Latest(context.Background(), Stable); err == nil || !strings.Contains(err.Error(), "refusing to connect to localhost") {
		t.Errorf("other host: %v", err)
	}
	u.api = strings.Replace(gh.srv.URL, "https://", "http://", 1)
	if _, err := u.Latest(context.Background(), Stable); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("plain http: %v", err)
	}

	// A redirect away from the allowed hosts is refused too.
	away := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.invalid/x", http.StatusFound)
	}))
	defer away.Close()
	u = &Updater{client: newClient(away.Client().Transport, []string{"127.0.0.1"}), api: away.URL, repo: "owner/repo"}
	if _, err := u.Latest(context.Background(), Stable); err == nil || !strings.Contains(err.Error(), "example.invalid") {
		t.Errorf("redirect: %v", err)
	}
}

func TestSizeLimits(t *testing.T) {
	big := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tag_name":"v1.0.0","x":"`+strings.Repeat("a", maxAPIBytes)+`"}`)
	}))
	defer big.Close()
	u := &Updater{client: newClient(big.Client().Transport, []string{"127.0.0.1"}), api: big.URL, repo: "owner/repo"}
	if _, err := u.Latest(context.Background(), Stable); !errors.Is(err, errTooLarge) {
		t.Errorf("got %v, want errTooLarge", err)
	}
}

func TestParseSums(t *testing.T) {
	h := strings.Repeat("ab", 32)
	s, err := ParseSums([]byte(h + "  a.tar.gz\r\n" + strings.ToUpper(h) + " *b.zip\n\n"))
	if err != nil || s["a.tar.gz"] != h || s["b.zip"] != h {
		t.Errorf("%v, %v", s, err)
	}
	for _, bad := range []string{h + " a", "xyz  a", h + "  ../a", strings.Repeat("zz", 32) + "  a"} {
		if _, err := ParseSums([]byte(bad)); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestVersionCompare(t *testing.T) {
	order := []string{"0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "v1.0.1", "1.10.0"}
	for i := range order {
		for j := range order {
			a, err := ParseVersion(order[i])
			if err != nil {
				t.Fatal(order[i], err)
			}
			b, _ := ParseVersion(order[j])
			want := cmpInt(i, j)
			if got := a.Compare(b); got != want {
				t.Errorf("%s vs %s: %d, want %d", order[i], order[j], got, want)
			}
		}
	}
	for _, bad := range []string{"dev", "", "1.2", "1.2.3.4", "01.2.3", "1.2.3-", "1.2.3-a..b", "1.x.3"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if v, _ := ParseVersion("v1.2.3-rc.1+build"); v.String() != "1.2.3-rc.1" || !v.Prerelease() {
		t.Errorf("String: %s", v)
	}
}

func TestManagedPath(t *testing.T) {
	t.Setenv("ProgramFiles", `C:\Program Files`)
	for _, c := range []struct {
		path, goos string
		want       Kind
	}{
		{"/Applications/Burnout.app/Contents/MacOS/burnout", "darwin", App},
		{"/Users/me/.local/bin/burnout", "darwin", UserPrefix},
		{"/usr/bin/burnout", "linux", Package},
		{"/bin/burnout", "linux", Package},
		{"/usr/local/bin/burnout", "linux", UserPrefix},
		{"/home/me/.local/bin/burnout", "linux", UserPrefix},
		{`C:\Program Files\burnout\burnout.exe`, "windows", Package},
		{`C:\Users\me\AppData\Local\Programs\burnout\burnout.exe`, "windows", UserPrefix},
	} {
		if got := managedPath(c.path, c.goos); got != c.want {
			t.Errorf("%s on %s: %s, want %s", c.path, c.goos, got, c.want)
		}
	}
}

func TestDetectResolvesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	app := filepath.Join(dir, "Burnout.app", "Contents", "MacOS")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(app, "burnout")
	if err := os.WriteFile(bin, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "burnout")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	in, err := Detect(link, "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != App {
		t.Errorf("a link into the app is a %s", in.Kind)
	}
	if name, _ := in.Asset(Version{Major: 1}); name != "burnout_1.0.0_darwin_all.dmg" {
		t.Errorf("asset %s", name)
	}
}

func TestAssetNames(t *testing.T) {
	v := Version{Major: 1, Minor: 2, Patch: 3}
	for _, c := range []struct {
		in   Install
		want string
	}{
		{Install{Kind: UserPrefix, GOOS: "linux", Arch: "arm64"}, "burnout_1.2.3_linux_arm64.tar.gz"},
		{Install{Kind: UserPrefix, GOOS: "darwin", Arch: "arm64"}, "burnout_1.2.3_darwin_all.tar.gz"},
		{Install{Kind: UserPrefix, GOOS: "windows", Arch: "amd64"}, "burnout_1.2.3_windows_amd64.zip"},
		{Install{Kind: Package, GOOS: "windows", Arch: "amd64"}, "burnout_1.2.3_windows_amd64.zip"},
		{Install{Kind: ReadOnly, GOOS: "linux", Arch: "amd64"}, "burnout_1.2.3_linux_amd64.tar.gz"},
	} {
		if got, _ := c.in.Asset(v); got != c.want {
			t.Errorf("%+v: %s, want %s", c.in, got, c.want)
		}
	}
}

func TestState(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	s := LoadState(dir)
	if !s.Due(now) {
		t.Error("never checked isn't due")
	}
	s = State{Checked: now, Tried: now, Latest: "1.2.0"}
	if err := SaveState(dir, s); err != nil {
		t.Fatal(err)
	}
	s = LoadState(dir)
	if s.Due(now.Add(6*24*time.Hour)) || !s.Due(now.Add(CheckEvery)) {
		t.Error("due after a week")
	}
	if v, ok := s.Newer("1.1.0"); !ok || v.String() != "1.2.0" {
		t.Errorf("newer: %v %v", v, ok)
	}
	for _, cur := range []string{"1.2.0", "1.3.0", "dev"} {
		if _, ok := s.Newer(cur); ok {
			t.Errorf("%s told to update", cur)
		}
	}
	failed := State{Checked: now.Add(-30 * 24 * time.Hour), Tried: now}
	if failed.Due(now.Add(time.Hour)) || !failed.Due(now.Add(RetryAfter)) {
		t.Error("a failed check retries after a day")
	}
}
