#!/bin/sh
# v8-check.sh (make v8-check): is the pinned workerd's V8 behind the V8 in
# Chrome's stable channel? (issue #338; docs/RELEASING.md "Upstream
# advisories".) V8 security fixes reach workerd as V8 rolls inside ordinary
# releases, and Chrome's stable release notes are the public signal that
# names the CVEs, so the pin is behind when its V8 milestone is lower than
# Chrome stable's (V8 15.8 is Chrome 158).
#
#   make v8-check                   print both versions; exit 0 (at or past
#                                   Chrome stable), 10 (behind) or 1 (error)
#   V8_CHECK_ISSUE=1 make v8-check  what nightly.yml runs: also open or update
#                                   the one `security` issue titled
#                                   "workerd pin is behind Chrome stable's V8"
#                                   when behind, close it when caught up;
#                                   exit 0 either way (the issue is the
#                                   signal). Needs gh with GH_TOKEN/GH_REPO;
#                                   RUN_URL goes into the issue.
#
# TW_WORKERD names another workerd binary (default node_modules/.bin/workerd,
# which must be the pinned release). V8_CHECK_CHROME names a file with
# chromiumdash's JSON instead of fetching it (tests, offline runs).
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
export GOTOOLCHAIN=local

WORKERD=${TW_WORKERD:-node_modules/.bin/workerd}
[ -e "$WORKERD" ] || { echo "v8-check: $WORKERD not found: run make deps (npm ci), or set TW_WORKERD" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
go build -o "$tmp/v8check" ./internal/v8check/cmd
set +e
# shellcheck disable=SC2086
"$tmp/v8check" -workerd "$WORKERD" -issue-body "$tmp/body.md" ${V8_CHECK_CHROME:+-chrome "$V8_CHECK_CHROME"} >"$tmp/out"
rc=$?
set -e
cat "$tmp/out"
[ "$rc" = 0 ] || [ "$rc" = 10 ] || exit "$rc"
[ "${V8_CHECK_ISSUE:-}" = 1 ] || exit "$rc"

title="workerd pin is behind Chrome stable's V8"
n=$(gh issue list --state open --label security --search "\"$title\" in:title" --json number --jq '.[0].number')
if [ "$rc" = 0 ]; then
	[ -z "$n" ] || gh issue close "$n" --comment "The pin caught up on $(date -u +%Y-%m-%d): $(cat "$tmp/out") ${RUN_URL:-}"
	exit 0
fi
echo "::warning::$title: $(cat "$tmp/out")"
if [ -n "$n" ]; then
	# One issue per gap: refresh its body (a new Chrome stable, a new pin that
	# is still behind) rather than comment every night.
	gh issue edit "$n" --body-file "$tmp/body.md" >/dev/null
	echo "v8-check: updated issue #$n"
else
	gh issue create --title "$title" --label security --body-file "$tmp/body.md"
fi
exit 0
