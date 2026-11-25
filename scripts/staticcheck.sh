#!/bin/sh
# staticcheck for linux and darwin, run by `make staticcheck`.
#
# usage: scripts/staticcheck.sh <path to staticcheck>
#
# Packages that build on both OSes are checked together with -matrix, so
# code that only one OS uses isn't reported unused. Packages that build on
# one OS only (hostd/vm/helper, …) are checked for that OS alone: -matrix
# fails on a package that has no files for one of its configurations.
# Everything is type-checked with cgo off, so a Mac and CI see the same
# thing. cmd/tw-vz (cgo, Virtualization.framework) can't be type-checked
# without cgo, so it's left out.
set -eu
sc=$1
GO=${GO:-go}
tmp=$(mktemp -d)
trap 'rm -r "$tmp"' EXIT

for os in linux darwin; do
	GOOS=$os CGO_ENABLED=0 $GO list ./... | grep -v '/cmd/tw-vz$' | sort >"$tmp/$os"
done
both=$(comm -12 "$tmp/linux" "$tmp/darwin")
linux_only=$(comm -23 "$tmp/linux" "$tmp/darwin")
darwin_only=$(comm -13 "$tmp/linux" "$tmp/darwin")

printf 'linux: GOOS=linux CGO_ENABLED=0\ndarwin: GOOS=darwin CGO_ENABLED=0\n' |
	"$sc" -matrix $both
if [ -n "$linux_only" ]; then
	GOOS=linux CGO_ENABLED=0 "$sc" $linux_only
fi
if [ -n "$darwin_only" ]; then
	GOOS=darwin CGO_ENABLED=0 "$sc" $darwin_only
fi
