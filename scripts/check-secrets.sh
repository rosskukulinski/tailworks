#!/bin/sh
# Fail if any tracked or staged file contains something shaped like a
# Tailscale key, or private workflow residue that doesn't belong in a public
# repo: a real person's home directory, or notes addressed to the author.
cd "$(git rev-parse --show-toplevel)" || exit 1
fail=0

if git grep -qE 'tskey-(auth|client|api)-[A-Za-z0-9]{8,}-[A-Za-z0-9]{20,}' -- . 2>/dev/null; then
  echo "check-secrets: a tracked file contains a Tailscale key" >&2
  fail=1
fi

# Home directories: only placeholder users are allowed in examples.
home_ok='^/(Users|home)/(me|u|x|you|user|USER|runner|node|alice|bob|example|\.nvm)$'
bad_homes=$(git grep -h -oE '/(Users|home)/[A-Za-z0-9_.-]+' -- . ':!scripts/check-secrets.sh' 2>/dev/null |
  sort -u | grep -vE "$home_ok")
if [ -n "$bad_homes" ]; then
  echo "check-secrets: a tracked file names a real home directory (use /Users/me or ~):" >&2
  for h in $bad_homes; do
    git grep -n -F "$h" -- . ':!scripts/check-secrets.sh' | sed 's/^/  /' >&2
  done
  fail=1
fi

# Notes from a private workflow that were never meant to be published.
if git grep -n -iE 'push(ed)? only to private' -- . ':!scripts/check-secrets.sh' >&2; then
  echo "check-secrets: private-workflow notes in a tracked file (above)" >&2
  fail=1
fi

# Extra patterns that must never be published but can't be listed here
# either (a personal tailnet name, a private hostname): one extended regex
# per line in .git/info/private-patterns, which is never committed.
extra="$(git rev-parse --git-common-dir)/info/private-patterns"
if [ -f "$extra" ] && grep -qvE '^[[:space:]]*(#|$)' "$extra"; then
  pats=$(mktemp) || exit 1
  grep -vE '^[[:space:]]*(#|$)' "$extra" >"$pats"
  if git grep -n -iE -f "$pats" -- . >&2; then
    echo "check-secrets: a tracked file matches $extra (above)" >&2
    fail=1
  fi
  rm -f "$pats"
fi

[ "$fail" = 0 ] || exit 1
echo "check-secrets: ok"
