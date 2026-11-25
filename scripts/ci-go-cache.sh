#!/bin/sh
# ci-go-cache.sh mark|prune: keep a CI job's saved Go cache down to what
# that job used (ci.yml, "Go caches").
#
#   mark   after the cache restore: date every cache file back two hours
#          and drop a marker. Go's build cache and staticcheck's both reset
#          a file's mtime when it is used, but only if the mtime is over an
#          hour old, so the backdating makes every use show. Two hours, not
#          further: both trim entries unused for five days whenever their
#          trim.txt is a day old, and a cache dated further back would be
#          trimmed away by the job's first go command.
#   prune  before the cache save: delete every entry that wasn't used or
#          written since mark (trim.txt and README stay).
#
# Without this the rolling cache keeps everything from the last five days
# (Go's own trim window) of master builds. Measured on 2026-11-24, lint's
# had grown to 29 GB on disk (5.5 GB compressed, about 3.5 min to restore)
# while a run used 4.9 GB of it, and test's to 15 GB while a run used 2.9 GB.
set -eu

dirs="$HOME/.cache/go-build $HOME/.cache/staticcheck"
marker="${RUNNER_TEMP:-/tmp}/go-cache-mark"

existing() {
	for d in $dirs; do [ -d "$d" ] && echo "$d"; done
	return 0
}

case "${1:-}" in
mark)
	[ -n "$(existing)" ] || exit 0 # nothing restored
	# shellcheck disable=SC2046 # word splitting is intended
	find $(existing) -type f -exec touch -m -d '2 hours ago' {} + 2>/dev/null || true
	touch "$marker"
	;;
prune)
	[ -f "$marker" ] || { echo "ci-go-cache: no marker; nothing pruned"; exit 0; }
	for d in $(existing); do
		before=$(du -sm "$d" | cut -f1)
		find "$d" -type f ! -newer "$marker" ! -name trim.txt ! -name README -delete
		echo "ci-go-cache: $d ${before} MB -> $(du -sm "$d" | cut -f1) MB"
	done
	;;
*)
	echo "usage: $0 mark|prune" >&2
	exit 2
	;;
esac
