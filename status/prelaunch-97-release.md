# prelaunch-97-release: ready the repo for the first published release (issue #97)

Last update: 2026-11-16 PT. Repo-side fixes so a correctly named, signed
v0.2.0 can be tagged and published; the publish itself is the maintainer's.

## Findings (at master 6ab669c)

- **v0.2.0's notes are stale.** `scripts/release-notes.sh --finalize` ran
  for v0.2.0 on 2026-11-15 (`internal/catalog/releases/v0.2.0.json`,
  `changes/released/v0.2.0/`, CHANGELOG.md), but v0.2.0 was never tagged.
  22 fragments landed since (the rename to tailworks among them) and sit
  pending in `changes/`. Tagging master as v0.2.0 would have drafted a
  release whose body (release.yml uses `changes/released/v0.2.0/`) and
  catalog entry miss all of them, and the next release would claim them.
- **The `gates` job's `make e2e-upgrade` could never pass.** Its default
  baseline was the last `v*` tag, v0.1.0: never published, and from before
  the rename (`cmd/twd-mgmt`, `cmd/twd-hostd`; hosts send
  `Twd-Host-Token`, which tw-mgmt no longer reads). Run locally with the
  build fixed to find the `twd-*` commands, the v0.1.0 mgmt never answered
  `/v1/healthz` with the renamed header ("mgmt did not start"). Nobody can
  be running v0.1.0 (it was only ever a draft), so it is the wrong baseline.
- `scripts/get.sh`'s header still said "releases before v0.3.0 are
  unsigned"; the code refuses every unsigned release. README step 1's
  example was `sh get.sh v0.1.0` (a twd-named draft that can't install).
- No `release` environment and no repository secrets exist yet
  (`TW_RELEASE_SIGNING_KEY`, `NPM_TOKEN`); `internal/releasesig/allowed_signers`
  pins no key.
- The tw-vz "not available on this hardware" failure of the one
  release.yml run (2026-11-15) is already handled in `scripts/release.sh`
  (warns and goes on for VZErrorDomain Code=2 only).

## Changes

- `scripts/release-check.sh <tag>` (new): tag = VERSION, notes finalized
  for the tag with nothing pending in `changes/`, a signing key pinned,
  npm versions match. Prints every problem. Runs first in release.yml's
  `gates`; docs/RELEASING.md step 4 runs it before tagging.
- `scripts/upgrade-from.sh` (new): the `make e2e-upgrade` baseline is the
  newest *published* GitHub release (as get.sh finds it), or `self` while
  none is published / GitHub is unreachable / it is this tree's own
  version. Makefile `TW_UPGRADE_FROM` default uses it (lazily: only
  e2e-upgrade expands it).
- docs/RELEASING.md: check-then-tag step, `headscale` and release-check in
  the gate list, installing from a draft's assets
  (`gh release download` + `TW_RELEASE_BASE=file://…`), publishing with
  `gh release edit --draft=false --latest` and checking `releases/latest`,
  the newcomer smoke, and "Re-finalizing an unpublished release".
- get.sh header comments, README step 1 example (`vX.Y.Z`), ADR-041 and
  e2e/upgrade comments for the new baseline.

## Tests

- `sh scripts/release-check.sh v0.2.0` on master: fails, listing the 22
  pending fragments and the missing signing key (as intended).
- Trial re-finalize (reverted, not committed): the recipe in RELEASING.md
  moved 37 fragments into v0.2.0; `release-check.sh v0.2.0` then failed
  only on the signing key; `go test ./internal/catalog/... ./mgmt/ -run
  'Catalog|Changelog|Release|Upgrade'` passed.
- `make e2e-upgrade TW_UPGRADE_FROM=v0.1.0` (old default): FAIL, mgmt did
  not start (pre-rename binaries; see Findings).
- `make e2e-upgrade TW_UPGRADE_FROM=self`: PASS (0.0.1 → 0.2.0, 44 s).
- `make e2e-upgrade` (new default; upgrade-from.sh resolved it to self): PASS (36 s).
- `make install-test`: ok.
- `make lint test`: PASS. `make changes-check`: ok (no upgrade-relevant paths; no fragment needed, nothing user-visible).

## Left for the maintainer

The exact publish steps are in the PR body (and docs/RELEASING.md). Not
done here: the key ceremony, the `release` environment and secrets,
re-finalizing v0.2.0 (it must be the last PR before the tag), tagging,
running release.yml, deleting the stale `twd v0.1.0` draft, publishing,
and the post-release smoke on clean machines.
