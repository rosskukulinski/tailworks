#!/bin/sh
# Download a published tw release from GitHub, verify it, and install it.
#
#   sh get.sh                    the latest release, into ~/.tailworks
#   sh get.sh vX.Y.Z             a given release
#   TW_HOME=DIR sh get.sh        install into DIR
#   TW_RELEASE_BASE=URL sh get.sh vX.Y.Z   download from a mirror instead of GitHub
#   TW_ALLOW_UNSIGNED=1 sh get.sh vX.Y.Z   skip the signature check (your own builds only)
#
# It downloads tailworks-<version>-<os>-<arch>.tar.gz, SHA256SUMS and
# SHA256SUMS.sig, checks that SHA256SUMS is signed by a tailworks release key
# pinned below (ssh-keygen -Y verify; an unsigned release is refused),
# checks the tarball's hash, extracts it into a fresh temporary directory,
# and runs its install.sh (no sudo; touches nothing outside ~/.tailworks,
# your shell profile included). Read it first: it's short.
set -eu
REPO=${TW_REPO:-rosskukulinski/tailworks}
TAG=${1:-latest}
# The release signing keys (allowed_signers lines; TW_RELEASE_SIGNERS=FILE
# overrides, for tests and mirrors of private builds).
# BEGIN tw release signers (copied from internal/releasesig/allowed_signers by scripts/release-keygen.sh --sync)
SIGNERS='
tw-release namespaces="tw-release" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC8VB/53QVWaiyARbdrg6MP6F6LVuQPebi4lhMN4h0Yk tw-release 2026-11-17
'
# END tw release signers

die() { printf 'get.sh: %s\n' "$*" >&2; exit 1; }
warn() { printf 'get.sh: WARNING: %s\n' "$*" >&2; }
[ "$(id -u)" != 0 ] || die "don't run as root (tailworks installs per user and needs no sudo)"
command -v curl >/dev/null || die "needs curl"
command -v tar >/dev/null || die "needs tar"

case $(uname -s) in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) die "unsupported OS $(uname -s) (tailworks runs on macOS and Linux)" ;;
esac
case $(uname -m) in
arm64 | aarch64) arch=arm64 ;;
x86_64 | amd64) arch=amd64 ;;
*) die "unsupported CPU $(uname -m) (arm64 or amd64)" ;;
esac
[ "$os-$arch" != darwin-amd64 ] || die "tw on macOS needs Apple silicon (arm64)"

if [ "$TAG" = latest ]; then
	TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
		sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$TAG" ] || die "no published release found in $REPO (build one: see docs/INSTALL.md)"
fi
VERSION=${TAG#v}
NAME=tailworks-$VERSION-$os-$arch.tar.gz
BASE=${TW_RELEASE_BASE:-https://github.com/$REPO/releases/download/$TAG}

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
printf 'downloading %s (%s)…\n' "$NAME" "$TAG"
curl -fsSL -o "$TMP/$NAME" "$BASE/$NAME" || die "no $NAME in release $TAG"
curl -fsSL -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS" || die "no SHA256SUMS in release $TAG"

# Authenticity: SHA256SUMS is signed by a pinned key (before the hashes in it are trusted).
if [ "${TW_ALLOW_UNSIGNED:-0}" = 1 ]; then
	warn "not checking the release signature (TW_ALLOW_UNSIGNED=1)"
elif ! curl -fsSL -o "$TMP/SHA256SUMS.sig" "$BASE/SHA256SUMS.sig" 2>/dev/null; then
	# Every published release is signed (no unsigned one was ever
	# published), so there is no "old release" exception: one would let
	# whoever can publish a release pick a tag that skips this check.
	die "release $TAG has no SHA256SUMS.sig: refusing an unsigned release (TW_ALLOW_UNSIGNED=1 to install it anyway)"
else
	command -v ssh-keygen >/dev/null || die "needs ssh-keygen (OpenSSH) to check the release signature"
	if [ -n "${TW_RELEASE_SIGNERS:-}" ]; then cat "$TW_RELEASE_SIGNERS" >"$TMP/allowed_signers"; else printf '%s\n' "$SIGNERS" >"$TMP/allowed_signers"; fi
	grep -q '^tw-release ' "$TMP/allowed_signers" || die "this get.sh pins no release signing key; get a newer one"
	ssh-keygen -Y verify -f "$TMP/allowed_signers" -I tw-release -n tw-release -s "$TMP/SHA256SUMS.sig" <"$TMP/SHA256SUMS" >/dev/null 2>&1 ||
		die "SHA256SUMS.sig in release $TAG is not a valid signature by a tw release key: refusing to install"
	printf 'signed by a tw release key\n'
fi

want=$(awk -v n="$NAME" '$2 == n || $2 == "*"n { print $1 }' "$TMP/SHA256SUMS")
[ -n "$want" ] || die "SHA256SUMS has no entry for $NAME"
if command -v sha256sum >/dev/null; then
	got=$(sha256sum "$TMP/$NAME" | awk '{print $1}')
else
	got=$(shasum -a 256 "$TMP/$NAME" | awk '{print $1}')
fi
[ "$got" = "$want" ] || die "checksum mismatch for $NAME (got $got, want $want)"

mkdir "$TMP/x"
tar -xzf "$TMP/$NAME" -C "$TMP/x"
set -- "$TMP"/x/tailworks-*/install.sh
[ -f "$1" ] || die "the tarball has no install.sh"
sh "$1"
