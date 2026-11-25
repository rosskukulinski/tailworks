#!/bin/sh
# upgrade-from.sh: the release `make e2e-upgrade` upgrades from (its
# TW_UPGRADE_FROM default). That is the newest *published* GitHub release,
# the one users actually run, not the newest git tag: v0.1.0 was tagged
# but never published, and it predates the rename to tailworks (its hosts
# send Twd-Host-Token, which tw-mgmt no longer reads), so nobody can
# upgrade from it. With no published release, or none reachable, it prints
# "self" (this tree, stamped an older version: the machinery check).
#
# When the newest published release is this tree's own version (release.yml
# re-run for a tag that is already out), it prints "self" too: the run that
# published it already passed the gate. TW_REPO overrides the repository,
# as for get.sh.
set -eu
REPO=${TW_REPO:-rosskukulinski/tailworks}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
tag=$(curl -fsSL --max-time 15 "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null |
	sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1) || tag=
if [ -n "$tag" ] && [ "$tag" = "v$(cat "$ROOT/VERSION")" ]; then
	echo "upgrade-from: $tag, the newest published release, is this tree's version: upgrading from this tree (self)" >&2
	echo self
	exit 0
fi
if [ -z "$tag" ]; then
	echo "upgrade-from: no published release in $REPO (or GitHub unreachable): upgrading from this tree (self)" >&2
	echo self
	exit 0
fi
# The test builds it from git: fetch the tag if this clone lacks it.
git -C "$ROOT" rev-parse -q --verify "refs/tags/$tag" >/dev/null ||
	git -C "$ROOT" fetch -q origin "refs/tags/$tag:refs/tags/$tag" >&2 || true
echo "upgrade-from: upgrading from $tag, the newest published release" >&2
echo "$tag"
