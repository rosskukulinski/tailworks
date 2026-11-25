#!/bin/sh
# demo_test.sh: run scripts/demo.sh for real against a throwaway Headscale
# tailnet (scripts/demo-headscale.sh), run by `make demo-test`. It checks
# the refusals, a first run, a rerun over the running apps, an interrupted
# run (the grant must come back), --cleanup, and a run after --cleanup under
# the same names. Needs docker; skips without.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
	echo "demo-test: SKIPPED (no docker daemon)"
	exit 0
fi

D=${DEMO_HS_DIR:-/tmp/tw-demo-hs}
export DEMO_HS_DIR="$D"
trap 'scripts/demo-headscale.sh down >/dev/null 2>&1 || true' EXIT
scripts/demo-headscale.sh up >/dev/null
# shellcheck disable=SC1091
. "$D/env"
log=$D/demo-test.log

fail() {
	echo "demo-test: FAIL: $*" >&2
	[ ! -f "$log" ] || tail -n 30 "$log" >&2
	exit 1
}
GT=$D/demo-grant-test
grant_tools() {
	"$GT" --control headscale --control-url "$DEMO_CONTROL_URL" --api-key-file "$DEMO_API_KEY_FILE" \
		--tailnet "$DEMO_TAILNET" --agent-workload-tag tag:tw-wl-demo-agent --agent-tag tag:tw-demo-agent-support-agent \
		--mcp-tag tag:tw-wl-demo show
}

# Refusals: no tailnet named, the wrong tailnet, no --yes without a terminal.
if (unset DEMO_TAILNET; scripts/demo.sh --yes) >"$log" 2>&1; then fail "ran without a tailnet"; fi
if scripts/demo.sh --tailnet other.example --yes >"$log" 2>&1; then fail "ran against a tailnet it was not given"; fi
grep -q "refusing to touch" "$log" || fail "the wrong-tailnet refusal does not say why"
if scripts/demo.sh </dev/null >"$log" 2>&1; then fail "wrote policy without --yes and without a terminal"; fi
echo "demo-test: ok refusals"

scripts/demo.sh --yes --no-pause >"$log" 2>&1 || fail "first run"
for want in '"ok":true' cap_missing '"code":"killed"' 'chain verified' 'demo: done'; do
	grep -q "$want" "$log" || fail "first run: no $want in the output"
done
cp "$DEMO_DIR/.demo-grant" "$GT" # --cleanup deletes the demo's copy
echo "demo-test: ok first run (allowed, denied, restored, killed, audit)"

scripts/demo.sh --yes --no-pause >"$log" 2>&1 || fail "rerun over the running apps"
echo "demo-test: ok rerun"

# Interrupted while the grant is narrowed: the trap restores it.
DEMO_PAUSE=5 scripts/demo.sh --yes >"$log" 2>&1 &
pid=$!
i=0
until grep -q 'not granted to' "$log"; do
	i=$((i + 1))
	[ $i -lt 300 ] || fail "the interrupted run never reached the denied call"
	sleep 1
done
case $(grant_tools) in *'"lookup_order"]'*) ;; *) fail "the grant is not narrowed at the denied step" ;; esac
kill -TERM "$pid"
wait "$pid" && fail "the interrupted run exited 0"
case $(grant_tools) in *'"lookup_order","refund"]'*) ;; *) fail "the grant was not restored after the interrupt: $(grant_tools)" ;; esac
echo "demo-test: ok interrupt restores the grant"

scripts/demo.sh --cleanup --yes >"$log" 2>&1 || fail "--cleanup"
if grant_tools >/dev/null 2>&1; then fail "--cleanup left the tool grant in the policy"; fi
[ ! -d "$DEMO_DIR/demo" ] || fail "--cleanup left $DEMO_DIR/demo"
echo "demo-test: ok cleanup"

# The same names deploy again after --cleanup (#364: undeploy left the
# workload node killed until a purge).
scripts/demo.sh --yes --no-pause >"$log" 2>&1 || fail "a run after --cleanup (same names)"
for want in '"ok":true' cap_missing '"code":"killed"' 'demo: done'; do
	grep -q "$want" "$log" || fail "run after --cleanup: no $want in the output"
done
scripts/demo.sh --cleanup --yes >"$log" 2>&1 || fail "second --cleanup"
echo "demo-test: ok run after cleanup"
echo "demo-test: PASS"
