#!/bin/sh
# tailworks installer: run from the extracted release directory.
#
#   ./install.sh                 install (or upgrade, or repair) into ~/.tailworks
#   ./install.sh --prefix DIR    install into DIR instead
#   ./install.sh --force         replace a different version while tw runs from the prefix
#   ./install.sh --uninstall     remove bin/ and share/ (keeps tailworks state: tw.json, logs, …)
#   ./install.sh --uninstall --purge   remove the whole prefix, state and backups/ included (run `tw down --purge` first)
#   ~/.tailworks/install.sh --rollback swap the previous install (bin.prev, share.prev) back in
#                                (`tw upgrade rollback` does it with tw stopped and restarted, the store too when it must)
#   ./install.sh --allow-unsigned      install a release without a valid SHA256SUMS.sig (local builds)
#
# The release must be signed: SHA256SUMS.sig, an SSH signature of SHA256SUMS
# by a key pinned below (ssh-keygen -Y verify). Local builds (a version with
# a "-" suffix, as `make release` stamps) may be unsigned; the check is
# skipped, with a warning, when ssh-keygen is missing. No sudo, nothing outside the prefix is touched (your shell profile
# included: it prints the PATH line to add). Re-running is safe. An upgrade
# keeps the version it replaces as bin.prev/share.prev/VERSION.prev (a
# re-install of the same version keeps the older .prev), and copies this
# script to <prefix>/install.sh so --rollback works without the tarball.
# --rollback refuses while tailworks runs from the prefix (tw down first, or
# --force); rolling back twice returns to where you started. So does an
# install of another version over a running tailworks: `tw upgrade` is the
# way to do that (it backs up, health-checks and rolls back); a same-version
# re-run (a repair) goes ahead. `tw uninstall` is the full uninstall;
# --uninstall --purge is its fallback when no tw binary runs.
set -eu
PREFIX=${TW_HOME:-$HOME/.tailworks}
MODE=install
PURGE=0
FORCE=0
ALLOW_UNSIGNED=${TW_ALLOW_UNSIGNED:-0}
# The release signing keys (allowed_signers lines; TW_RELEASE_SIGNERS=FILE
# overrides, for tests and private builds).
# BEGIN tw release signers (copied from internal/releasesig/allowed_signers by scripts/release-keygen.sh --sync)
SIGNERS='
tw-release namespaces="tw-release" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC8VB/53QVWaiyARbdrg6MP6F6LVuQPebi4lhMN4h0Yk tw-release 2026-11-17
'
# END tw release signers
while [ $# -gt 0 ]; do
	case $1 in
	--prefix) PREFIX=$2; shift ;;
	--prefix=*) PREFIX=${1#*=} ;;
	--uninstall) MODE=uninstall ;;
	--purge) PURGE=1 ;;
	--force) FORCE=1 ;;
	--rollback) MODE=rollback ;;
	--allow-unsigned) ALLOW_UNSIGNED=1 ;;
	-h | --help) sed -n '2,20p' "$0"; exit 0 ;;
	*) echo "install.sh: unknown argument $1" >&2; exit 2 ;;
	esac
	shift
done

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" != 0 ] || die "don't run as root (tailworks installs per user, into ~/.tailworks, and needs no sudo)"

running() {
	pf=$PREFIX/supervisor.pid
	[ -f "$pf" ] && kill -0 "$(cat "$pf")" 2>/dev/null
}

if [ "$MODE" = uninstall ]; then
	if running && [ "$FORCE" != 1 ]; then
		die "tw is running from $PREFIX (tw down first, or pass --force)"
	fi
	if [ "$PURGE" = 1 ]; then
		# The same end state as `tw uninstall` for the prefix: all of it.
		case $PREFIX in
		"" | / | "$HOME" | "$HOME/") die "won't remove $PREFIX" ;;
		esac
		rm -rf "$PREFIX"
	fi
	rm -rf "$PREFIX/bin" "$PREFIX/share" "$PREFIX/VERSION" "$PREFIX/.install-"* \
		"$PREFIX/bin.prev" "$PREFIX/share.prev" "$PREFIX/VERSION.prev" "$PREFIX/install.sh"
	rmdir "$PREFIX" 2>/dev/null || true
	if [ -d "$PREFIX" ]; then
		say "✓ uninstalled tw from $PREFIX (state kept: $(ls -A "$PREFIX" | tr '\n' ' '))"
	else
		say "✓ uninstalled tw ($PREFIX removed)"
	fi
	say "  remove the PATH line for $PREFIX/bin from your shell profile if you added one"
	exit 0
fi

if [ "$MODE" = rollback ]; then
	[ -d "$PREFIX/bin.prev" ] && [ -d "$PREFIX/share.prev" ] || die "no previous install in $PREFIX (bin.prev, share.prev) to roll back to"
	if running && [ "$FORCE" != 1 ]; then
		die "tw is running from $PREFIX (tw down first, or pass --force)"
	fi
	cur=$(cat "$PREFIX/VERSION" 2>/dev/null || echo unknown)
	prev=$(cat "$PREFIX/VERSION.prev" 2>/dev/null || echo unknown)
	# Swap current and previous, so a second --rollback undoes the first.
	for d in bin share VERSION; do
		rm -rf "$PREFIX/$d.swap"
		[ -e "$PREFIX/$d" ] && mv "$PREFIX/$d" "$PREFIX/$d.swap"
		[ -e "$PREFIX/$d.prev" ] && mv "$PREFIX/$d.prev" "$PREFIX/$d"
		[ -e "$PREFIX/$d.swap" ] && mv "$PREFIX/$d.swap" "$PREFIX/$d.prev"
	done
	say "✓ rolled back tw $cur → $prev in $PREFIX ($cur is kept as bin.prev: --rollback again to return)"
	say "  if a contract migration ran since, put the store back too: tw restore <state dir>/backups/<time>-$prev --yes (tw upgrade rollback does both)"
	exit 0
fi

SRC=$(cd "$(dirname "$0")" && pwd)
[ -f "$SRC/VERSION" ] && [ -x "$SRC/bin/tw" ] || die "run me from the extracted tw release directory"
VERSION=$(cat "$SRC/VERSION")
t0=$(date +%s)

# Another version over a running tailworks: refuse, don't swap binaries
# under it (no backup, no health check, no rollback). tw upgrade does it
# properly; tw upgrade and hostd's self-upgrade pass --force.
cur=$(cat "$PREFIX/VERSION" 2>/dev/null || true)
if [ -n "$cur" ] && [ "$cur" != "$VERSION" ] && running && [ "$FORCE" != 1 ]; then
	die "tw $cur is running from $PREFIX: \`$PREFIX/bin/tw upgrade --from $SRC\` backs up, health-checks and rolls back; or \`tw down\` first; --force swaps anyway"
fi

# Platform check.
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
[ "$arch" = aarch64 ] && arch=arm64
[ "$arch" = x86_64 ] && arch=amd64
want=$(sed -n 's/.*"os":"\([^"]*\)","arch":"\([^"]*\)".*/\1-\2/p' "$SRC/share/tailworks/release.json")
[ "$os-$arch" = "$want" ] || die "this release is for $want, this machine is $os-$arch"

# Linux: workerd is linked against glibc and needs >= 2.35 (Ubuntu 22.04,
# Debian 12, Fedora 36, RHEL 10). musl (Alpine) and older glibc (Debian 11,
# RHEL/Alma 8-9, Amazon Linux 2) can't run it. `bin/tw doctor` (static) says
# the same from the extracted directory.
if [ "$os" = linux ]; then
	glibc=$(getconf GNU_LIBC_VERSION 2>/dev/null | awk '{print $2}')
	if [ -z "$glibc" ]; then
		if ls /lib/ld-musl-* >/dev/null 2>&1; then
			die "this Linux uses musl libc (Alpine?); tailworks' workerd needs glibc >= 2.35. Use a glibc distro (Ubuntu 22.04+, Debian 12+, Fedora 36+), or a glibc container/VM. Details: $SRC/bin/tw doctor"
		fi
		die "can't determine the glibc version (getconf GNU_LIBC_VERSION); tailworks' workerd needs glibc >= 2.35. Details: $SRC/bin/tw doctor"
	fi
	gmaj=${glibc%%.*}
	gmin=${glibc#*.}
	gmin=${gmin%%.*}
	if [ "$gmaj" -lt 2 ] || { [ "$gmaj" -eq 2 ] && [ "$gmin" -lt 35 ]; }; then
		die "glibc $glibc is too old: tailworks' workerd needs glibc >= 2.35 (Ubuntu 22.04+, Debian 12+, Fedora 36+, RHEL 10). Details: $SRC/bin/tw doctor"
	fi
fi

# Authenticity: SHA256SUMS is signed by a pinned release key.
if [ "$ALLOW_UNSIGNED" = 1 ]; then
	say "WARNING: not checking the release signature (--allow-unsigned)"
elif [ ! -f "$SRC/SHA256SUMS.sig" ]; then
	case $VERSION in
	*-*) say "WARNING: $VERSION is a local build with no SHA256SUMS.sig: not signature-checked" ;;
	*) die "this release is not signed (no SHA256SUMS.sig). Install releases from GitHub (scripts/get.sh), or pass --allow-unsigned for a build you made" ;;
	esac
else
	# The pinned key first: an install.sh that pins none refuses, even
	# without ssh-keygen ("no key" never means "accept").
	signers=$(mktemp)
	if [ -n "${TW_RELEASE_SIGNERS:-}" ]; then cat "$TW_RELEASE_SIGNERS" >"$signers"; else printf '%s\n' "$SIGNERS" >"$signers"; fi
	grep -q '^tw-release ' "$signers" || { rm -f "$signers"; die "this install.sh pins no release signing key, so it can't check SHA256SUMS.sig (--allow-unsigned to install anyway)"; }
	if ! command -v ssh-keygen >/dev/null 2>&1; then
		say "WARNING: no ssh-keygen (OpenSSH): can't check SHA256SUMS.sig (see INSTALL.md, \"Verify a release\")"
	elif ! ssh-keygen -Y verify -f "$signers" -I tw-release -n tw-release -s "$SRC/SHA256SUMS.sig" <"$SRC/SHA256SUMS" >/dev/null 2>&1; then
		rm -f "$signers"
		die "SHA256SUMS.sig is not a valid signature by a tw release key: refusing to install"
	else
		say "✓ signed by a tw release key"
	fi
	rm -f "$signers"
fi

# Integrity (hashing ~300 MB takes a few seconds; say so).
nfiles=$(grep -c . "$SRC/SHA256SUMS" 2>/dev/null || echo "?")
say "verifying $nfiles files (SHA256SUMS)…"
if command -v sha256sum >/dev/null 2>&1; then
	(cd "$SRC" && sha256sum -c --quiet SHA256SUMS) || die "SHA256SUMS check failed: the release is corrupt"
else
	(cd "$SRC" && shasum -a 256 -c --quiet SHA256SUMS) || die "SHA256SUMS check failed: the release is corrupt"
fi
say "✓ verified"

# Copy into a staging dir in the prefix, then swap (re-runs and upgrades).
mkdir -p "$PREFIX"
chmod 700 "$PREFIX"
STAGE=$PREFIX/.install-$$
rm -rf "$STAGE"
mkdir -p "$STAGE"
trap 'rm -rf "$STAGE"' EXIT
cp -R "$SRC/bin" "$SRC/share" "$SRC/VERSION" "$STAGE/"
# The long name works too: tailworks → tw.
ln -sf tw "$STAGE/bin/tailworks"
if [ "$os" = darwin ]; then
	# Release binaries are ad-hoc signed; files fetched by a browser carry
	# com.apple.quarantine (scp/curl don't). Clear it on our copy only.
	xattr -dr com.apple.quarantine "$STAGE" 2>/dev/null || true
fi
# Smoke-test before swapping: the binaries must run on this machine.
"$STAGE/bin/workerd" --version >/dev/null 2>&1 || die "workerd does not run on this machine ($STAGE/bin/workerd --version)"
"$STAGE/bin/tw" version >/dev/null 2>&1 || die "tw does not run on this machine"
# The install being replaced becomes .prev (install.sh --rollback), unless
# it is this same version (a repair): then the older .prev is kept.
old=$(cat "$PREFIX/VERSION" 2>/dev/null || true)
keepprev=0
{ [ -z "$old" ] || [ "$old" = "$VERSION" ]; } && keepprev=1
for d in bin share; do
	rm -rf "$PREFIX/$d.old"
	[ -e "$PREFIX/$d" ] && mv "$PREFIX/$d" "$PREFIX/$d.old"
	mv "$STAGE/$d" "$PREFIX/$d"
	if [ $keepprev = 0 ] && [ -e "$PREFIX/$d.old" ]; then
		rm -rf "$PREFIX/$d.prev"
		mv "$PREFIX/$d.old" "$PREFIX/$d.prev"
	fi
	rm -rf "$PREFIX/$d.old"
done
[ $keepprev = 1 ] || printf '%s\n' "$old" >"$PREFIX/VERSION.prev"
mv "$STAGE/VERSION" "$PREFIX/VERSION"
cp "$SRC/install.sh" "$PREFIX/install.sh.tmp" && chmod 755 "$PREFIX/install.sh.tmp" && mv "$PREFIX/install.sh.tmp" "$PREFIX/install.sh"

say "✓ installed tw $VERSION into $PREFIX ($(($(date +%s) - t0)) s)"
[ $keepprev = 1 ] || say "  previous install ($old) kept as bin.prev/share.prev: tw upgrade rollback restores it"
say "  $("$PREFIX/bin/tw" version)"
say "  $("$PREFIX/bin/workerd" --version) · process tier ($( [ "$os" = darwin ] && echo "dev-grade isolation on macOS; microVM tier on Virtualization.framework when the release has share/tailworks/vm" || echo "see tw doctor for the sandbox and the microVM tier"))"
case ":$PATH:" in
*":$PREFIX/bin:"*) ;;
*)
	prof="~/.profile"
	case ${SHELL:-} in
	*/zsh) prof="~/.zprofile" ;;
	*/bash)
		# On Linux, creating ~/.bash_profile would make bash skip ~/.profile.
		if [ "$os" = darwin ] || [ -f "$HOME/.bash_profile" ]; then prof="~/.bash_profile"; fi ;;
	esac
	say ""
	say "Add tw to your PATH (this installer doesn't edit your shell profile):"
	say "  echo 'export PATH=\"$PREFIX/bin:\$PATH\"' >> $prof && export PATH=\"$PREFIX/bin:\$PATH\""
	;;
esac
say ""
say "Next: tw doctor   then   tw up --oauth-client-file <file>   (an OAuth client: no browser, no Funnel; see INSTALL.md)"
