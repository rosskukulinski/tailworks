#!/bin/sh
# release-notes.sh [--finalize] <prev-tag|-> <tag>: the release notes for
# <tag>, assembled from the pending fragments (changes/*.md; see
# changes/README.md) in sections: Breaking, Security, Upgrade actions,
# Features, Fixes. Prints markdown on stdout.
#
#   scripts/release-notes.sh v0.1.0 v0.2.0              preview
#   scripts/release-notes.sh --finalize v0.1.0 v0.2.0   also: move the fragments to
#        changes/released/v0.2.0/, write internal/catalog/releases/v0.2.0.json
#        and re-render CHANGELOG.md from the catalog
#
# Once <tag> is finalized (changes/released/<tag>/ exists), the preview
# prints that release's fragments instead (release.yml uses this for the
# draft's body).
#
# prev-tag ("-" = none) bounds the contract migrations listed in the
# catalog entry: mgmt/migrations/*.sql files added since prev-tag that have
# a "-- twd:contract" line. --finalize leaves the changes for you to
# commit (docs/RELEASING.md).
set -eu
FINALIZE=0
if [ "${1:-}" = --finalize ]; then FINALIZE=1; shift; fi
[ $# -eq 2 ] || { echo "usage: release-notes.sh [--finalize] <prev-tag|-> <tag>" >&2; exit 2; }
PREV=$1
TAG=$2
case $TAG in v[0-9]*.[0-9]*.[0-9]*) ;; *) echo "release-notes: tag $TAG is not vMAJOR.MINOR.PATCH" >&2; exit 2 ;; esac
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
export GOTOOLCHAIN=local

frags=$(ls changes/*.md 2>/dev/null | grep -v '^changes/README\.md$' || true)
# Already finalized (the tag's own commit, as release.yml sees it): the
# notes are the fragments released with it.
if [ $FINALIZE = 0 ] && [ -d "changes/released/$TAG" ]; then
	frags=$(ls "changes/released/$TAG"/*.md 2>/dev/null || true)
fi
# shellcheck disable=SC2086
go run ./internal/catalog/changes notes -title "twd $TAG" $frags

[ $FINALIZE = 1 ] || exit 0

out=internal/catalog/releases/$TAG.json
[ ! -e "$out" ] || { echo "release-notes: $out exists (already finalized?)" >&2; exit 1; }
[ ! -e "changes/released/$TAG" ] || { echo "release-notes: changes/released/$TAG exists" >&2; exit 1; }

WORKERD=$(node -p 'require("./package-lock.json").packages["node_modules/workerd"].version')
SCHEMA=""
CONTRACT=""
if [ -d mgmt/migrations ]; then
	SCHEMA=$(ls mgmt/migrations/*.sql 2>/dev/null | sed 's|.*/||; s|\..*||' | LC_ALL=C sort -u | tail -n 1)
	for f in mgmt/migrations/*.sql; do
		[ -f "$f" ] || continue
		grep -q '^-- twd:contract' "$f" || continue
		if [ "$PREV" != - ] && git cat-file -e "$PREV:$f" 2>/dev/null; then continue; fi
		id=$(basename "$f" | sed 's|\..*||')
		case ",$CONTRACT," in *",$id,"*) ;; *) CONTRACT=${CONTRACT:+$CONTRACT,}$id ;; esac
	done
fi
PROTOCOLS=$(go run ./internal/version/protocols)
# shellcheck disable=SC2086
go run ./internal/catalog/changes entry -version "$TAG" -date "$(date -u +%Y-%m-%d)" -workerd "$WORKERD" \
	-schema "$SCHEMA" -contract "$CONTRACT" -protocols "$PROTOCOLS" $frags >"$out.tmp"
mv "$out.tmp" "$out"
# CHANGELOG.md is rendered from the catalog, now including this entry
# (TestChangelogUpToDate checks it).
go run ./internal/catalog/changes changelog >CHANGELOG.md.tmp
mv CHANGELOG.md.tmp CHANGELOG.md
mkdir -p "changes/released/$TAG"
for f in $frags; do
	if git ls-files --error-unmatch "$f" >/dev/null 2>&1; then git mv "$f" "changes/released/$TAG/"; else mv "$f" "changes/released/$TAG/"; fi
done
echo "release-notes: wrote $out and moved $(echo $frags | wc -w | tr -d ' ') fragment(s) to changes/released/$TAG/ (commit them)" >&2
