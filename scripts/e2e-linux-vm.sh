#!/bin/sh
# e2e-linux-vm.sh <lima vm>: L3 E2E from the installed release on a fresh
# nested-virt Lima VM (see the Makefile target).
#   1. build the linux-arm64 release with the microVM assets in Lima tw-dev
#      (from this worktree's files), and copy it here (dist/);
#   2. create <vm> from e2e/linuxvm/twl-kvm-u24.yaml if it doesn't exist, start it;
#   3. run the Go test (e2e/linuxvm), which drives the VM over Lima's ssh;
#   4. on exit, always: tw down --purge, sudo tw host setup --undo, uninstall,
#      remove ~/.tailworks-e2e (and the API key file in it) on the VM; stop the VM.
set -eu
VM=$1
ROOT=$(cd "$(dirname "$0")/.." && pwd)
DEV=${TW_DEV_VM:-tw-dev}
cd "$ROOT"
VERSION=$(cat VERSION)-$(git rev-parse --short=8 HEAD)$(git diff --quiet HEAD -- . ':!STATUS.md' ':!status' || echo -dirty)
REL=/tmp/tw-l3-release
echo "e2e-linux-vm: release $VERSION, built in $DEV:$REL"

# 1. the release tarball. `make release` on the Mac now includes the microVM
#    guest (scripts/vm-assets-lima.sh builds it in tw-dev), so a current
#    dist/ tarball that has share/tailworks/vm is used as is: the row then tests the
#    artifact a user gets. Otherwise (no tarball, or one without the guest),
#    build linux-arm64 + assets inside tw-dev as before.
TGZ=tailworks-$VERSION-linux-arm64.tar.gz
mkdir -p dist
# TW_E2E_TARBALL=<path>: test that tarball (release.yml passes the one it built).
if [ -n "${TW_E2E_TARBALL:-}" ]; then
	TGZ=$(basename "$TW_E2E_TARBALL")
	[ "$TW_E2E_TARBALL" -ef "dist/$TGZ" ] || cp -f "$TW_E2E_TARBALL" "dist/$TGZ"
	echo "e2e-linux-vm: using $TW_E2E_TARBALL (TW_E2E_TARBALL)"
elif [ -f "dist/$TGZ" ] && tar tzf "dist/$TGZ" | grep -q 'share/tailworks/vm/manifest.json$'; then
	echo "e2e-linux-vm: using dist/$TGZ (has share/tailworks/vm; built by make release)"
else
	echo "e2e-linux-vm: dist/$TGZ missing or without share/tailworks/vm: building it in $DEV:$REL"
	limactl shell "$DEV" -- mkdir -p "$REL"
	git ls-files -co --exclude-standard | tar cf - -T - | limactl shell "$DEV" -- tar xf - -C "$REL"
	limactl shell "$DEV" -- bash -lc "cd $REL && make deps >/tmp/tw-l3-release.log 2>&1 && make vm-assets >>/tmp/tw-l3-release.log 2>&1 && \
		TW_VM_ASSETS_arm64=$REL/dist/vm-assets-arm64 make release RELEASE_TARGETS=linux-arm64 RELEASE_VERSION=$VERSION >>/tmp/tw-l3-release.log 2>&1 || { tail -40 /tmp/tw-l3-release.log; exit 1; }"
	limactl copy "$DEV:$REL/dist/$TGZ" "dist/$TGZ"
fi
ls -lh "dist/$TGZ"

# 2. the fresh VM
if ! limactl list -q 2>/dev/null | grep -qx "$VM"; then
	echo "e2e-linux-vm: creating $VM (fresh, nested virt, nothing mounted)"
	limactl create --name="$VM" --tty=false e2e/linuxvm/twl-kvm-u24.yaml
fi
limactl start "$VM" >/dev/null 2>&1 || limactl start "$VM"

cleanup() {
	limactl shell "$VM" -- bash -c '
		[ -d ~/.tailworks-e2e ] || [ -e /etc/tailworks ] || exit 0
		echo "e2e-linux-vm trap: cleaning up on $(hostname)"
		if [ -x ~/.tailworks/bin/tw ] && [ -f ~/.tailworks-e2e/state/supervisor.pid ] && kill -0 "$(cat ~/.tailworks-e2e/state/supervisor.pid)" 2>/dev/null; then
			~/.tailworks/bin/tw --state-dir ~/.tailworks-e2e/state down --purge >/dev/null 2>&1 || true
		fi
		pkill -TERM -f "$HOME/.tailworks/bin/" 2>/dev/null; sleep 0.5; pkill -KILL -f "$HOME/.tailworks/bin/" 2>/dev/null
		[ -x ~/.tailworks/bin/tw ] && sudo ~/.tailworks/bin/tw host setup --undo >/dev/null 2>&1
		u=$(ls -d ~/.tailworks-e2e/tailworks-*/install.sh 2>/dev/null | head -1)
		[ -n "$u" ] && "$u" --uninstall --purge --force >/dev/null
		rm -rf ~/.tailworks-e2e
		true' || echo "e2e-linux-vm trap: cleanup on $VM failed: check ~/.tailworks-e2e there by hand" >&2
	if [ "${TW_E2E_KEEP_VM_RUNNING:-}" = "" ]; then
		limactl stop "$VM" >/dev/null 2>&1 && echo "e2e-linux-vm: stopped $VM (not deleted)"
	fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# 3. the test
TW_E2E_API_KEY_FILE="$TS_CREDS/tailnet1_api.env" TW_E2E_TAILNET="$TAILNET1" TW_E2E_LIMA_VM="$VM" \
	TW_E2E_TARBALL="$ROOT/dist/$TGZ" \
	go test -tags e2elinuxvm -count=1 -v -timeout 40m -run "^${TW_E2E_RUN:-TestE2ELinuxVM}\$" ./e2e/linuxvm/...
