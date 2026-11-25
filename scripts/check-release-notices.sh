#!/bin/sh
# check-release-notices.sh <tarball>...: fail unless every release tarball
# carries its license files (issue #102; scripts/third-party-notices.mjs
# writes them): LICENSE and THIRD_PARTY_NOTICES at the top, and with a
# microVM guest (share/tailworks/vm/vmlinux) also share/tailworks/vm/
# KERNEL-SOURCE.md and ROOTFS-SOURCE.md. Run by scripts/release.sh after
# each tarball and by release.yml before the upload.
set -eu
[ $# -gt 0 ] || { echo "usage: check-release-notices.sh <tarball>..."; exit 2; }
bad=0
for t in "$@"; do
	list=$(tar -tzf "$t")
	top=$(printf '%s\n' "$list" | head -n 1 | sed 's|^\./||; s|/.*||')
	need="LICENSE THIRD_PARTY_NOTICES"
	if printf '%s\n' "$list" | sed 's|^\./||' | grep -qx "$top/share/tailworks/vm/vmlinux"; then
		need="$need share/tailworks/vm/KERNEL-SOURCE.md share/tailworks/vm/ROOTFS-SOURCE.md"
	fi
	for f in $need; do
		if ! printf '%s\n' "$list" | sed 's|^\./||' | grep -qx "$top/$f"; then
			echo "check-release-notices: $t has no $top/$f"
			bad=1
		fi
	done
	# THIRD_PARTY_NOTICES must name what every tarball redistributes.
	if printf '%s\n' "$list" | sed 's|^\./||' | grep -qx "$top/THIRD_PARTY_NOTICES"; then
		tar -xzOf "$t" "$top/THIRD_PARTY_NOTICES" | grep '^1\. workerd$' >/dev/null ||
			{ echo "check-release-notices: $t: THIRD_PARTY_NOTICES has no workerd section"; bad=1; }
	fi
	[ $bad = 1 ] || echo "check-release-notices: $t: ok ($need)"
done
exit $bad
