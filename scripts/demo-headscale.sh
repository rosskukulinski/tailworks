#!/bin/sh
# demo-headscale.sh: a throwaway tailnet for scripts/demo.sh, so the demo can
# be tested (and recorded) without touching a real tailnet or the tailworks
# install in ~/.tailworks.
#
#   scripts/demo-headscale.sh up      start it, print how to run the demo
#   scripts/demo-headscale.sh down    stop and delete all of it
#
# up starts, under $DEMO_HS_DIR (default /tmp/tw-demo-hs):
#   - Headscale ($HEADSCALE_IMAGE, the release `make e2e-headscale` pins) in
#     Docker on 127.0.0.1:$DEMO_HS_PORT (default 18473), base domain demo.tw.test;
#   - a Tailscale client container named `laptop`, joined as user `demo`: the
#     "device on the tailnet" the demo calls from (docker exec laptop curl);
#   - tailworks (`tw up --control=headscale --ephemeral --service=false`) with
#     its state in $DEMO_HS_DIR/state.
# It writes $DEMO_HS_DIR/env, the environment scripts/demo.sh needs.
#
# Needs docker, curl and a tw that can run here: from a checkout, `make deps
# build` first (bin/tw is used when it exists). Nodes reach each other
# through Tailscale's public DERP relays, as in `make e2e-headscale`.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
D=${DEMO_HS_DIR:-/tmp/tw-demo-hs}
PORT=${DEMO_HS_PORT:-18473}
IMAGE=${HEADSCALE_IMAGE:-headscale/headscale:v0.29.7}
CLIENT_IMAGE=${DEMO_HS_CLIENT_IMAGE:-tailscale/tailscale:stable}
DOMAIN=demo.tw.test
HS=tw-demo-hs
CLIENT=laptop

die() {
	printf 'demo-headscale: %s\n' "$*" >&2
	exit 1
}

# This script's tw never uses the default state dir.
export TW_STATE_DIR="$D/state"
if [ -x "$ROOT/bin/tw" ]; then
	PATH=$ROOT/bin:$PATH
	export TW_WORKERD="${TW_WORKERD:-$ROOT/node_modules/.bin/workerd}"
	export TW_CREATE_TAILWORKS="${TW_CREATE_TAILWORKS:-$ROOT/js/packages/create-tailworks}"
fi

down() {
	if [ -d "$D/state" ] && command -v tw >/dev/null 2>&1; then
		tw down --purge >/dev/null 2>&1 || true
	fi
	docker rm -f "$HS" "$CLIENT" >/dev/null 2>&1 || true
	# --rm containers go away a moment after the rm returns.
	i=0
	while [ -n "$(docker ps -aq -f "name=^$HS\$" -f "name=^$CLIENT\$")" ] && [ $i -lt 40 ]; do
		i=$((i + 1))
		sleep 0.5
	done
	case $D in /tmp/* | /private/tmp/* | "${TMPDIR:-/nonexistent}"*) rm -rf "$D" ;; *) echo "demo-headscale: left $D in place (not under /tmp): delete it yourself" ;; esac
}

case ${1:-} in
down)
	down
	echo "demo-headscale: stopped and removed"
	exit 0
	;;
up) ;;
*)
	sed -n '2,7p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac

command -v docker >/dev/null 2>&1 || die "docker not found"
docker info >/dev/null 2>&1 || die "the docker daemon is not running"
command -v tw >/dev/null 2>&1 || die "tw not found (from a checkout: make deps build)"

down
# Headscale's files go in a directory no earlier run used: Docker Desktop can
# keep serving a deleted directory to a container started right after.
HSDIR=$D/hs-$(date +%s)
mkdir -p "$D/state" "$HSDIR/lib"
chmod 777 "$HSDIR/lib"
cat >"$HSDIR/config.yaml" <<EOF
server_url: http://127.0.0.1:$PORT
listen_addr: 0.0.0.0:8080
metrics_listen_addr: 127.0.0.1:9090
grpc_listen_addr: 127.0.0.1:50443
grpc_allow_insecure: false
noise:
  private_key_path: /var/lib/headscale/noise_private.key
prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48
  allocation: sequential
derp:
  server:
    enabled: false
  urls:
    - https://controlplane.tailscale.com/derpmap/default
  auto_update_enabled: false
  update_frequency: 24h
disable_check_updates: true
node:
  ephemeral:
    inactivity_timeout: 30m
database:
  type: sqlite
  sqlite:
    path: /var/lib/headscale/db.sqlite
log:
  level: info
policy:
  mode: database
dns:
  magic_dns: true
  base_domain: $DOMAIN
  override_local_dns: false
  nameservers:
    global: []
unix_socket: /var/run/headscale/headscale.sock
unix_socket_permission: "0770"
EOF

docker run -d --name "$HS" -p "127.0.0.1:$PORT:8080" \
	-v "$HSDIR/config.yaml:/etc/headscale/config.yaml:ro" -v "$HSDIR/lib:/var/lib/headscale" \
	"$IMAGE" serve >/dev/null
i=0
until curl -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; do
	i=$((i + 1))
	[ $i -lt 120 ] || {
		docker logs --tail 20 "$HS" >&2 || true
		die "headscale did not become healthy on 127.0.0.1:$PORT"
	}
	sleep 0.5
done
key=$(docker exec "$HS" headscale apikeys create --expiration 12h | tail -n 1)
(umask 077 && printf 'HEADSCALE_API_KEY=%s\n' "$key" >"$D/headscale.env")

docker exec "$HS" headscale users create demo >/dev/null
join=$(docker exec "$HS" headscale preauthkeys create --user 1 --ephemeral --expiration 12h | tail -n 1)
docker run -d --rm --name "$CLIENT" --hostname "$CLIENT" --cap-add NET_ADMIN --device /dev/net/tun \
	-e TS_AUTHKEY="$join" -e TS_USERSPACE=false -e TS_STATE_DIR=/tmp/ts -e TS_ACCEPT_DNS=true \
	-e TS_EXTRA_ARGS="--login-server=http://host.docker.internal:$PORT" "$CLIENT_IMAGE" >/dev/null
i=0
until docker exec "$CLIENT" tailscale status >/dev/null 2>&1; do
	i=$((i + 1))
	[ $i -lt 60 ] || die "the client container did not join: docker logs $CLIENT"
	sleep 1
done
docker exec "$CLIENT" sh -c 'command -v curl >/dev/null 2>&1 || apk add --no-cache curl >/dev/null 2>&1' ||
	die "could not install curl in the client container"

# USER names the operator in the audit trail (<user>@<host>): keep a real
# login out of a recording.
USER=demo tw up --service=false --control=headscale --control-url "http://127.0.0.1:$PORT" \
	--tailnet "$DOMAIN" --api-key-file "$D/headscale.env" --apply-policy --yes --ephemeral --host-id demo

{
	echo "# written by scripts/demo-headscale.sh up; source it, then run scripts/demo.sh"
	echo "export TW_STATE_DIR='$D/state'"
	if [ -x "$ROOT/bin/tw" ]; then
		echo "export PATH='$ROOT/bin':\"\$PATH\""
		echo "export TW_WORKERD='$TW_WORKERD'"
		echo "export TW_CREATE_TAILWORKS='$TW_CREATE_TAILWORKS'"
	fi
	echo "export USER=demo"
	echo "export DEMO_TAILNET=$DOMAIN"
	echo "export DEMO_CONTROL_URL=http://127.0.0.1:$PORT"
	echo "export DEMO_API_KEY_FILE='$D/headscale.env'"
	echo "export DEMO_DIR='$D/apps'"
	echo "export DEMO_CALL='docker exec $CLIENT curl'"
} >"$D/env"

echo
echo "demo-headscale: ready. Run the demo against it:"
echo "  . $D/env && scripts/demo.sh --yes"
echo "and remove it afterwards: scripts/demo-headscale.sh down"
