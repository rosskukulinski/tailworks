#!/bin/sh
# check-changes.sh [base]: release-note fragments are present and valid
# (ADR-041 §9; changes/README.md).
#
#   1. Every changes/*.md and changes/released/*/*.md has valid front matter.
#   2. If the diff base...HEAD (plus uncommitted changes) touches an
#      upgrade-relevant path, a fragment added or changed in that diff names
#      the matching upgrade kind:
#        mgmt/migrations/            schema-expand or schema-contract
#                                    (schema-contract if an added migration has a "-- tw:contract" line)
#        api/openapi.yaml            protocol
#        schema/cli.schema.json      cli-json
#        package-lock.json workerd   workerd
#        guest/inputs.sh             workerd or protocol
#        **/testdata/formats/        format
#        hostd/vm/helperapi/api.go "Version =", internal/version protocol consts   protocol
#
# base defaults to the merge-base with origin/master (else master). On
# master itself (no diff) only step 1 runs.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
export GOTOOLCHAIN=local

die() { printf 'check-changes: %s\n' "$*" >&2; exit 1; }

# 1. Front matter.
frags=$(ls changes/*.md changes/released/*/*.md 2>/dev/null | grep -v '^changes/README\.md$' || true)
if [ -n "$frags" ]; then
	# shellcheck disable=SC2086
	go run ./internal/catalog/changes validate $frags || die "fix the fragments above (format: changes/README.md)"
fi

# 2. Required kinds for this branch's diff.
BASE=${1:-}
if [ -z "$BASE" ]; then
	BASE=$(git merge-base HEAD origin/master 2>/dev/null || git merge-base HEAD master 2>/dev/null || true)
fi
[ -n "$BASE" ] || { echo "check-changes: no base to diff against; fragments valid"; exit 0; }

# Changed paths: committed since base, plus staged/unstaged/untracked work.
changed=$( {
	git diff --name-only "$BASE"...HEAD
	git diff --name-only HEAD
	git ls-files --others --exclude-standard
} | LC_ALL=C sort -u)
[ -n "$changed" ] || { echo "check-changes: no changes since $(git rev-parse --short "$BASE"); fragments valid"; exit 0; }

need=""          # space-separated requirement groups; a|b means either
add() { need="$need $1"; }

if printf '%s\n' "$changed" | grep -q '^mgmt/migrations/'; then
	contract=0
	for f in $(printf '%s\n' "$changed" | grep '^mgmt/migrations/.*\.sql$' || true); do
		# Only migrations new in this diff decide contract vs expand.
		if ! git cat-file -e "$BASE:$f" 2>/dev/null && [ -f "$f" ] && grep -q '^-- tw:contract' "$f"; then contract=1; fi
	done
	if [ $contract = 1 ]; then add schema-contract; else add 'schema-expand|schema-contract'; fi
fi
printf '%s\n' "$changed" | grep -qx 'api/openapi.yaml' && add protocol
printf '%s\n' "$changed" | grep -qx 'schema/cli.schema.json' && add cli-json
printf '%s\n' "$changed" | grep -qx 'guest/inputs.sh' && add 'workerd|protocol'
printf '%s\n' "$changed" | grep -q 'testdata/formats/' && add format

if printf '%s\n' "$changed" | grep -qx 'package-lock.json'; then
	wd() { node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{console.log(JSON.parse(s).packages["node_modules/workerd"].version)}catch(e){console.log("")}})'; }
	old=$(git show "$BASE:package-lock.json" 2>/dev/null | wd || true)
	new=$(wd <package-lock.json)
	[ "$old" = "$new" ] || add workerd
fi

# A changed line in a protocol constant.
proto_changed() { # file pattern
	{ git diff "$BASE"...HEAD -- "$1"; git diff HEAD -- "$1"; } | grep -E '^[+-][^+-]' | grep -Eq "$2"
}
proto_changed hostd/vm/helperapi/api.go '^[+-][[:space:]]*(const[[:space:]]+)?Version[[:space:]]*=' && add protocol
proto_changed internal/version/version.go '^[+-][[:space:]]*(PushProtocol|GuestProtocol|VZProtocol|RESTMajor|CLIJSONMajor)[[:space:]]*=' && add protocol

need=$(printf '%s\n' $need | LC_ALL=C sort -u | tr '\n' ' ')
[ -n "${need% }" ] || { echo "check-changes: ok (no upgrade-relevant paths changed)"; exit 0; }

# Kinds named by fragments this diff adds or changes.
# A release commit moves the fragments to changes/released/<tag>/: they count too.
mine=$(printf '%s\n' "$changed" | grep -E '^changes/([^/]+|released/[^/]+/[^/]+)\.md$' | grep -v '^changes/README\.md$' || true)
have=""
for f in $mine; do
	[ -f "$f" ] && have="$have $(go run ./internal/catalog/changes kinds "$f" | tr '\n' ' ')"
done

missing=""
for group in $need; do
	ok=0
	for k in $(printf '%s' "$group" | tr '|' ' '); do
		case " $have " in *" $k "*) ok=1 ;; esac
	done
	[ $ok = 1 ] || missing="$missing $(printf '%s' "$group" | sed 's/|/ or /g')"
done
if [ -n "$missing" ]; then
	branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "<branch>")
	if [ -z "$mine" ]; then
		where="add changes/$branch.md"
	else
		where="add them to the upgrade list of $(echo $mine | sed 's/ /, /g')"
	fi
	die "this branch changes upgrade-relevant paths but no fragment names:$missing
  $where (format: changes/README.md)"
fi
echo "check-changes: ok (needs:$(printf ' %s' $need | sed 's/|/ or /g'); found in $(echo $mine | sed 's/ /, /g'))"
