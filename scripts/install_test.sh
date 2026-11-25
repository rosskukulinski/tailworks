#!/bin/sh
# install_test.sh: install.sh upgrade keeps .prev, --rollback swaps it back,
# a same-version repair keeps the older .prev, another version over a running
# tw is refused (install.sh and get.sh), --uninstall --purge removes it all;
# install.sh and get.sh refuse a release that isn't signed by a pinned key
# (issue #71). Hermetic (fake releases in a temp dir, signed by a throwaway
# key that TW_RELEASE_SIGNERS pins); run: sh scripts/install_test.sh
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
[ "$arch" = aarch64 ] && arch=arm64
[ "$arch" = x86_64 ] && arch=amd64
sha() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
command -v ssh-keygen >/dev/null || { echo "install_test: needs ssh-keygen (OpenSSH)" >&2; exit 1; }

# A release key the test pins, and one it doesn't.
ssh-keygen -q -t ed25519 -N '' -C test -f "$T/key"
ssh-keygen -q -t ed25519 -N '' -C other -f "$T/other"
printf 'tw-release namespaces="tw-release" %s\n' "$(cat "$T/key.pub")" >"$T/allowed_signers"
export TW_RELEASE_SIGNERS=$T/allowed_signers
sign() { # key file
	rm -f "$2.sig"
	ssh-keygen -q -Y sign -f "$1" -n tw-release "$2"
}

mkrel() { # version [dir]: a signed release directory (default $T/rel-<version>)
	d=${2:-$T/rel-$1}
	mkdir -p "$d/bin" "$d/share/tailworks"
	printf '#!/bin/sh\necho tw %s\n' "$1" >"$d/bin/tw"
	printf '#!/bin/sh\necho workerd\n' >"$d/bin/workerd"
	chmod 755 "$d/bin/"*
	printf '%s\n' "$1" >"$d/VERSION"
	printf '{"version":"%s","os":"%s","arch":"%s"}\n' "$1" "$os" "$arch" >"$d/share/tailworks/release.json"
	cp "$ROOT/scripts/install.sh" "$d/install.sh"
	(cd "$d" && find . -type f ! -name SHA256SUMS ! -name SHA256SUMS.sig | sed 's|^\./||' | while read -r f; do sha "$f"; done >SHA256SUMS)
	sign "$T/key" "$d/SHA256SUMS"
}
fail() { echo "FAIL: $*" >&2; exit 1; }
P=$T/prefix
export TW_HOME=$P

mkrel 0.1.0
mkrel 0.2.0
sh "$T/rel-0.1.0/install.sh" >/dev/null
[ "$(cat "$P/VERSION")" = 0.1.0 ] || fail "first install"
[ ! -e "$P/bin.prev" ] || fail "first install made a .prev"
[ -x "$P/install.sh" ] || fail "install.sh not copied to the prefix"
[ "$("$P/bin/tailworks")" = "tw 0.1.0" ] || fail "the tailworks → tw symlink"

sh "$T/rel-0.2.0/install.sh" >/dev/null
[ "$(cat "$P/VERSION")" = 0.2.0 ] || fail "upgrade"
[ "$(cat "$P/VERSION.prev")" = 0.1.0 ] || fail "VERSION.prev after upgrade"
[ "$("$P/bin.prev/tw")" = "tw 0.1.0" ] || fail "bin.prev is not 0.1.0"

sh "$T/rel-0.2.0/install.sh" >/dev/null # repair: keeps 0.1.0 as .prev
[ "$(cat "$P/VERSION.prev")" = 0.1.0 ] || fail "repair replaced .prev"

sh "$P/install.sh" --rollback >/dev/null
[ "$(cat "$P/VERSION")" = 0.1.0 ] || fail "rollback VERSION"
[ "$("$P/bin/tw")" = "tw 0.1.0" ] || fail "rollback bin"
[ "$(cat "$P/VERSION.prev")" = 0.2.0 ] || fail "rollback did not keep 0.2.0 as .prev"
sh "$P/install.sh" --rollback >/dev/null
[ "$("$P/bin/tw")" = "tw 0.2.0" ] || fail "second rollback did not return"

# Refuses while running (a live supervisor.pid), unless --force.
sleep 30 &
pid=$!
echo $pid >"$P/supervisor.pid"
if sh "$P/install.sh" --rollback >/dev/null 2>&1; then fail "rollback while running"; fi
sh "$P/install.sh" --rollback --force >/dev/null
# Another version over a running tw: refused, pointing at tw upgrade
# (issue #485); a same-version repair goes ahead; --force swaps anyway.
[ "$(cat "$P/VERSION")" = 0.1.0 ] || fail "expected 0.1.0 after the forced rollback"
if sh "$T/rel-0.2.0/install.sh" >"$T/out" 2>&1; then fail "installed 0.2.0 over a running 0.1.0"; fi
grep -q "tw 0.1.0 is running from $P" "$T/out" || fail "running refusal message: $(cat "$T/out")"
grep -q "tw upgrade --from $T/rel-0.2.0" "$T/out" || fail "the refusal doesn't name tw upgrade --from: $(cat "$T/out")"
[ "$(cat "$P/VERSION")" = 0.1.0 ] || fail "the refused install changed VERSION"
sh "$T/rel-0.1.0/install.sh" >"$T/out" 2>&1 || fail "a same-version repair while running: $(cat "$T/out")"
# get.sh checks before it downloads anything (no such mirror).
if TW_RELEASE_BASE="file://$T/no-such-mirror" sh "$ROOT/scripts/get.sh" v0.2.0 >"$T/out" 2>&1; then fail "get.sh over a running tw"; fi
grep -q "tw 0.1.0 is running from $P" "$T/out" || fail "get.sh running refusal: $(cat "$T/out")"
grep -q "upgrade 0.2.0" "$T/out" || fail "get.sh refusal doesn't name tw upgrade: $(cat "$T/out")"
sh "$T/rel-0.2.0/install.sh" --force >/dev/null || fail "--force over a running tw"
[ "$(cat "$P/VERSION")" = 0.2.0 ] || fail "--force did not install"
kill $pid 2>/dev/null || true
wait $pid 2>/dev/null || true
rm -f "$P/supervisor.pid"

# --uninstall keeps the state; --uninstall --purge removes the whole
# prefix, backups/ and link.json included (issue #487: tw uninstall's end
# state).
mkdir -p "$P/mgmt" "$P/backups/b1" "$P/cli"
echo '{}' >"$P/tw.json"
echo '{}' >"$P/link.json"
sh "$P/install.sh" --uninstall >/dev/null
[ ! -e "$P/bin" ] && [ ! -e "$P/share" ] || fail "uninstall left bin/share"
[ -e "$P/tw.json" ] && [ -d "$P/backups/b1" ] || fail "uninstall without --purge removed state"
sh "$T/rel-0.2.0/install.sh" --uninstall --purge >/dev/null
[ ! -e "$P" ] || fail "uninstall --purge left: $(ls -A "$P")"
if sh "$T/rel-0.1.0/install.sh" --rollback >/dev/null 2>&1; then fail "rollback with no .prev"; fi

# install.sh: signatures.
refused() { # what dir [args]: install.sh refuses, installs nothing
	what=$1 d=$2
	shift 2
	rm -rf "$P"
	if sh "$d/install.sh" "$@" >"$T/out" 2>&1; then fail "install.sh installed $what"; fi
	[ ! -e "$P/bin" ] || fail "install.sh $what left $P/bin"
}
mkrel 0.3.0 "$T/bad-sig"
sign "$T/other" "$T/bad-sig/SHA256SUMS"
refused "a release signed by an unpinned key" "$T/bad-sig"
grep -q 'not a valid signature' "$T/out" || fail "bad signature message: $(cat "$T/out")"
mkrel 0.3.0 "$T/garbled-sig"
echo garbage >"$T/garbled-sig/SHA256SUMS.sig"
refused "a garbled signature" "$T/garbled-sig"
mkrel 0.3.0 "$T/resummed"
printf '#!/bin/sh\necho evil\n' >"$T/resummed/bin/tw"
(cd "$T/resummed" && find . -type f ! -name SHA256SUMS ! -name SHA256SUMS.sig | sed 's|^\./||' | while read -r f; do sha "$f"; done >SHA256SUMS)
refused "a changed binary with a re-made SHA256SUMS" "$T/resummed"
mkrel 0.3.0 "$T/unsigned"
rm "$T/unsigned/SHA256SUMS.sig"
refused "an unsigned release" "$T/unsigned"
grep -q 'not signed' "$T/out" || fail "unsigned message: $(cat "$T/out")"
mkrel 0.3.0 "$T/nokey"
(TW_RELEASE_SIGNERS=/dev/null && refused "with no key pinned" "$T/nokey")
grep -q 'pins no release signing key' "$T/out" || fail "no pinned key message: $(cat "$T/out")"
rm -rf "$P"
sh "$T/unsigned/install.sh" --allow-unsigned >"$T/out" 2>&1 || fail "--allow-unsigned: $(cat "$T/out")"
grep -q WARNING "$T/out" || fail "--allow-unsigned did not warn"
rm -rf "$P"
mkrel 0.3.0-abcdef12 "$T/local"
rm "$T/local/SHA256SUMS.sig"
sh "$T/local/install.sh" >"$T/out" 2>&1 || fail "a local build: $(cat "$T/out")"
grep -q 'local build' "$T/out" || fail "a local build did not warn"
rm -rf "$P"
mkrel 0.3.0 "$T/good"
sh "$T/good/install.sh" >"$T/out" 2>&1 || fail "a signed release: $(cat "$T/out")"
grep -q 'signed by a tw release key' "$T/out" || fail "signed release message: $(cat "$T/out")"
rm -rf "$P"

# get.sh: SHA256SUMS.sig is checked before the tarball is opened.
mkmirror() { # dir version: a fake GitHub release (tarball, SHA256SUMS, SHA256SUMS.sig)
	n=tailworks-$2-$os-$arch
	mkdir -p "$1/stage"
	mkrel "$2" "$1/stage/$n"
	tar -czf "$1/$n.tar.gz" -C "$1/stage" "$n"
	rm -rf "$1/stage"
	(cd "$1" && sha "$n.tar.gz" >SHA256SUMS)
	sign "$T/key" "$1/SHA256SUMS"
}
get() { # dir version
	TW_RELEASE_BASE="file://$1" sh "$ROOT/scripts/get.sh" "v$2" >"$T/out" 2>&1
}
mkmirror "$T/m-good" 0.3.0
get "$T/m-good" 0.3.0 || fail "get.sh a signed release: $(cat "$T/out")"
[ "$(cat "$P/VERSION")" = 0.3.0 ] || fail "get.sh did not install"
# Over a running tw: refused; TW_INSTALL_FORCE=1 installs anyway.
sleep 30 &
pid=$!
echo $pid >"$P/supervisor.pid"
mkmirror "$T/m-next" 0.4.0
if get "$T/m-next" 0.4.0; then fail "get.sh installed over a running tw"; fi
TW_INSTALL_FORCE=1 get "$T/m-next" 0.4.0 || fail "TW_INSTALL_FORCE=1 get.sh: $(cat "$T/out")"
[ "$(cat "$P/VERSION")" = 0.4.0 ] || fail "TW_INSTALL_FORCE=1 did not install"
kill $pid 2>/dev/null || true
wait $pid 2>/dev/null || true
rm -rf "$P"
mkmirror "$T/m-bad" 0.3.0
sign "$T/other" "$T/m-bad/SHA256SUMS"
if get "$T/m-bad" 0.3.0; then fail "get.sh installed a release signed by an unpinned key"; fi
grep -q 'not a valid signature' "$T/out" || fail "get.sh bad signature message: $(cat "$T/out")"
[ ! -e "$P/bin" ] || fail "get.sh installed with a bad signature"
mkmirror "$T/m-unsigned" 0.3.0
rm "$T/m-unsigned/SHA256SUMS.sig"
if get "$T/m-unsigned" 0.3.0; then fail "get.sh installed an unsigned release"; fi
grep -q 'no SHA256SUMS.sig' "$T/out" || fail "get.sh unsigned message: $(cat "$T/out")"
# No version skips the check: an "old" or non-numeric tag with no
# signature is refused too (whoever can publish a release picks the tag).
for v in 0.2.0 0.2.99; do
	mkmirror "$T/m-old-$v" "$v"
	rm "$T/m-old-$v/SHA256SUMS.sig"
	if get "$T/m-old-$v" "$v"; then fail "get.sh installed unsigned release $v"; fi
	[ ! -e "$P/bin" ] || fail "get.sh installed unsigned release $v"
done
echo "install_test: ok"
