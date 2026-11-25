#!/usr/bin/env bash
# ci-npm-cli-check.sh <dist dir> <platform> <version>: the tw CLI's npm
# packages (issue #240) on their own platform, before they are published
# (release.yml's tarball jobs run it after ci-tarball-check.sh).
#
#   1. scripts/npm-cli-packages.sh makes @tailworks/cli-<platform> from the
#      signed tarball; npm pack it and the tailworks package (stamped with
#      <version>, as npm-publish.sh publishes them);
#   2. npm install -g both into a fresh prefix, offline: tw and tailworks
#      are on PATH and run this release (tw version);
#   3. tw doctor passes and says it is an npm install;
#   4. tw init hello, then tw dev --once through the npm-installed tw (the
#      package's daemons, workerd, bundler and vendored modules) gets the
#      app's answer;
#   5. tw upgrade refuses with npm_install (npm owns the directory).
set -euo pipefail
DIST=$(cd "${1:?dist dir}" && pwd)
PLATFORM=${2:?platform, e.g. linux-amd64}
VERSION=${3:?version}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d "${RUNNER_TEMP:-/tmp}/tw-npm-cli.XXXXXX")
trap 'rm -rf "$WORK"' EXIT
say() { printf '\n== %s\n' "$*"; }

say "1. @tailworks/cli-$PLATFORM from the tarball; npm pack"
sh "$ROOT/scripts/npm-cli-packages.sh" "$VERSION" "$DIST" "$WORK/out" "$PLATFORM"
mkdir -p "$WORK/pkgs"
cp -R "$ROOT/js/cli/tailworks" "$WORK/shim"
node -e '
const fs = require("node:fs"), [f, v] = process.argv.slice(1);
const p = JSON.parse(fs.readFileSync(f, "utf8"));
p.version = v;
for (const d of Object.keys(p.optionalDependencies)) p.optionalDependencies[d] = v;
fs.writeFileSync(f, JSON.stringify(p, null, 2) + "\n");
' "$WORK/shim/package.json" "$VERSION"
(cd "$WORK/out/cli-$PLATFORM" && npm pack --silent --pack-destination "$WORK/pkgs")
(cd "$WORK/shim" && npm pack --silent --pack-destination "$WORK/pkgs")
ls -l "$WORK/pkgs"

say "2. npm install -g (offline: the other platforms' packages are skipped)"
G=$WORK/global
mkdir -p "$WORK/home"
export HOME=$WORK/home
npm install -g --prefix "$G" --offline --cache "$WORK/npm-cache" --no-audit --no-fund --update-notifier=false --loglevel=error \
	"$WORK/pkgs/tailworks-$VERSION.tgz" "$WORK/pkgs/tailworks-cli-$PLATFORM-$VERSION.tgz"
export PATH=$G/bin:$PATH
for b in tw tailworks; do
	got=$("$b" version | head -1)
	echo "$b: $got"
	case $got in *"$VERSION"*) ;; *) echo "$b version: want $VERSION"; exit 1 ;; esac
done

say "3. tw doctor"
tw doctor
tw doctor --json | node -e '
let s = ""; process.stdin.on("data", (d) => (s += d)).on("end", () => {
  const j = JSON.parse(s);
  const c = (j.checks || j).find((c) => c.name === "install");
  if (!c || !c.detail.startsWith("npm package @tailworks/cli-")) { console.error("install check:", JSON.stringify(c)); process.exit(1); }
  console.log("install: " + c.detail);
});'

say "4. tw init hello, tw dev --once"
tw init "$WORK/apps/hello" >/dev/null
out=$(tw dev "$WORK/apps/hello" --once --port 0 --json) || { echo "$out"; echo "tw dev --once failed"; exit 1; }
status=$(printf '%s' "$out" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const r=JSON.parse(s);console.log(r.smoke&&r.smoke.status)})')
echo "smoke status $status"
[ "$status" = 200 ] || { echo "$out"; exit 1; }

say "5. tw upgrade --plan prints the npm command; tw upgrade refuses (npm owns the install)"
out=$(tw upgrade "$VERSION" --plan 2>&1) || { echo "$out"; echo "tw upgrade --plan failed on an npm install"; exit 1; }
echo "$out"
case $out in *"npm install -g tailworks@$VERSION"*) ;; *) echo "want the npm command"; exit 1 ;; esac
if out=$(tw upgrade "$VERSION" --yes 2>&1); then echo "$out"; echo "tw upgrade ran from an npm install"; exit 1; fi
echo "$out"
case $out in *npm_install*) ;; *) echo "want npm_install"; exit 1 ;; esac

echo
echo "ci-npm-cli-check: $PLATFORM $VERSION ok"
