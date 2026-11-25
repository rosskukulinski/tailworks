#!/bin/sh
# ci-sandbox-images.sh <arch>…: the sandbox images for each arch (arm64,
# amd64) in the self-hosted runner's cache, $TW_CI_CACHE (default
# ~/tw-ci-cache)/images-<arch>-<hash of guest/images>, built with
# guest/images/build-sandbox-images.sh when missing. Prints one
# "CTR_IMAGES_<arch>=<dir>" line per arch (for $GITHUB_ENV).
#
# Several runner instances share the cache, so a build holds
# <cache>/images.lock (a directory; mkdir is atomic); a stale lock (no
# holder for 2 h) is taken over.
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
C=${TW_CI_CACHE:-$HOME/tw-ci-cache}
mkdir -p "$C"
h=$(cat guest/images/* 2>/dev/null | shasum -a 256 | cut -c1-12)
lock=$C/images.lock
waited=0
until mkdir "$lock" 2>/dev/null; do
	if [ -n "$(find "$lock" -maxdepth 0 -mmin +120 2>/dev/null)" ]; then
		echo "ci-sandbox-images: taking over a stale lock" >&2
		rm -rf "$lock"
		continue
	fi
	[ $((waited % 60)) -eq 0 ] && echo "ci-sandbox-images: waiting for another job's build ($lock)" >&2
	sleep 5
	waited=$((waited + 5))
done
trap 'rm -rf "$lock"' EXIT
for arch in "$@"; do
	d=$C/images-$arch-$h
	if [ ! -f "$d/images.json" ]; then
		rm -rf "$C"/images-"$arch"-*
		echo "ci-sandbox-images: building linux/$arch into $d" >&2
		PLATFORM=linux/$arch guest/images/build-sandbox-images.sh "$d" >&2
	fi
	echo "CTR_IMAGES_$arch=$d"
done
