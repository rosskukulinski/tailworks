#!/bin/sh
# ci-ts-creds.sh: write the test tailnets' API keys ($K1, $K2: release.yml
# passes the release environment's secrets) as the files the real-tailnet
# suites read ($RUNNER_TEMP/ts-creds/tailnet{1,2}_api.env, mode 0600), and
# set TS_CREDS for the job's later steps. Fails if a secret or TAILNET1 is
# missing: a release never skips the real-tailnet suites.
set -eu
[ -n "${K1:-}" ] && [ -n "${K2:-}" ] && [ -n "${TAILNET1:-}" ] || {
	echo "::error::the release environment needs TW_E2E_TAILNET1, TW_E2E_TAILNET1_API_KEY and TW_E2E_TAILNET2_API_KEY"
	exit 1
}
d=$RUNNER_TEMP/ts-creds
mkdir -p "$d"
(umask 077 && printf 'TS_API_KEY=%s\n' "$K1" >"$d/tailnet1_api.env" && printf 'TS_API_KEY=%s\n' "$K2" >"$d/tailnet2_api.env")
echo "TS_CREDS=$d" >>"$GITHUB_ENV"
