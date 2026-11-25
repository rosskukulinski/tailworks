#!/bin/sh
# Runs TestTCPEgressPostgres: postgres.js from a Worker and a facet object,
# over connect() through hostd and netd, to Postgres 17 on the (hermetic)
# tailnet (ADR-052).
#
# By default it starts a throwaway postgres:17-alpine container (docker
# context $1, default orbstack) on a free port. With TW_TEST_POSTGRES_DSN
# already set it uses that server (user postgres, password twtest).
set -u
ctx="${1:-orbstack}"
name="tw-e2e-pg-$$"
if [ -z "${TW_TEST_POSTGRES_DSN:-}" ]; then
	docker --context "$ctx" run -d --rm --name "$name" -e POSTGRES_PASSWORD=twtest \
		-p "127.0.0.1::5432" postgres:17-alpine >/dev/null || exit 1
	trap 'docker --context "$ctx" rm -f "$name" >/dev/null 2>&1' EXIT
	port=$(docker --context "$ctx" port "$name" 5432/tcp | sed -n 's/^127\.0\.0\.1://p' | head -n 1)
	[ -n "$port" ] || { echo "no host port published for $name"; exit 1; }
	i=0
	until docker --context "$ctx" exec "$name" pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1; do
		i=$((i + 1))
		[ "$i" -gt 30 ] && { echo "postgres did not start"; exit 1; }
		sleep 1
	done
	TW_TEST_POSTGRES_DSN="postgres://postgres:twtest@127.0.0.1:$port/postgres?sslmode=disable"
fi
export TW_TEST_POSTGRES_DSN
GOTOOLCHAIN=local go test -count=1 -run 'TestTCPEgressPostgres' -v ./hostd/
