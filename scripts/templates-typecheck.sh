#!/bin/sh
# templates-typecheck.sh [template...]: issue #41's acceptance test without
# the npm registry. For each create-tailworks template (default: all):
#
#   tw init <app> --template <t> && cd <app> && npm install && npx tsc --noEmit
#
# with @tailworks/* installed from `npm pack` tarballs of this checkout instead of
# npmjs.com (package.json dependencies and overrides point at the tarballs;
# every other package comes from the registry, so this needs the network).
# Then `tw types --check`: the template's worker-configuration.d.ts still
# matches its config after `tw init` renamed the app.
#
# KEEP=1 keeps the work dir (printed) for a look at the apps.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
export GOTOOLCHAIN=local

WORK=$(mktemp -d "${TMPDIR:-/tmp}/tw-typecheck.XXXXXX")
if [ "${KEEP:-0}" = 1 ]; then echo "templates-typecheck: work dir $WORK"; else trap 'rm -rf "$WORK"' EXIT; fi

TEMPLATES="$*"
[ -n "$TEMPLATES" ] || TEMPLATES=$(ls js/packages/create-tailworks/templates)

go build -o "$WORK/tw" ./cmd/tw
mkdir -p "$WORK/pkgs" "$WORK/apps"
npm pack --silent -w @tailworks/tailnet -w @tailworks/mcp -w @tailworks/node -w create-tailworks --pack-destination "$WORK/pkgs" >/dev/null
export TW_CREATE_TAILWORKS="$ROOT/js/packages/create-tailworks"

fail=0
for t in $TEMPLATES; do
	app="$WORK/apps/$t-app"
	if [ -f "js/packages/create-tailworks/templates/$t/src/entry.py" ]; then
		echo "== $t (a Python Worker: no TypeScript to check)"
		continue
	fi
	echo "== $t"
	"$WORK/tw" init "$app" --template "$t" >/dev/null
	node - "$app/package.json" "$WORK/pkgs" <<'EOF'
const fs = require("fs"), path = require("path");
const [file, pkgs] = process.argv.slice(2);
const p = JSON.parse(fs.readFileSync(file, "utf8"));
p.overrides = {};
for (const tgz of fs.readdirSync(pkgs)) {
  const name = JSON.parse(require("child_process").execFileSync("tar", ["-xzOf", path.join(pkgs, tgz), "package/package.json"])).name;
  const spec = "file:" + path.join(pkgs, tgz);
  for (const sec of ["dependencies", "devDependencies"]) if (p[sec]?.[name]) p[sec][name] = spec;
  p.overrides[name] = spec;
}
fs.writeFileSync(file, JSON.stringify(p, null, 2) + "\n");
EOF
	if (cd "$app" && npm install --no-audit --no-fund --loglevel=error >/dev/null && npx tsc --noEmit && "$WORK/tw" types --check >/dev/null); then
		echo "   ok: npm install, tsc --noEmit, tw types --check"
	else
		echo "   FAILED: $t"
		fail=1
	fi
done
exit $fail
