package release

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const packaging = "../../packaging"

func TestPinnedKeysParse(t *testing.T) {
	keys, err := PublicKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("got %d keys, want current and next", len(keys))
	}
	if keys[0].Equal(keys[1]) {
		t.Error("current and next are the same key, so rotation would break installs")
	}
}

var pemBlock = regexp.MustCompile(`-----BEGIN PUBLIC KEY-----[A-Za-z0-9+/=\s]+?-----END PUBLIC KEY-----`)

// The installers carry their own copies of the keys; they must match ours,
// in the same order.
func TestInstallersPinTheSameKeys(t *testing.T) {
	want := []string{norm(CurrentPublicKey), norm(NextPublicKey)}
	for _, name := range []string{"install.sh", "install.ps1"} {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(packaging, name))
			if err != nil {
				t.Fatal(err)
			}
			got := pemBlock.FindAllString(string(b), -1)
			if len(got) != len(want) {
				t.Fatalf("found %d PEM keys, want %d", len(got), len(want))
			}
			for i := range want {
				if norm(got[i]) != want[i] {
					t.Errorf("key %d differs from internal/release/keys.go", i)
				}
			}
		})
	}
}

// norm drops line endings, so a CRLF checkout compares equal.
func norm(p string) string {
	return strings.Join(strings.Fields(p), "\n")
}

// The fixtures are signed by a throwaway key with openssl, the same way
// the release workflow signs checksums.txt.
func TestVerifyFixture(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(packaging, "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	sums, sig := read("checksums.txt"), read("checksums.txt.sig")
	fixture, err := parseKeys(string(read("fixture.pub.pem")))
	if err != nil {
		t.Fatal(err)
	}

	if err := verify(fixture, sums, sig); err != nil {
		t.Errorf("fixture signature: %v", err)
	}
	if err := verify(fixture, append(sums, '\n'), sig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tampered checksums: got %v, want ErrBadSignature", err)
	}
	if err := VerifyChecksums(sums, sig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("unpinned key: got %v, want ErrBadSignature", err)
	}
}

func TestParseKeyRejects(t *testing.T) {
	for name, p := range map[string]string{
		"empty":    "",
		"not pem":  "hello",
		"private":  "-----BEGIN EC PRIVATE KEY-----\nAAAA\n-----END EC PRIVATE KEY-----\n",
		"trailing": CurrentPublicKey + "x",
		"two keys": CurrentPublicKey + NextPublicKey,
	} {
		if _, err := parseKey(p); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}
