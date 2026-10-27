#!/bin/sh
# Installs burnout for the current user on macOS or Linux (ADR-0005).
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
# have run `burnout setup` (consent) and `burnout service install`
# yourself; this script just prints those steps. There is no option to
# skip either (privacy & security §5.2).
#
# usage: install.sh [--version vX.Y.Z] [--dir DIR]
#   BURNOUT_VERSION and BURNOUT_INSTALL_DIR set the same things.
#
# To verify a download by hand instead, see packaging/README.md.
set -eu

REPO=kshitijdhara/burnout-tracker

# Pinned release keys. Keep in step with install.ps1 and
# internal/release/keys.go (a test checks).
# PLACEHOLDER keys until the key ceremony in packaging/README.md.
BURNOUT_KEY_CURRENT='-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE/8SWkP4fESmdwPU6RS6MaGrtL7OP
NsOyRNsSrsKFL05ePQxaew10J1Z+OW/Iza6IjmogozwXeH314JGJjpkEEQ==
-----END PUBLIC KEY-----'
BURNOUT_KEY_NEXT='-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEinX0Slhy6+TMIgTbJLbzeaxhXtYn
tPFyZNyYDQHrGNPPc2/fsyjxaEr+DEasJXoVs4l1S+aiIF0Z29NV7HrOxQ==
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

# latest_version prints the tag the latest release redirects to.
latest_version() {
	url=$(curl --proto '=https' --tlsv1.2 -fsSLI -o /dev/null -w '%{url_effective}' \
		"https://github.com/$REPO/releases/latest") || die "couldn't find the latest release"
	tag=${url##*/}
	case $tag in
	v[0-9]*) echo "$tag" ;;
	*) die "couldn't find the latest release (got '$url')" ;;
	esac
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
	for key in "$BURNOUT_KEY_CURRENT" "$BURNOUT_KEY_NEXT"; do
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
	version=${BURNOUT_VERSION:-}
	dir=${BURNOUT_INSTALL_DIR:-$HOME/.local/bin}
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
	asset="burnout_${version#v}_$(target).tar.gz"
	base="https://github.com/$REPO/releases/download/$version"

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	say "Downloading burnout $version"
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
	tar -xzf "$tmp/$asset" -C "$tmp/x" burnout
	mkdir -p "$dir"
	# Copy next to the target, then rename: a running burnout keeps its
	# old file, and a half-copied binary is never on the PATH.
	cp "$tmp/x/burnout" "$dir/.burnout.new"
	chmod 0755 "$dir/.burnout.new"
	mv -f "$dir/.burnout.new" "$dir/burnout"
	say "Installed $dir/burnout"

	case ":$PATH:" in
	*":$dir:"*) cmd=burnout ;;
	*)
		cmd="$dir/burnout"
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

# CI sources this file with BURNOUT_INSTALL_LIB=1 to test the verifiers
# (packaging/test-install.sh, the release workflow).
if [ "${BURNOUT_INSTALL_LIB:-}" != 1 ]; then
	main "$@"
fi
