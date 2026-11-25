#!/bin/sh
# npm_cli_test.sh: the tw CLI's npm packages (issue #240), hermetically.
# Fake signed release tarballs (a throwaway key that TW_RELEASE_SIGNERS
# pins) → scripts/npm-cli-packages.sh → npm pack → npm install -g into a
# temp prefix, offline, then tw runs through the tailworks shim.
#
#   - npm-cli-packages.sh refuses an unsigned release, a signature by
#     another key and a tarball that doesn't match SHA256SUMS;
#   - each platform package drops install.sh, SHA256SUMS and the microVM
#     guest, keeps the vendored node_modules, says "install":"npm" and
#     "microvm":false, and has the right os/cpu (and libc on Linux);
#   - npm install -g of tailworks + this platform's package puts tw and
#     tailworks on PATH; they run bin/tw with the arguments and exit status.
#
# The real binaries are checked per platform by release.yml's tarball jobs
# (scripts/ci-npm-cli-check.sh). Run: sh scripts/npm_cli_test.sh
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
sha() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
command -v ssh-keygen >/dev/null || fail "needs ssh-keygen (OpenSSH)"
command -v npm >/dev/null || fail "needs npm"

case "$(uname -s)-$(uname -m)" in
Darwin-arm64) HOST=darwin-arm64 ;;
Linux-aarch64 | Linux-arm64) HOST=linux-arm64 ;;
Linux-x86_64) HOST=linux-amd64 ;;
*) HOST="" ;;
esac
V=$(cat "$ROOT/VERSION")
TARGETS="darwin-arm64 linux-arm64 linux-amd64"

ssh-keygen -q -t ed25519 -N '' -C test -f "$T/key"
ssh-keygen -q -t ed25519 -N '' -C other -f "$T/other"
printf 'tw-release namespaces="tw-release" %s\n' "$(cat "$T/key.pub")" >"$T/allowed_signers"
export TW_RELEASE_SIGNERS=$T/allowed_signers

# A release dist dir: one tarball per target, SHA256SUMS signed by $1.
mkdist() { # key dir
	mkdir -p "$2"
	for t in $TARGETS; do
		n=tailworks-$V-$t
		d=$T/stage/$n
		rm -rf "$d"
		mkdir -p "$d/bin" "$d/share/tailworks/vm" "$d/share/tailworks/node_modules/dep" "$d/share/tailworks/create-tailworks/templates/hello"
		printf '#!/bin/sh\necho "tw %s $*"\n[ "$1" = fail ] && exit 7\nexit 0\n' "$V" >"$d/bin/tw"
		printf '#!/bin/sh\necho workerd\n' >"$d/bin/workerd"
		chmod 755 "$d/bin/"*
		printf '{"version":"%s","os":"%s","arch":"%s","microvm":true}\n' "$V" "${t%-*}" "${t#*-}" >"$d/share/tailworks/release.json"
		echo kernel >"$d/share/tailworks/vm/vmlinux"
		echo '{"name":"dep"}' >"$d/share/tailworks/node_modules/dep/package.json"
		echo 'module.exports = 1' >"$d/share/tailworks/node_modules/dep/index.js"
		echo '{}' >"$d/share/tailworks/create-tailworks/templates/hello/package.json"
		echo "$V" >"$d/VERSION"
		echo license >"$d/LICENSE"
		echo '#!/bin/sh' >"$d/install.sh"
		(cd "$d" && find . -type f | sed 's|^\./||' | while read -r f; do sha "$f"; done >SHA256SUMS)
		tar -czf "$2/$n.tar.gz" -C "$T/stage" "$n"
	done
	(cd "$2" && sha tailworks-*.tar.gz >SHA256SUMS)
	ssh-keygen -q -Y sign -f "$1" -n tw-release "$2/SHA256SUMS"
}

echo "-- refuses what isn't a signed, intact release"
mkdist "$T/other" "$T/badkey"
refuses() { # dist dir, message
	if out=$(sh "$ROOT/scripts/npm-cli-packages.sh" "$V" "$1" "$T/o" 2>&1); then fail "accepted $1"; fi
	case $out in *"$2"*) ;; *) fail "$1: want \"$2\", got: $out" ;; esac
}
refuses "$T/badkey" "does not verify against a pinned release key"
mkdist "$T/key" "$T/unsigned"
rm "$T/unsigned/SHA256SUMS.sig"
case $V in
*-*) ;; # a local version may be unsigned
*) refuses "$T/unsigned" "is not signed" ;;
esac
mkdist "$T/key" "$T/tampered"
echo x >>"$T/tampered/tailworks-$V-linux-arm64.tar.gz"
refuses "$T/tampered" "linux-arm64.tar.gz does not match SHA256SUMS"

echo "-- builds a package per target"
mkdist "$T/key" "$T/dist"
sh "$ROOT/scripts/npm-cli-packages.sh" "$V" "$T/dist" "$T/out" >/dev/null
for t in $TARGETS; do
	p=$T/out/cli-$t
	for f in install.sh SHA256SUMS SHA256SUMS.sig share/tailworks/vm; do
		[ ! -e "$p/$f" ] || fail "$t: $f is in the npm package"
	done
	[ -f "$p/share/tailworks/node_modules/dep/index.js" ] || fail "$t: vendored node_modules missing"
	node - "$p" "$t" <<'EOF' || fail "$t: package.json / release.json"
const fs = require("node:fs");
const [dir, t] = process.argv.slice(2);
const [os, arch] = t.split("-");
const p = JSON.parse(fs.readFileSync(dir + "/package.json", "utf8"));
const r = JSON.parse(fs.readFileSync(dir + "/share/tailworks/release.json", "utf8"));
const want = (c, m) => { if (!c) { console.error(t + ": " + m); process.exit(1); } };
want(p.name === "@tailworks/cli-" + t, "name " + p.name);
want(p.os.join() === os && p.cpu.join() === (arch === "amd64" ? "x64" : arch), "os/cpu " + p.os + "/" + p.cpu);
want(os !== "linux" || (p.libc || []).join() === "glibc", "libc " + p.libc);
want(p.files.includes("bin") && p.files.includes("share") && !p.files.includes("install.sh"), "files " + p.files);
want(r.install === "npm" && r.microvm === false, "release.json " + JSON.stringify(r));
EOF
done

if [ -z "$HOST" ]; then
	echo "npm_cli_test: no tailworks build for $(uname -s)-$(uname -m): skipping the npm install"
	echo "npm_cli_test: ok"
	exit 0
fi

echo "-- npm install -g tailworks + @tailworks/cli-$HOST (offline), then tw"
mkdir -p "$T/pkgs"
(cd "$T/out/cli-$HOST" && npm pack --silent --pack-destination "$T/pkgs" >/dev/null)
(cd "$ROOT/js/cli/tailworks" && npm pack --silent --pack-destination "$T/pkgs" >/dev/null)
G=$T/global
npm install -g --prefix "$G" --offline --cache "$T/npm-cache" --no-audit --no-fund --update-notifier=false --loglevel=error \
	"$T/pkgs/tailworks-$V.tgz" "$T/pkgs/tailworks-cli-$HOST-$V.tgz" >/dev/null
for b in tw tailworks; do
	[ "$("$G/bin/$b" dev --port 0)" = "tw $V dev --port 0" ] || fail "$b: didn't run the platform package's tw"
done
st=0
"$G/bin/tw" fail >/dev/null || st=$?
[ "$st" = 7 ] || fail "tw's exit status: got $st, want 7"
echo "npm_cli_test: ok"
