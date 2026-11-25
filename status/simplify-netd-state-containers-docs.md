Last update: 2026-11-24 PT. Simplify audit #441, #450, #499: stop shipping tw-netd, one state-dir table, containers.md cleanup.

## What changed

- **#441** `scripts/release.sh` no longer builds `./cmd/tw-netd` into the
  tarballs (nothing execs it: tw-hostd links netd; `tw doctor` and
  `findBin` know only tw-mgmt and tw-hostd; install.sh, get.sh and the npm
  packaging never name it). `cmd/tw-netd/main.go` says it is the netd test
  harness (e2e/local builds it itself). Docs: INSTALL.md (npm platform
  package contents, tarball contents and refreshed sizes, measured from the
  v0.5.0 assets minus tw-netd), SECURITY.md and THREAT-MODEL.md ("tw-hostd
  (which embeds netd)"), ADR-041, an RFC "As built" row (netd is a library
  inside tw-hostd), limits.md (concurrent node joins: not configurable,
  `netd/registry.go:JoinParallel`). Fragment `changes/<branch>.md`,
  `kind: breaking`.
- **#450** `tw up` no longer passes the never-written `<state>/tenants` to
  hostd's `-sandbox-deny`; `up.go` now uses `sandboxDenyList()` so the two
  lists can't drift again. INSTALL.md has one "What is in ~/.tailworks"
  table (state dir and prefix: written by, in `tw backup`, removed by
  uninstall, safe to delete), linked from Back up, Uninstall and
  upgrading.md.
- **#499** containers.md links ADR-032 in this repo; the Lifecycle table's
  three extra facts (monitor() exit semantics, parked monitor() is not
  activity, ContainerGrace/ContainerMaxLife) are in the Lifetime bullets;
  the table is gone, its closing paragraph kept under "How `running` is
  kept".

## Not done

- #450's "`tw doctor` prints sizes only under `-v`/`--json` with a bounded
  walk": doctor prints no per-entry state-dir sizes today, so there is
  nothing to gate; adding a size walk is a feature, left for a follow-up.

## Tests

- `make lint`: pass. `make test-docs`: pass. `make changes-check`: ok (no
  upgrade-relevant paths).
- `make test`: the first run failed in timing- and liveness-sensitive tests
  (`TestStartCloseBudget`, `TestWarmKeysRevokedOnCloseInParallel`,
  `no_host: h1 not live`, ...) with the machine at load average ~130 from
  parallel agents. Rerun per package (`go test -count=1` of
  `./cmd/tw/ ./hostd/ ./hostd/gen/ ./mgmt/ ./internal/mgmtd/`): all pass.
- `make e2e-local` (TestNetdBinary, still builds cmd/tw-netd itself): pass.
- `make e2e-tw-upgrade`, `make install-test`, `make npm-cli-test`: pass.
