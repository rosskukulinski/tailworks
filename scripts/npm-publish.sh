#!/bin/sh
# npm-publish.sh <version> [--dry-run] [--cli-dist <dir>]: publish tailworks'
# npm packages (@tailworks/tailnet, @tailworks/node, @tailworks/mcp,
# create-tailworks, and the tw CLI: @tailworks/cli-<os>-<arch> and
# tailworks) at <version>, from a checkout of the release tag (ADR-041 §9;
# docs/RELEASING.md "Publish npm").
#
#   - The CLI (issue #240) comes from the release tarballs in --cli-dist
#     (tailworks-<version>-<target>.tar.gz + SHA256SUMS + SHA256SUMS.sig, as
#     on the GitHub release): scripts/npm-cli-packages.sh checks the
#     signature and makes one platform package per target, published before
#     the tailworks package that lists them. A real publish needs
#     --cli-dist; a --dry-run without it checks only the tailworks package.
#
#   - Checks every package and template is at <version> first
#     (scripts/npm-versions.mjs), so a release never publishes a mix.
#   - Builds each package (its prepack runs tsc for dist/ and the .d.ts).
#   - Publishes in dependency order (@tailworks/mcp depends on @tailworks/tailnet), and
#     skips a package already on the registry at <version>, so a re-run after
#     a partial failure finishes the job.
#   - A prerelease (0.3.0-rc.1) goes to the "next" dist-tag, not "latest".
#
# Auth is npm's own. In CI it is trusted publishing: npm (>= 11.5.1)
# exchanges the GitHub Actions OIDC token for a publish credential and
# attaches provenance itself (.github/workflows/npm-publish.yml). By hand it
# is your `npm login`. --dry-run packs and validates without publishing
# (no auth).
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

V=${1:-}
[ -n "$V" ] || { echo "usage: scripts/npm-publish.sh <version> [--dry-run]" >&2; exit 2; }
V=${V#v}
shift
DRY=""
CLI_DIST=""
while [ $# -gt 0 ]; do
	case $1 in
	--dry-run) DRY="--dry-run" ;;
	--cli-dist) CLI_DIST=$2; shift ;;
	*) echo "npm-publish: unknown argument $1" >&2; exit 2 ;;
	esac
	shift
done
[ -n "$CLI_DIST" ] || [ -n "$DRY" ] || { echo "npm-publish: a real publish needs --cli-dist <dir with the release tarballs> (the tw CLI's packages)" >&2; exit 2; }

node scripts/npm-versions.mjs "$V"

TAG=latest
case "$V" in *-*) TAG=next ;; esac

# Workspace directory → package name, in publish order.
for ws in tailnet node mcp create-tailworks; do
	name=$(node -p "require('./js/packages/$ws/package.json').name")
	if [ -z "$DRY" ] && [ "$(npm view "$name@$V" version 2>/dev/null || true)" = "$V" ]; then
		echo "npm-publish: $name@$V is already published; skipping"
		continue
	fi
	echo "npm-publish: $name@$V (dist-tag $TAG)${DRY:+ (dry run)}"
	npm publish -w "js/packages/$ws" --access public --tag "$TAG" $DRY
done

# The tw CLI: the platform packages, then the tailworks package whose
# optionalDependencies name them (it must never be on npm without them).
CLI_TARGETS="darwin-arm64 linux-arm64 linux-amd64"
published() { [ -z "$DRY" ] && [ "$(npm view "$1@$V" version 2>/dev/null || true)" = "$V" ]; }
if [ -n "$CLI_DIST" ]; then
	CLI_OUT=$(mktemp -d "${TMPDIR:-/tmp}/tw-npm-cli.XXXXXX")
	trap 'rm -rf "$CLI_OUT"' EXIT
	sh scripts/npm-cli-packages.sh "$V" "$CLI_DIST" "$CLI_OUT" $CLI_TARGETS
	for t in $CLI_TARGETS; do
		name=@tailworks/cli-$t
		if published "$name"; then
			echo "npm-publish: $name@$V is already published; skipping"
			continue
		fi
		echo "npm-publish: $name@$V (dist-tag $TAG)${DRY:+ (dry run)}"
		# --loglevel=warn: not a notice line per file (~13k of them).
		npm publish "$CLI_OUT/cli-$t" --access public --tag "$TAG" --loglevel=warn $DRY
	done
else
	echo "npm-publish: no --cli-dist: not packing @tailworks/cli-* (dry run; release.yml's tarball jobs check them per platform)"
fi
if published tailworks; then
	echo "npm-publish: tailworks@$V is already published; skipping"
else
	echo "npm-publish: tailworks@$V (dist-tag $TAG)${DRY:+ (dry run)}"
	npm publish ./js/cli/tailworks --access public --tag "$TAG" $DRY
fi
