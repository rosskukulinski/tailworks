#!/usr/bin/env bash
# ci-suite.sh <command…>: run a test suite for release.yml and fail if it
# passed only by skipping. Many suites exit 0 with "SKIPPED" when their
# credentials, a VM or a tool are missing. That is right on a developer's
# machine and wrong for a release gate, where a skip would hide a runtime
# that was never tested.
#
# Fails when the command fails, when it prints a Makefile-level
# "<target>: SKIPPED" line, or when a top-level Go test skips
# ("--- SKIP: TestX"; subtests are indented and allowed).
# TW_CI_ALLOW_SKIP=<ERE>: top-level skips that are fine on this runner,
# matched against the "--- SKIP:" line.
set -uo pipefail
log=$(mktemp "${RUNNER_TEMP:-/tmp}/ci-suite.XXXXXX")
trap 'rm -f "$log"' EXIT
echo "ci-suite: $*"
"$@" 2>&1 | tee "$log"
rc=${PIPESTATUS[0]}
if [ "$rc" -ne 0 ]; then
	echo "ci-suite: FAILED (exit $rc): $*"
	exit "$rc"
fi
bad=$(grep -E '^[a-z0-9-]+: SKIPPED|^--- SKIP: ' "$log" || true)
if [ -n "${TW_CI_ALLOW_SKIP:-}" ] && [ -n "$bad" ]; then
	bad=$(printf '%s\n' "$bad" | grep -Ev "$TW_CI_ALLOW_SKIP" || true)
fi
if [ -n "$bad" ]; then
	echo "ci-suite: FAILED: the suite skipped instead of running (a release must test every runtime):"
	printf '  %s\n' "$bad"
	exit 1
fi
echo "ci-suite: ok: $*"
