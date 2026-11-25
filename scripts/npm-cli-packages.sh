#!/bin/sh
# npm-cli-packages.sh <version> <dist dir> <out dir> [targets…]: the npm
# platform packages of the tw CLI (issue #240), made from the release
# tarballs, so npm ships exactly the binaries the GitHub release does.
#
#   <out dir>/cli-<os>-<arch>/   @tailworks/cli-<os>-<arch>, ready for npm publish
#
# <dist dir> holds tailworks-<version>-<target>.tar.gz and SHA256SUMS (what
# scripts/release.sh writes, and what the GitHub release carries).
# SHA256SUMS must be signed by a pinned release key
# (internal/releasesig/allowed_signers; TW_RELEASE_SIGNERS overrides, for
# tests) unless the version is a local build ("x.y.z-…"), and each tarball
# must match it. Default targets: darwin-arm64 linux-arm64 linux-amd64, the
# ones js/cli/tailworks lists as optionalDependencies.
#
# Each package is the extracted tarball (bin/, share/tailworks, VERSION,
# LICENSE, THIRD_PARTY_NOTICES, INSTALL.md) without:
#   - install.sh, SHA256SUMS(.sig): npm installs it, and npm's integrity
#     and provenance cover it;
#   - share/tailworks/vm, the microVM guest (kernel + rootfs, ~200 MB): the
#     microVM tier stays tarball-only (tw doctor says so);
# and with release.json's "microvm" false and "install":"npm", plus a
# package.json with "os"/"cpu" (and "libc" on Linux) so npm installs it only
# where it runs. tw recognises the package.json (internal/npminstall).
set -eu
V=${1:?usage: npm-cli-packages.sh <version> <dist dir> <out dir> [targets…]}
V=${V#v}
DIST=$(cd "${2:?dist dir}" && pwd)
OUT=${3:?out dir}
shift 3
TARGETS=${*:-darwin-arm64 linux-arm64 linux-amd64}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
die() { echo "npm-cli-packages: $*" >&2; exit 1; }

[ -f "$DIST/SHA256SUMS" ] || die "$DIST/SHA256SUMS missing"
if [ -f "$DIST/SHA256SUMS.sig" ]; then
	ssh-keygen -Y verify -f "${TW_RELEASE_SIGNERS:-$ROOT/internal/releasesig/allowed_signers}" -I tw-release -n tw-release \
		-s "$DIST/SHA256SUMS.sig" <"$DIST/SHA256SUMS" >/dev/null || die "$DIST/SHA256SUMS.sig does not verify against a pinned release key"
else
	case $V in
	*-*) echo "npm-cli-packages: WARNING: $V is a local build with no SHA256SUMS.sig: not signature-checked" ;;
	*) die "$DIST/SHA256SUMS is not signed (no SHA256SUMS.sig): a release's npm packages come from its signed tarballs" ;;
	esac
fi

for t in $TARGETS; do
	os=${t%-*}
	arch=${t#*-}
	name=tailworks-$V-$t
	tgz=$DIST/$name.tar.gz
	[ -f "$tgz" ] || die "$tgz missing"
	(cd "$DIST" && grep " $name.tar.gz\$" SHA256SUMS | sha256 -c - >/dev/null) || die "$name.tar.gz does not match SHA256SUMS"
	pkg=$OUT/cli-$t
	rm -rf "$pkg" "$OUT/.x"
	mkdir -p "$OUT/.x"
	tar -xzf "$tgz" -C "$OUT/.x"
	[ -d "$OUT/.x/$name" ] || die "$tgz has no $name/"
	mv "$OUT/.x/$name" "$pkg"
	rm -rf "$OUT/.x"
	rm -rf "$pkg/install.sh" "$pkg/SHA256SUMS" "$pkg/SHA256SUMS.sig" "$pkg/share/tailworks/vm"
	[ -x "$pkg/bin/tw" ] && [ -x "$pkg/bin/workerd" ] || die "$name: no bin/tw or bin/workerd"
	[ "$(cat "$pkg/VERSION")" = "$V" ] || die "$name: VERSION is $(cat "$pkg/VERSION"), want $V"
	node - "$pkg" "$V" "$os" "$arch" <<'EOF'
const fs = require("node:fs");
const path = require("node:path");
const [dir, version, os, arch] = process.argv.slice(2);
const rj = path.join(dir, "share", "tailworks", "release.json");
const rel = JSON.parse(fs.readFileSync(rj, "utf8"));
if (rel.os !== os || rel.arch !== arch || rel.version !== version) {
  console.error(`npm-cli-packages: ${rj} is ${rel.version} ${rel.os}-${rel.arch}, want ${version} ${os}-${arch}`);
  process.exit(1);
}
rel.microvm = false; // the guest stays in the tarball
rel.install = "npm";
fs.writeFileSync(rj, JSON.stringify(rel) + "\n");
const files = fs.readdirSync(dir).filter((f) => f !== "package.json" && f !== "README.md").sort();
const p = {
  name: `@tailworks/cli-${os}-${arch}`,
  version,
  description: `The tailworks CLI (tw) and its daemons, workerd and bundler for ${os}-${arch}: installed by the "tailworks" package`,
  // npm (and node) call amd64 "x64".
  os: [os],
  cpu: [arch === "amd64" ? "x64" : arch],
  ...(os === "linux" ? { libc: ["glibc"] } : {}),
  files,
  preferUnplugged: true,
  publishConfig: { access: "public" },
  repository: { type: "git", url: "git+https://github.com/rosskukulinski/tailworks.git" },
  homepage: "https://github.com/rosskukulinski/tailworks#readme",
  bugs: "https://github.com/rosskukulinski/tailworks/issues",
  license: "BSD-3-Clause",
};
fs.writeFileSync(path.join(dir, "package.json"), JSON.stringify(p, null, 2) + "\n");
fs.writeFileSync(
  path.join(dir, "README.md"),
  `# ${p.name}\n\nThe [tailworks](https://github.com/rosskukulinski/tailworks) CLI for ${os}-${arch}, from the ${version} release tarball (without the microVM guest).\n\nDon't install this package directly: install \`tailworks\` (\`npm install -g tailworks\`, or \`npx tailworks …\`), which picks the package for your platform.\n`,
);
EOF
	# npm pack leaves out some files wherever they are (.npmignore,
	# .gitignore, .npmrc, …) and honours a nested .npmignore. Every file
	# that matters must survive: compare the pack list with the payload.
	(cd "$pkg" && npm pack --dry-run --json 2>/dev/null) | node -e '
const fs = require("node:fs"), path = require("node:path");
const dir = process.argv[1];
const packed = new Set(JSON.parse(fs.readFileSync(0, "utf8"))[0].files.map((f) => f.path));
const dropOK = new Set([".npmignore", ".gitignore", ".npmrc", ".DS_Store", "npm-debug.log"]);
const lost = [];
(function walk(d) {
  for (const e of fs.readdirSync(path.join(dir, d), { withFileTypes: true })) {
    const rel = d ? d + "/" + e.name : e.name;
    if (e.isDirectory()) walk(rel);
    else if (!packed.has(rel) && !dropOK.has(e.name)) lost.push(rel);
  }
})("");
if (lost.length) {
  console.error("npm-cli-packages: npm pack would leave out " + lost.length + " files of " + dir + ":\n  " + lost.slice(0, 20).join("\n  "));
  process.exit(1);
}
console.log("npm-cli-packages: npm pack keeps all " + packed.size + " files");
' "$pkg"
	echo "npm-cli-packages: $pkg ($(du -sk "$pkg" | awk '{printf "%.0f MB", $1/1024}') unpacked)"
done
