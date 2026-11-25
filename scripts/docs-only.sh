#!/bin/sh
# docs-only.sh [base]: exit 0 if the diff base...HEAD (plus uncommitted
# work) changes only Markdown prose, 1 otherwise. ci.yml uses it to run the
# docs gates (make lint-docs test-docs) instead of the full lint, test and
# suites; AGENTS.md lets a docs-only branch do the same locally.
#
# Docs-only means every changed path ends in .md and none is a test input:
# nothing under testdata/ or e2e/. Many .md files are still code-adjacent
# (docs/ and README.md are embedded in tw, agent/skills/ ships as skills,
# docs/cli.md and docs/contracts/ are generated), which is why lint-docs and
# test-docs exist rather than skipping CI outright.
#
# base defaults to the merge-base with origin/master (else master). No base
# or an empty diff counts as not docs-only, so the full gates run.
set -eu
cd "$(git rev-parse --show-toplevel)"

BASE=${1:-}
if [ -z "$BASE" ]; then
	BASE=$(git merge-base HEAD origin/master 2>/dev/null || git merge-base HEAD master 2>/dev/null || true)
fi
[ -n "$BASE" ] || { echo "docs-only: no base to diff against" >&2; exit 1; }

changed=$({
	git diff --name-only "$BASE" HEAD
	git diff --name-only HEAD
	git ls-files --others --exclude-standard
} | sort -u)
[ -n "$changed" ] || { echo "docs-only: no changes against $BASE" >&2; exit 1; }

other=$(printf '%s\n' "$changed" | grep -vE '\.md$' || true)
fixtures=$(printf '%s\n' "$changed" | grep -E '(^|/)testdata/|^e2e/' || true)
if [ -n "$other$fixtures" ]; then
	echo "docs-only: no; not prose:" >&2
	printf '%s\n' $other $fixtures | sort -u | sed 's/^/  /' >&2
	exit 1
fi
echo "docs-only: yes ($(printf '%s\n' "$changed" | wc -l | tr -d ' ') Markdown files)" >&2
