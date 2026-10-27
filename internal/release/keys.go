// Package release holds the pinned release-signing keys and checks a
// release's checksums.txt against them (ADR-0005, "Release signing").
//
// The release workflow signs checksums.txt with an ECDSA P-256 key:
// checksums.txt.sig is the output of `openssl dgst -sha256 -sign`, an
// ASN.1 DER signature over the SHA-256 of the file. install.sh, install.ps1
// and `burnout update` each embed the same two public keys, current and
// next, so the key can be rotated without breaking existing installs. A
// test keeps the copies in step.
//
// Key rotation and the key ceremony are described in packaging/README.md.
package release

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// CurrentPublicKey signs releases today.
//
// PLACEHOLDER: a throwaway key whose private half was discarded. It is
// replaced at the key ceremony (packaging/README.md); until then nothing
// verifies, so the release workflow refuses to publish.
const CurrentPublicKey = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE/8SWkP4fESmdwPU6RS6MaGrtL7OP
NsOyRNsSrsKFL05ePQxaew10J1Z+OW/Iza6IjmogozwXeH314JGJjpkEEQ==
-----END PUBLIC KEY-----
`

// NextPublicKey takes over when the current key is retired.
//
// PLACEHOLDER, like CurrentPublicKey.
const NextPublicKey = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEinX0Slhy6+TMIgTbJLbzeaxhXtYn
tPFyZNyYDQHrGNPPc2/fsyjxaEr+DEasJXoVs4l1S+aiIF0Z29NV7HrOxQ==
-----END PUBLIC KEY-----
`

// ChecksumsFile and SignatureFile are the release assets the signature
// covers and lives in.
const (
	ChecksumsFile = "checksums.txt"
	SignatureFile = "checksums.txt.sig"
)

// ErrBadSignature means no pinned key verifies the signature.
var ErrBadSignature = errors.New("release: checksums.txt signature does not match a pinned release key")

// PublicKeys returns the pinned keys, current first.
func PublicKeys() ([]*ecdsa.PublicKey, error) {
	return parseKeys(CurrentPublicKey, NextPublicKey)
}

// VerifyChecksums checks that sig is a pinned key's signature of
// checksums. Only after it passes may the hashes in checksums be trusted.
func VerifyChecksums(checksums, sig []byte) error {
	keys, err := PublicKeys()
	if err != nil {
		return err
	}
	return verify(keys, checksums, sig)
}

func verify(keys []*ecdsa.PublicKey, data, sig []byte) error {
	digest := sha256.Sum256(data)
	for _, k := range keys {
		if ecdsa.VerifyASN1(k, digest[:], sig) {
			return nil
		}
	}
	return ErrBadSignature
}

func parseKeys(pems ...string) ([]*ecdsa.PublicKey, error) {
	keys := make([]*ecdsa.PublicKey, 0, len(pems))
	for i, p := range pems {
		k, err := parseKey(p)
		if err != nil {
			return nil, fmt.Errorf("release: key %d: %w", i, err)
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// parseKey accepts exactly one PEM "PUBLIC KEY" block holding a P-256 key.
func parseKey(p string) (*ecdsa.PublicKey, error) {
	block, rest := pem.Decode([]byte(p))
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, errors.New("not a PEM public key")
	}
	if len(rest) != 0 {
		return nil, errors.New("trailing data after the PEM block")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	k, ok := pub.(*ecdsa.PublicKey)
	if !ok || k.Curve != elliptic.P256() {
		return nil, errors.New("not an ECDSA P-256 key")
	}
	return k, nil
}
