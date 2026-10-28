#!/bin/sh
# Tests install.sh's signature and checksum checks against fixtures signed
# with a throwaway key (testdata/). Run from anywhere; CI runs it on Linux
# and macOS, where it also runs against the system LibreSSL.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
td=$here/testdata
fails=0

ok() { printf 'ok   %s\n' "$1"; }
bad() {
	printf 'FAIL %s\n' "$1"
	fails=$((fails + 1))
}
# expect_pass/expect_fail LABEL CMD...
expect_pass() {
	l=$1
	shift
	if "$@" 2>/dev/null; then ok "$l"; else bad "$l"; fi
}
expect_fail() {
	l=$1
	shift
	if "$@" 2>/dev/null; then bad "$l (accepted)"; else ok "$l (rejected)"; fi
}

run() {
	export BURNTOUT_INSTALL_LIB=1
	# shellcheck source-path=SCRIPTDIR source=install.sh
	. "$here/install.sh"
	printf '# %s\n' "$(openssl version)"

	tmp=$(mktemp -d)
	fixture=$(cat "$td/fixture.pub.pem")

	expect_pass "fixture key, fixture signature" verify_with_key "$fixture" "$td/checksums.txt" "$td/checksums.txt.sig"
	{ cat "$td/checksums.txt"; echo; } >"$tmp/tampered.txt"
	expect_fail "tampered checksums.txt" verify_with_key "$fixture" "$tmp/tampered.txt" "$td/checksums.txt.sig"
	expect_fail "wrong key" verify_with_key "$BURNTOUT_KEY_CURRENT" "$td/checksums.txt" "$td/checksums.txt.sig"
	# The fixture key is not pinned, so the real check must refuse it.
	expect_fail "unpinned key" verify_signature "$td/checksums.txt" "$td/checksums.txt.sig"

	expect_pass "matching hash" verify_checksum "$td/checksums.txt" "$td/asset.bin" asset.bin
	expect_fail "different hash" verify_checksum "$td/checksums.txt" "$td/asset.bin" other.bin
	expect_fail "unlisted asset" verify_checksum "$td/checksums.txt" "$td/asset.bin" missing.bin
	expect_fail "name prefix only" verify_checksum "$td/checksums.txt" "$td/asset.bin" asset

	# End to end: main against testdata/release, a fixture release signed
	# by the fixture key, with downloads served from disk.
	fetch() { cp "$td/release/${1##*/}" "$2"; }
	if (main --version v0.0.0 --dir "$tmp/pinned") >/dev/null 2>&1 || [ -e "$tmp/pinned/burntout" ]; then
		bad "install with the pinned keys (accepted)"
	else
		ok "install with the pinned keys (rejected, nothing installed)"
	fi
	BURNTOUT_KEY_CURRENT=$fixture
	if out=$(main --version v0.0.0 --dir "$tmp/bin" 2>&1) &&
		[ "$("$tmp/bin/burntout")" = "burntout fixture" ] &&
		case $out in *"burntout setup"*"burntout service install"*) true ;; *) false ;; esac; then
		ok "install with the fixture key"
	else
		bad "install with the fixture key: $out"
	fi
	rm -rf "$tmp"
}

(run) || fails=1
# macOS: also the LibreSSL in /usr/bin, if the default openssl is another.
if [ "$(uname -s)" = Darwin ] && [ "$(command -v openssl)" != /usr/bin/openssl ]; then
	(PATH=/usr/bin:/bin:/usr/sbin:/sbin run) || fails=1
fi
[ "$fails" -eq 0 ] || exit 1
