#!/bin/sh
# e2e-linux.sh [distro…]: the E2E-LINUX matrix, at most $TW_E2E_PARALLEL
# (default 2) VMs at a time; each VM is stopped as soon as its row is done.
# Rows run in the order given (put the slow amd64 one first).
# Exit status: non-zero if any row failed. See scripts/e2e-linux-one.sh.
ROOT=$(cd "$(dirname "$0")/.." && pwd)
: "${TW_E2E_OUT:=$ROOT/e2e/real/results/linux-$(date +%Y%m%d-%H%M%S)}"
export TW_E2E_OUT
mkdir -p "$TW_E2E_OUT"
[ $# -gt 0 ] || set -- ubuntu24-amd64 ubuntu24-arm64 debian12-arm64 fedora-arm64 ubuntu22-arm64
# TW_E2E_RELEASE=vX.Y.Z: test that GitHub release's linux tarballs (gh release
# download, checked against its SHA256SUMS here; install.sh checks the
# signature in the VM) instead of TW_E2E_TARBALL_<arch> from `make release`.
if [ -n "${TW_E2E_RELEASE:-}" ]; then
	rel=$ROOT/dist/published/$TW_E2E_RELEASE
	v=${TW_E2E_RELEASE#v}
	mkdir -p "$rel"
	gh release download "$TW_E2E_RELEASE" --dir "$rel" --clobber \
		-p SHA256SUMS -p SHA256SUMS.sig -p "tailworks-$v-linux-arm64.tar.gz" -p "tailworks-$v-linux-amd64.tar.gz" ||
		{ echo "e2e-linux: gh release download $TW_E2E_RELEASE failed" >&2; exit 2; }
	(cd "$rel" && grep -E " tailworks-$v-linux-(arm64|amd64)\.tar\.gz\$" SHA256SUMS | shasum -a 256 -c) ||
		{ echo "e2e-linux: $TW_E2E_RELEASE tarballs don't match its SHA256SUMS" >&2; exit 2; }
	TW_E2E_TARBALL_arm64=$rel/tailworks-$v-linux-arm64.tar.gz
	TW_E2E_TARBALL_amd64=$rel/tailworks-$v-linux-amd64.tar.gz
	export TW_E2E_TARBALL_arm64 TW_E2E_TARBALL_amd64
fi
echo "e2e-linux: rows $* (≤ ${TW_E2E_PARALLEL:-2} at a time) → $TW_E2E_OUT"
printf '%s\n' "$@" | xargs -P "${TW_E2E_PARALLEL:-2}" -n 1 sh "$ROOT/scripts/e2e-linux-one.sh"
rc=$?
echo "e2e-linux: results in $TW_E2E_OUT"
ls "$TW_E2E_OUT"/*.json 2>/dev/null | while read -r f; do
	jq -r '[.run, .checks.result, (.numbers.install_ms|tostring), (.numbers.up_ready_ms|tostring), (.numbers.deploy_agent_ready_ms|tostring)] | join(" ")' "$f" 2>/dev/null
done
exit $rc
