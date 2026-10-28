#!/bin/sh
# Installs BurntOut for the current user on macOS or Linux (ADR-0005).
#
# Please read this script before you run it. It:
#   1. picks the release asset for this OS and CPU;
#   2. downloads checksums.txt and checksums.txt.sig from the GitHub release;
#   3. checks the signature against the release keys pinned below, using
#      openssl (the LibreSSL that ships with macOS works too), then the
#      asset's SHA-256 against the signed checksums.txt;
#   4. installs the binary to ~/.local/bin (or --dir).
#
# It records nothing and starts nothing. Collection starts only once you
# have run `burntout setup` (consent) and `burntout service install`
# yourself; this script just prints those steps. There is no option to
# skip either (privacy & security §5.2).
#
# usage: install.sh [--version vX.Y.Z] [--dir DIR]
#   BURNTOUT_VERSION and BURNTOUT_INSTALL_DIR set the same things.
#
# To verify a download by hand instead, see packaging/README.md.
set -eu

REPO=kshitijdhara/burntout

# Pinned release keys. Keep in step with install.ps1 and
# internal/release/keys.go (a test checks).
BURNTOUT_KEY_CURRENT='-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEtWf8TGinleBqdY/75gHXSfsVXUuK
PuYDXdvc3bdwWiTeyPyeL2NDltsJmDXSb1gMESB8mpfKXWEn/dJEyn1LKQ==
-----END PUBLIC KEY-----'
BURNTOUT_KEY_NEXT='-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEWo5NAi/5+LFyDfLNDJgXviM2feAz
ovmH7aazyDKIhfdZqXAWi1tg88tGXgFk7tbfwAwZ/PKUUYi4RPP8Pfrgow==
-----END PUBLIC KEY-----'

say() { printf '%s\n' "$*"; }
die() {
	printf 'install.sh: %s\n' "$*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || die "needs '$1', which isn't installed"
}

# target prints the os_arch part of the asset name.
target() {
	case $(uname -s) in
	Darwin) echo darwin_all ;; # one universal binary
	Linux)
		case $(uname -m) in
		x86_64 | amd64) echo linux_amd64 ;;
		aarch64 | arm64) echo linux_arm64 ;;
		*) die "no release for Linux on $(uname -m); build from source with 'make build'" ;;
		esac
		;;
	*) die "no release for $(uname -s); on Windows use install.ps1" ;;
	esac
}

fetch() { # url out
	curl --proto '=https' --tlsv1.2 -fsSL -o "$2" "$1" || die "download failed: $1"
}

# latest_version prints the newest release's tag: the one GitHub's latest
# release redirects to or, while there are only pre-releases (an alpha),
# which "latest" never names, the newest release of any kind.
latest_version() {
	if url=$(curl --proto '=https' --tlsv1.2 -fsSLI -o /dev/null -w '%{url_effective}' \
		"https://github.com/$REPO/releases/latest" 2>/dev/null); then
		tag=${url##*/}
		case $tag in v[0-9]*)
			echo "$tag"
			return
			;;
		esac
	fi
	list=$(curl --proto '=https' --tlsv1.2 -fsSL \
		"https://api.github.com/repos/$REPO/releases?per_page=1") || die "couldn't list the releases"
	tag=$(printf '%s\n' "$list" | tr ',' '\n' |
		sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\(v[0-9][^"]*\)".*/\1/p' | head -n 1)
	[ -n "$tag" ] || die "couldn't find a release"
	echo "$tag"
}

# verify_with_key PEM DATA SIG succeeds if SIG is PEM's key's signature of
# DATA.
verify_with_key() {
	keyfile=$(mktemp)
	printf '%s\n' "$1" >"$keyfile"
	if openssl dgst -sha256 -verify "$keyfile" -signature "$3" "$2" >/dev/null 2>&1; then
		rm -f "$keyfile"
		return 0
	fi
	rm -f "$keyfile"
	return 1
}

# verify_signature DATA SIG succeeds if a pinned key signed DATA.
verify_signature() {
	need openssl
	for key in "$BURNTOUT_KEY_CURRENT" "$BURNTOUT_KEY_NEXT"; do
		if verify_with_key "$key" "$1" "$2"; then
			return 0
		fi
	done
	printf 'install.sh: %s\n' "$1 is not signed by a pinned release key; not installing" >&2
	return 1
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		openssl dgst -sha256 -r "$1" | awk '{print $1}'
	fi
}

# expected_sha256 CHECKSUMS NAME prints NAME's hash from CHECKSUMS, and
# fails unless it is listed exactly once.
expected_sha256() {
	awk -v n="$2" '$2 == n { print $1; c++ } END { exit c == 1 ? 0 : 1 }' "$1"
}

# verify_checksum CHECKSUMS FILE NAME succeeds if FILE's hash is NAME's
# line in CHECKSUMS. Only trust CHECKSUMS once verify_signature passed.
verify_checksum() {
	want=$(expected_sha256 "$1" "$3") || {
		printf 'install.sh: %s\n' "$3 is not listed once in $1" >&2
		return 1
	}
	got=$(sha256_of "$2")
	if [ "$got" != "$want" ]; then
		printf 'install.sh: %s\n' "$3: SHA-256 is $got, checksums.txt says $want; not installing" >&2
		return 1
	fi
}

main() {
	version=${BURNTOUT_VERSION:-}
	dir=${BURNTOUT_INSTALL_DIR:-$HOME/.local/bin}
	while [ $# -gt 0 ]; do
		case $1 in
		--version)
			[ $# -ge 2 ] || die "--version needs a value"
			version=$2
			shift 2
			;;
		--dir)
			[ $# -ge 2 ] || die "--dir needs a value"
			dir=$2
			shift 2
			;;
		-h | --help)
			say "usage: install.sh [--version vX.Y.Z] [--dir DIR]"
			return 0
			;;
		*) die "unknown option: $1" ;;
		esac
	done

	need curl
	need tar
	need openssl
	need awk

	[ -n "$version" ] || version=$(latest_version)
	case $version in v*) ;; *) version=v$version ;; esac
	asset="burntout_${version#v}_$(target).tar.gz"
	base="https://github.com/$REPO/releases/download/$version"

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	say "Downloading BurntOut $version"
	fetch "$base/checksums.txt" "$tmp/checksums.txt"
	fetch "$base/checksums.txt.sig" "$tmp/checksums.txt.sig"
	verify_signature "$tmp/checksums.txt" "$tmp/checksums.txt.sig" || exit 1
	say "Signature OK (pinned release key)"

	expected_sha256 "$tmp/checksums.txt" "$asset" >/dev/null ||
		die "$version has no $asset"
	fetch "$base/$asset" "$tmp/$asset"
	verify_checksum "$tmp/checksums.txt" "$tmp/$asset" "$asset" || exit 1
	say "Checksum OK ($asset)"

	mkdir -p "$tmp/x"
	tar -xzf "$tmp/$asset" -C "$tmp/x" burntout
	mkdir -p "$dir"
	# Copy next to the target, then rename: a running BurntOut keeps its
	# old file, and a half-copied binary is never on the PATH.
	cp "$tmp/x/burntout" "$dir/.burntout.new"
	chmod 0755 "$dir/.burntout.new"
	mv -f "$dir/.burntout.new" "$dir/burntout"
	say "Installed $dir/burntout"

	case ":$PATH:" in
	*":$dir:"*) cmd=burntout ;;
	*)
		cmd="$dir/burntout"
		say ""
		say "$dir is not on your PATH. Add it to your shell's startup file, for example:"
		say "  export PATH=\"$dir:\$PATH\""
		;;
	esac

	say ""
	say "Nothing is recorded yet. When you're ready:"
	say "  $cmd setup             review what is collected and consent"
	say "  $cmd service install   run in the background from login"
}

# CI sources this file with BURNTOUT_INSTALL_LIB=1 to test the verifiers
# (packaging/test-install.sh, the release workflow).
if [ "${BURNTOUT_INSTALL_LIB:-}" != 1 ]; then
	main "$@"
fi
