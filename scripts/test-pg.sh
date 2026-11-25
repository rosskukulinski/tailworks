#!/bin/sh
# Runs the mgmt Store tests against Postgres 17.
#
# By default it starts a throwaway postgres:17-alpine container (OrbStack docker).
# With TW_TEST_POSTGRES_DSN already set (CI's Postgres service container,
# .github/workflows/nightly.yml), it uses that database and starts nothing.
# The tests drop and recreate the public schema, so point it at a scratch database.
#
# The container is named tw-test-pg-<pid> and, unless a port is given, docker
# publishes it on a free port, so runs in parallel worktrees don't collide.
# TW_TEST_PG_NAME and the port argument (make test-pg PG_PORT=55433) pin them.
set -u
ctx="${1:-orbstack}"
port="${2:-}"
name="${TW_TEST_PG_NAME:-tw-test-pg-$$}"
log="${TW_TEST_PG_LOG:-/tmp/$name.log}"
if [ -z "${TW_TEST_POSTGRES_DSN:-}" ]; then
	docker --context "$ctx" rm -f "$name" >/dev/null 2>&1 || true
	docker --context "$ctx" run -d --rm --name "$name" -e POSTGRES_PASSWORD=twtest \
		-p "127.0.0.1:$port:5432" postgres:17-alpine >/dev/null || exit 1
	trap 'docker --context "$ctx" rm -f "$name" >/dev/null 2>&1' EXIT
	if [ -z "$port" ]; then
		port=$(docker --context "$ctx" port "$name" 5432/tcp | sed -n 's/^127\.0\.0\.1://p' | head -n 1)
		[ -n "$port" ] || { echo "no host port published for $name"; exit 1; }
	fi
	i=0
	until docker --context "$ctx" exec "$name" pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1; do
		i=$((i + 1))
		[ "$i" -gt 30 ] && { echo "postgres did not start"; exit 1; }
		sleep 1
	done
	TW_TEST_POSTGRES_DSN="postgres://postgres:twtest@127.0.0.1:$port/postgres?sslmode=disable"
fi
export TW_TEST_POSTGRES_DSN
# Store tests on both backends, then the API tests with the Postgres store.
# -parallel 1: the tests share one database (each resets the public schema),
# so the ones marked t.Parallel() for the SQLite run must run one at a time here.
GOTOOLCHAIN=local go test -race -count=1 -parallel 1 -run 'Lease|Audit|Store|Migrat' -v ./mgmt/ >"$log" 2>&1
st=$?
TW_TEST_API_POSTGRES=1 GOTOOLCHAIN=local go test -race -count=1 -parallel 1 -run 'API|Kill|WIF|Tailnet|V1|TestWarmKey' -v ./mgmt/ >>"$log" 2>&1
st2=$?
[ "$st" -eq 0 ] && st=$st2
grep -E '^(---|    ---|ok|FAIL|panic)' "$log"
echo "full log: $log"
exit $st
