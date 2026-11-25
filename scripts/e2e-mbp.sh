#!/bin/sh
# e2e-mbp.sh <ssh host> <go test args…>: runs the e2e-mbp Go test, and on
# exit (pass, fail, timeout or ^C) makes sure nothing of the run is left on
# the remote Mac: tw down --purge if still running, stop tailworks processes,
# uninstall, and remove ~/.tailworks-e2e (with the API key file). The Go test does
# the same in t.Cleanup; this is the backstop for a killed test process.
host=$1
shift
cleanup() {
	ssh -o BatchMode=yes -o ConnectTimeout=15 "$host" '
		[ -d ~/.tailworks-e2e ] || exit 0
		echo "e2e-mbp trap: cleaning ~/.tailworks-e2e on $(hostname -s)"
		if [ -x ~/.tailworks/bin/tw ] && [ -f ~/.tailworks-e2e/state/supervisor.pid ] && kill -0 "$(cat ~/.tailworks-e2e/state/supervisor.pid)" 2>/dev/null; then
			~/.tailworks/bin/tw --state-dir ~/.tailworks-e2e/state down --purge >/dev/null 2>&1 || true
		fi
		pkill -TERM -f "$HOME/.tailworks/bin/" 2>/dev/null; sleep 0.5; pkill -KILL -f "$HOME/.tailworks/bin/" 2>/dev/null
		u=$(ls -d ~/.tailworks-e2e/tailworks-*/install.sh 2>/dev/null | head -1)
		[ -n "$u" ] && "$u" --uninstall --purge --force >/dev/null
		rm -rf ~/.tailworks-e2e
		true' || echo "e2e-mbp trap: ssh $host failed: check ~/.tailworks-e2e on it by hand" >&2
}
trap cleanup EXIT
trap 'exit 130' INT TERM
go test "$@"
