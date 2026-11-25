#!/bin/sh
# demo.sh: the launch demo, as a script you can run again and again (#323).
#
#   scripts/demo.sh --tailnet <tailnet> [--yes] [--up] [--no-pause] [--dir <dir>]
#   scripts/demo.sh --tailnet <tailnet> --cleanup [--yes]
#
# What it shows, in order:
#
#   1. tw status              tailworks is up (--up runs `tw up` first if it isn't)
#   2. tw init                an MCP server (template mcp) and an agent (template agent)
#   3. tw policy --apply      the apps' tags, then the two grants of the demo
#   4. tw deploy              both apps
#   5. a tool call            from a tailnet device, through the agent: allowed
#   6. tw ls                  the agent is a Durable Object with its own tailnet node
#   7. one grant narrowed     the same tool call is now denied (cap_missing)
#   8. the grant restored     allowed again
#   9. tw kill                the agent's node is gone; the call answers `killed`
#  10. tw audit               the agent's hash-chained record
#
# It writes to a tailnet's policy, so:
#
#   - --tailnet (or DEMO_TAILNET) is required and must be the tailnet the
#     local `tw` serves (`tw status`); the script stops if they differ.
#   - It prints what it will change and asks before the first write (--yes
#     skips the question; without a terminal --yes is required).
#   - It adds: tagOwners for the demo apps' tags (tw policy --apply), and two
#     grants (the caller grant and the tool grant, see internal/demogrant).
#     It changes one thing: the tool grant's `tools` list, for a few seconds.
#     An EXIT/INT/TERM trap restores it however the script ends.
#   - --cleanup undeploys both apps, removes the two grants and deletes the
#     app directories. The tagOwners entries stay (tw never removes policy);
#     they are harmless and a rerun reuses them.
#   - A rerun after --cleanup deploys the apps again under the same names
#     (#364); a rerun without it redeploys over the running apps.
#
# Pauses, for recording: set DEMO_PAUSE=<seconds> to sleep between steps.
# Without it the script waits for Enter when stdin is a terminal and does
# not pause otherwise. --no-pause is DEMO_PAUSE=0. DEMO_CLEAR=1 clears the
# screen before each step, so a step fills the frame.
#
# Environment:
#   DEMO_TAILNET            same as --tailnet
#   DEMO_PAUSE              seconds between steps (see above)
#   DEMO_CLEAR              1: clear the screen before each step
#   DEMO_DIR                where the two apps are created (default ${TMPDIR:-/tmp}/tw-demo)
#   DEMO_TW                 the tw binary (default: tw on PATH)
#   DEMO_CALL               the curl that runs on a tailnet device (default: curl,
#                           this machine; e.g. "ssh laptop curl")
#   DEMO_CALLER_SRC         who the caller grant lets in (default autogroup:member)
#   DEMO_NAME               the MCP app's name (default demo); the agent app is
#                           <name>-agent
#   DEMO_AGENT              the agent instance's name (default demo-<HHMMSS>; a
#                           killed name stays killed, so each run needs a new one)
#   DEMO_OAUTH_CLIENT_FILE  Tailscale: OAuth client file with Policy File write
#                           (default ~/.config/tailworks/oauth.env, what tw up saved)
#   DEMO_API_KEY_FILE       Tailscale: TS_API_KEY=… file instead of the OAuth
#                           client; Headscale: HEADSCALE_API_KEY=… file (required)
#   DEMO_CONTROL_URL        Headscale: the server URL (required there)
#   DEMO_UP_ARGS            extra arguments for `tw up` with --up
#   DEMO_GRANT              the grant editor (default: built from internal/demogrant,
#                           which needs Go and this repo)
#
# Recording it (asciinema, the GIF and MP4, the screenshots): docs/demo.md.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TW=${DEMO_TW:-tw}
TAILNET=${DEMO_TAILNET:-}
WORK=${DEMO_DIR:-${TMPDIR:-/tmp}/tw-demo}
CALL=${DEMO_CALL:-curl}
CALLER_SRC=${DEMO_CALLER_SRC:-autogroup:member}
AGENT=${DEMO_AGENT:-demo-$(date +%H%M%S)}
YES=0
UP=0
CLEANUP=0

# The two apps and their tags, as tw init names them after the app.
NAME=${DEMO_NAME:-demo}
MCP_APP=$NAME
AGENT_APP=$NAME-agent
MCP_TAG=tag:tw-wl-$MCP_APP
AGENT_WL_TAG=tag:tw-wl-$AGENT_APP
AGENT_TAG=tag:tw-$AGENT_APP-support-agent

usage() {
	sed -n '2,5p' "$0" | sed 's/^# \{0,1\}//'
	echo "  (the header of $0 lists the environment variables)"
}

die() {
	printf 'demo: %s\n' "$*" >&2
	exit 1
}

while [ $# -gt 0 ]; do
	case $1 in
	--tailnet) [ $# -ge 2 ] || die "--tailnet needs a value"; TAILNET=$2; shift ;;
	--tailnet=*) TAILNET=${1#--tailnet=} ;;
	--dir) [ $# -ge 2 ] || die "--dir needs a value"; WORK=$2; shift ;;
	--dir=*) WORK=${1#--dir=} ;;
	--yes | -y) YES=1 ;;
	--up) UP=1 ;;
	--cleanup) CLEANUP=1 ;;
	--no-pause) DEMO_PAUSE=0 ;;
	--help | -h) usage; exit 0 ;;
	*) usage >&2; die "unknown argument: $1" ;;
	esac
	shift
done

if [ -t 1 ]; then
	B=$(printf '\033[1m') D=$(printf '\033[2m') G=$(printf '\033[32m') R=$(printf '\033[31m') N=$(printf '\033[0m')
else
	B='' D='' G='' R='' N=''
fi

step() {
	[ "${DEMO_CLEAR:-0}" != 1 ] || printf '\033[H\033[2J'
	printf '\n%s# %s%s\n' "$B" "$*" "$N"
}
note() { printf '%s  %s%s\n' "$D" "$*" "$N"; }

# show prints a command the way it would be typed, then runs it.
show() {
	line=
	for a in "$@"; do
		case $a in *' '*) a="\"$a\"" ;; esac
		line="$line $a"
	done
	printf '%s$%s%s\n' "$B" "$line" "$N"
	"$@"
}

pause() {
	if [ -n "${DEMO_PAUSE:-}" ]; then
		[ "$DEMO_PAUSE" = 0 ] || sleep "$DEMO_PAUSE"
	elif [ -t 0 ]; then
		printf '%s  [Enter]%s' "$D" "$N"
		read -r _ || true
	fi
}

confirm() {
	[ "$YES" = 1 ] && return 0
	[ -t 0 ] || die "this writes to the tailnet policy of $TAILNET: run it with --yes when there is no terminal"
	printf '%s [y/N] ' "$1"
	read -r a || a=
	case $a in y | Y | yes) ;; *) die "stopped; nothing was changed" ;; esac
}

# ---- preflight (this script writes no policy before the confirmation) ------

command -v "$TW" >/dev/null 2>&1 || die "$TW not found (install tailworks, or set DEMO_TW)"
[ -n "$TAILNET" ] || { usage >&2; die "name the tailnet: --tailnet <tailnet> (or DEMO_TAILNET). The demo edits its policy"; }

status=$("$TW" status --json 2>/dev/null) || {
	[ "$UP" = 1 ] || die "tailworks is not running here ('$TW status' failed): run tw up first, or pass --up"
	step "tw up: start tailworks on this machine"
	# shellcheck disable=SC2086
	show "$TW" up ${DEMO_UP_ARGS:-}
	status=$("$TW" status --json) || die "tw up ran, but tw status still fails"
	pause
}
have=$(printf '%s\n' "$status" | sed -n 's/^  "tailnet": "\(.*\)",\{0,1\}$/\1/p' | head -n 1)
mint=$(printf '%s\n' "$status" | sed -n 's/^  "mintMode": "\(.*\)",\{0,1\}$/\1/p' | head -n 1)
[ "$have" = "$TAILNET" ] || die "this machine's tw serves tailnet '$have', not '$TAILNET': refusing to touch a tailnet you did not name"

# The grant editor's credentials: the same kind tw up was given.
if [ "$mint" = headscale ]; then
	[ -n "${DEMO_CONTROL_URL:-}" ] && [ -n "${DEMO_API_KEY_FILE:-}" ] ||
		die "a Headscale tailnet: set DEMO_CONTROL_URL and DEMO_API_KEY_FILE (what tw up --control-url / --api-key-file got)"
	set -- --control headscale --control-url "$DEMO_CONTROL_URL" --api-key-file "$DEMO_API_KEY_FILE"
elif [ -n "${DEMO_API_KEY_FILE:-}" ]; then
	set -- --control tailscale --api-key-file "$DEMO_API_KEY_FILE"
else
	oauth=${DEMO_OAUTH_CLIENT_FILE:-$HOME/.config/tailworks/oauth.env}
	[ -f "$oauth" ] || die "no OAuth client file at $oauth: set DEMO_OAUTH_CLIENT_FILE (it needs Policy File write) or DEMO_API_KEY_FILE"
	set -- --control tailscale --oauth-client-file "$oauth"
fi
set -- "$@" --tailnet "$TAILNET" --caller-src "$CALLER_SRC" \
	--agent-workload-tag "$AGENT_WL_TAG" --agent-tag "$AGENT_TAG" --mcp-tag "$MCP_TAG"

mkdir -p "$WORK"
WORK=$(cd "$WORK" && pwd)
if [ -n "${DEMO_GRANT:-}" ]; then
	GRANT=$DEMO_GRANT
else
	command -v go >/dev/null 2>&1 || die "the grant editor is built with Go (internal/demogrant): install Go, or set DEMO_GRANT to a built binary"
	GRANT=$WORK/.demo-grant
	(cd "$ROOT" && go build -o "$GRANT" ./internal/demogrant) || die "could not build internal/demogrant"
fi
# grant <op…>: run the editor with the credentials and tags above. They are
# kept one per line in a file, so the exit trap can use them too (paths, not
# secrets).
GRANT_ARGS=$WORK/.grant-args
: >"$GRANT_ARGS"
trap 'rm -f "$GRANT_ARGS"' EXIT
for a in "$@"; do printf '%s\n' "$a" >>"$GRANT_ARGS"; done
grant() {
	ops=$*
	set --
	while IFS= read -r a; do set -- "$@" "$a"; done <"$GRANT_ARGS"
	# shellcheck disable=SC2086
	"$GRANT" "$@" $ops
}

# How to undo this run, for the messages.
UNDO="scripts/demo.sh --tailnet $TAILNET --cleanup"
[ "$NAME" = demo ] || UNDO="DEMO_NAME=$NAME $UNDO"

# ---- --cleanup -------------------------------------------------------------

if [ "$CLEANUP" = 1 ]; then
	echo "demo --cleanup on tailnet $TAILNET:"
	echo "  - undeploy workloads $AGENT_APP and $MCP_APP (their objects are killed, their devices deleted)"
	echo "  - remove the demo's two grants from the tailnet policy:"
	grant --dry-run remove | sed -e '/^dry run:/d' -e 's/^/      /'
	echo "  - delete $WORK/$MCP_APP and $WORK/$AGENT_APP"
	confirm "Go ahead?"
	"$TW" undeploy "$AGENT_APP" --reason "demo cleanup" || echo "  ($AGENT_APP was not deployed)"
	"$TW" undeploy "$MCP_APP" --reason "demo cleanup" || echo "  ($MCP_APP was not deployed)"
	grant remove
	rm -rf "${WORK:?}/$MCP_APP" "${WORK:?}/$AGENT_APP" "$WORK/.demo-grant" "$GRANT_ARGS"
	echo "demo: cleaned up. The tagOwners entries for $MCP_TAG, $AGENT_WL_TAG and the agent tags stay in the policy."
	exit 0
fi

# ---- what will change, and the question ------------------------------------

echo "demo on tailnet $TAILNET (apps in $WORK). It will:"
echo "  - add tagOwners for the two apps' tags to the tailnet policy (tw policy --apply; additive)"
echo "  - add these grants if they are missing:"
grant --dry-run ensure | sed -e '/^dry run:/d' -e 's/^/      /'
echo "  - deploy workloads $MCP_APP (template mcp) and $AGENT_APP (template agent)"
echo "  - take \"refund\" out of the grant $AGENT_TAG → $MCP_TAG for a few seconds, then put it back"
echo "  - kill the agent object it created ($AGENT)"
echo "Nothing else in the policy is touched. Undo: $UNDO"
confirm "Go ahead?"
[ "$YES" != 1 ] || pause

NARROWED=0
on_exit() {
	rc=$?
	trap - EXIT INT TERM
	if [ "$NARROWED" = 1 ]; then
		echo
		echo "demo: restoring the grant"
		grant restore || echo "demo: COULD NOT RESTORE THE GRANT. Put \"refund\" back in the tools of the grant $AGENT_TAG → $MCP_TAG (or run: $UNDO)" >&2
	fi
	rm -f "$GRANT_ARGS"
	exit "$rc"
}
trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# ---- helpers for the calls -------------------------------------------------

# nodes prints "namespace|objectName|workload|magicDNS" for every live object.
nodes() {
	"$TW" ls --json | awk -F'"' '
		/^ *"objectID":/ { ns = ""; name = "" }
		/^ *"workload":/ { wl = $4 }
		/^ *"namespace":/ { ns = $4 }
		/^ *"objectName":/ { name = $4 }
		/^ *"magicDNS":/ { if (ns != "") print ns "|" name "|" wl "|" $4 }'
}
node_of() { nodes | awk -F'|' -v ns="$1" -v name="$2" '$1 == ns && $2 == name { print $4; exit }'; }

# call_raw <tool>: the agent calls one MCP tool, asked by a tailnet device.
call_raw() {
	# shellcheck disable=SC2086
	$CALL -sS -m 60 "http://$AGENT_DNS/agents/support-agent/$AGENT/tool?server=http://$MCP_DNS/mcp&name=$1" 2>&1 || true
}

# wait_for <tool> <pattern>: policy changes take a moment to reach the nodes.
wait_for() {
	i=0
	while [ $i -lt 60 ]; do
		case $(call_raw "$1") in *"$2"*) return 0 ;; esac
		i=$((i + 1))
		sleep 1
	done
	die "after 60 s the $1 call still does not answer $2: $(call_raw "$1" | cut -c1-300)"
}

# call <tool> <pattern>: show the call and its answer; fail unless it matches.
call() {
	printf '%s$ %s '\''http://%s/agents/support-agent/%s/tool?server=http://%s/mcp&name=%s'\''%s\n' "$B" "$CALL" "$AGENT_DNS" "$AGENT" "$MCP_DNS" "$1" "$N"
	out=$(call_raw "$1")
	case $out in
	*'"ok":true'*) printf '%s%s%s\n' "$G" "$out" "$N" ;;
	*)
		# A refusal carries the whole typed error; show its code and message.
		case $out in
		*',"text":'*) printf '%s%s, …}%s\n' "$R" "${out%%,\"text\":*}" "$N" ;;
		*) printf '%s%s%s\n' "$R" "$(printf '%s' "$out" | cut -c1-300)" "$N" ;;
		esac
		msg=$(printf '%s' "$out" | sed -n 's/.*\\\{0,1\}"message\\\{0,1\}":\\\{0,1\}"\(.*\)\\\{0,1\}",\\\{0,1\}"doc_url.*/\1/p' | sed 's/\\\\\\"/"/g; s/\\"/"/g; s/\\$//')
		case $out in *',"text":'*) [ -z "$msg" ] || printf '%s  → %s%s\n' "$R" "$msg" "$N" ;; esac
		;;
	esac
	case $out in *"$2"*) ;; *) die "expected $2 from the $1 call" ;; esac
}

# ---- the demo --------------------------------------------------------------

step "1. tailworks is up: one process on this machine, on tailnet $TAILNET"
show "$TW" status
pause

step "2. An MCP server and an agent, from the templates (Agents SDK and MCP, unmodified)"
cd "$WORK"
if [ -f "$MCP_APP/tailworks.jsonc" ]; then
	note "$WORK/$MCP_APP exists: keeping it (a rerun; --cleanup starts over)"
else
	show "$TW" init "$MCP_APP" --template mcp
fi
if [ -f "$AGENT_APP/tailworks.jsonc" ]; then
	note "$WORK/$AGENT_APP exists: keeping it"
else
	show "$TW" init "$AGENT_APP" --template agent
fi
pause

step "3. The tailnet policy: the apps' tags, who may call the agent, which tools the agent may call"
show "$TW" policy "$MCP_APP" --apply --yes
show "$TW" policy "$AGENT_APP" --apply --yes
note "tw adds tags, never callers. The two grants of this demo:"
grant ensure
pause

step "4. Deploy both"
show "$TW" deploy "$MCP_APP"
show "$TW" deploy "$AGENT_APP"
MCP_DNS=$(node_of _workload "$MCP_APP")
AGENT_DNS=$(node_of _workload "$AGENT_APP")
[ -n "$MCP_DNS" ] && [ -n "$AGENT_DNS" ] || die "tw ls does not list the two workload nodes"
pause

step "5. From a device on the tailnet: ask agent \"$AGENT\" to refund an order through the MCP server"
note "no API key and no OAuth: WireGuard identifies the caller, a grant carries the tools"
wait_for lookup_order '"ok":true'
call refund '"ok":true'
pause

step "6. The agent is a Durable Object with its own node on the tailnet"
# tw ls can show the object as "assigned" for a moment after it already
# answered (it took a pre-joined pool node): wait until it is listed ready.
i=0
OBJ_DNS=
while [ $i -lt 30 ]; do
	OBJ_DNS=$(node_of SupportAgent "$AGENT")
	[ -z "$OBJ_DNS" ] || break
	i=$((i + 1))
	sleep 1
done
[ -n "$OBJ_DNS" ] || die "tw ls does not list the SupportAgent object $AGENT with a node"
show "$TW" ls
OBJ=${OBJ_DNS%%.*}
pause

step "7. Take \"refund\" out of one grant: the same call is denied"
NARROWED=1
grant narrow
wait_for refund cap_missing
call refund cap_missing
call lookup_order '"ok":true'
pause

step "8. Put it back: allowed again"
grant restore
NARROWED=0
wait_for refund '"ok":true'
call refund '"ok":true'
pause

step "9. Kill the agent: its node leaves the tailnet"
show "$TW" kill "$OBJ" --reason "launch demo"
show "$TW" ls
call refund killed
pause

step "10. All of it is in the audit trail"
show "$TW" audit "$OBJ"
echo
echo "demo: done. Run it again as it is, or undo it: $UNDO"
