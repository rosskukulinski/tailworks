#!/bin/sh
# release-check.sh <tag>: is this checkout ready to be tagged and released
# as <tag>? Run it on the commit you are about to tag (docs/RELEASING.md,
# "Cutting a release"); release.yml's gates run it on the tag before
# anything else, so a release that would ship wrong notes, an unsigned
# SHA256SUMS or mismatched npm versions fails in a minute, not after the
# guest builds.
#
# Checks:
#   - <tag> is vMAJOR.MINOR.PATCH and matches VERSION;
#   - the notes are finalized for <tag> (changes/released/<tag>/ and
#     internal/catalog/releases/<tag>.json exist) and no fragment is still
#     pending in changes/ (it would be left out of <tag>'s notes and
#     catalog entry, and show up in the next release's instead);
#   - a release signing key is pinned (internal/releasesig/allowed_signers),
#     so get.sh, install.sh and tw-hostd can check the signature;
#   - every npm package and template is at the version
#     (scripts/npm-versions.mjs).
#
# It prints every problem, then exits 1 if there was one.
set -eu
[ $# -eq 1 ] || { echo "usage: release-check.sh <tag>" >&2; exit 2; }
TAG=$1
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

fail=0
bad() { printf 'release-check: %s\n' "$*" >&2; fail=1; }

case $TAG in
v[0-9]*.[0-9]*.[0-9]*) ;;
*) echo "release-check: tag $TAG is not vMAJOR.MINOR.PATCH" >&2; exit 2 ;;
esac
V=${TAG#v}
[ "$V" = "$(cat VERSION)" ] || bad "tag $TAG does not match VERSION $(cat VERSION) (bump it: docs/RELEASING.md, step 3)"

[ -d "changes/released/$TAG" ] && [ -f "internal/catalog/releases/$TAG.json" ] ||
	bad "the notes for $TAG are not finalized: run scripts/release-notes.sh --finalize <prev-tag> $TAG (docs/RELEASING.md, step 2)"
pending=$(ls changes/*.md 2>/dev/null | grep -v '^changes/README\.md$' || true)
if [ -n "$pending" ]; then
	bad "$(echo "$pending" | wc -l | tr -d ' ') fragment(s) still pending in changes/, left out of $TAG's notes:
$(echo "$pending" | sed 's/^/  /')
  finalize them into $TAG (docs/RELEASING.md, step 2; \"Re-finalizing an unpublished release\" if $TAG already has notes)"
fi

grep -q '^tw-release ' internal/releasesig/allowed_signers ||
	bad "no release signing key is pinned in internal/releasesig/allowed_signers (docs/RELEASING.md, \"The key ceremony\")"

node scripts/npm-versions.mjs "$V" >/dev/null || bad "npm package versions don't match $V (node scripts/npm-versions.mjs --set $V)"

[ $fail = 0 ] || exit 1
echo "release-check: $TAG is ready to tag"
