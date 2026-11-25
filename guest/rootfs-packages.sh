#!/usr/bin/env bash
# rootfs-packages.sh BASE STAGE PATH...: the license trail of the guest
# image's Ubuntu files (issue #102). BASE is the extracted ubuntu-base rootfs
# (with var/lib/dpkg and usr/share/doc), STAGE the image being built, and each
# PATH (relative to STAGE) a file guest/build-image.sh copied from BASE.
#
# For every Debian package that owns one of the PATHs it
#   - copies the package's /usr/share/doc/<pkg>/copyright into STAGE (the
#     Debian convention: the copyright file travels with the binaries), and
#   - prints one JSON object per line:
#     {"package","version","source","source_version","files":[...]}
#     (sorted by package; build-image.sh puts them in manifest.json
#     "packages", and scripts/third-party-notices.mjs turns that into the
#     release's ROOTFS-SOURCE.md: what to fetch for the corresponding source).
#
# A PATH no package owns, or a package without a copyright file, fails the
# build: every Ubuntu file in the image must be accounted for.
# Exit 3: BASE has no dpkg database (a BASE_ROOTFS without one).
set -euo pipefail
BASE=${1:?usage: rootfs-packages.sh BASE STAGE PATH...}
STAGE=${2:?usage: rootfs-packages.sh BASE STAGE PATH...}
shift 2
INFO=$BASE/var/lib/dpkg/info
STATUS=$BASE/var/lib/dpkg/status
if [ ! -f "$STATUS" ] || [ ! -d "$INFO" ]; then
  echo "rootfs-packages: $BASE has no dpkg database (var/lib/dpkg)" >&2
  exit 3
fi

owners=$(mktemp)
trap 'rm -f "$owners"' EXIT
for p in "$@"; do
  # usrmerge: a .list may name /lib/... or /usr/lib/... for the same file.
  rel=/${p#/}
  rel=${rel#/usr}
  list=$(grep -lxF -e "$rel" -e "/usr$rel" "$INFO"/*.list 2>/dev/null | LC_ALL=C sort | head -n 1 || true)
  [ -n "$list" ] || { echo "rootfs-packages: no package in $BASE owns $p" >&2; exit 1; }
  pkg=${list##*/}
  pkg=${pkg%.list}
  pkg=${pkg%%:*}
  printf '%s\t%s\n' "$pkg" "${p#/}" >>"$owners"
done
LC_ALL=C sort -u -o "$owners" "$owners" # a file may be passed twice (ld.so)

for pkg in $(cut -f1 "$owners" | LC_ALL=C sort -u); do
  # The package's paragraph in dpkg's status file: Version and Source
  # ("Source: name" or "Source: name (version)" when it differs).
  fields=$(awk -v p="$pkg" 'BEGIN { RS = ""; FS = "\n" }
    { n = ""; v = ""; s = ""
      for (i = 1; i <= NF; i++) {
        if ($i ~ /^Package: /) n = substr($i, 10)
        else if ($i ~ /^Version: /) v = substr($i, 10)
        else if ($i ~ /^Source: /) s = substr($i, 9)
      }
      if (n == p) { print v "\t" s; exit } }' "$STATUS")
  version=${fields%%	*}
  src=${fields#*	}
  [ -n "$version" ] || { echo "rootfs-packages: $pkg is not in $STATUS" >&2; exit 1; }
  srcname=${src%% *}
  srcver=$version
  [ -n "$srcname" ] || srcname=$pkg
  case $src in *"("*")"*) srcver=${src#*(}; srcver=${srcver%)*} ;; esac

  cr=$BASE/usr/share/doc/$pkg/copyright # -f follows a symlinked doc dir
  [ -f "$cr" ] || { echo "rootfs-packages: $pkg has no usr/share/doc/$pkg/copyright in $BASE" >&2; exit 1; }
  mkdir -p "$STAGE/usr/share/doc/$pkg"
  cp -L "$cr" "$STAGE/usr/share/doc/$pkg/copyright"
  chmod 0644 "$STAGE/usr/share/doc/$pkg/copyright"

  files=$(awk -F '\t' -v p="$pkg" '$1 == p { printf "%s\"/%s\"", (n++ ? "," : ""), $2 }' "$owners")
  printf '{"package":"%s","version":"%s","source":"%s","source_version":"%s","files":[%s]}\n' \
    "$pkg" "$version" "$srcname" "$srcver" "$files"
done
