Last update: 2026-11-23 12:00 PT. release.yml tests every runtime and suite before drafting.

# Release tests every runtime

## What changed

- `.github/workflows/release.yml` now runs, before any draft:
  - every hermetic suite (`suites` matrix, `test-images`, `test-pg`,
    `headscale`), in addition to `gates`;
  - each tarball on a clean runner of its platform (`tarballs`,
    `scripts/ci-tarball-check.sh`);
  - the Firecracker tier on the shipped amd64 guest (hosted KVM) and the
    shipped arm64 guest (Lima on the self-hosted Mac runner), and the vz tier
    on the shipped vz guest: `e2e-vm`, `e2e-container`,
    `e2e-container-build` → `e2e-container-registry`, `e2e-vz`,
    `e2e-vz-container`;
  - the real-tailnet suites, serialized: `e2e-real`, `e2e-oauth`,
    `e2e-public`, `e2e-svc`, `e2e-svc-caps`, `e2e-mm-svc`,
    `e2e-coldid-real` (hosted), then `e2e-linux` (all 7 distro rows, from
    the tarballs), `e2e-linux-vm`, `e2e-linux-reboot`, `e2e-mac`,
    `e2e-multi` (self-hosted Mac).
  - `rehearsal=true` dispatch input: everything, no draft.
- `scripts/ci-suite.sh`: a suite that "passes" by printing SKIPPED or
  skipping a top-level test fails the release.
- `scripts/ci-lima-make.sh`: make targets inside the Lima dev VM.
- `scripts/e2e-linux-vm.sh`: `TW_E2E_TARBALL` picks the tarball to test.
- `docs/RELEASING.md`: the new jobs, rehearsal, "Release runner".

Repo settings (done 2026-11-23): `release` environment deploys from `v*`
tags and master only; fork PR workflows need approval for all outside
contributors; secrets `TW_E2E_TAILNET1`, `TW_E2E_TAILNET1_API_KEY`,
`TW_E2E_TAILNET2_API_KEY` in the `release` environment; a self-hosted runner
with label `tw-release` is registered.

## Tests

- `make lint`: ok.
- `go test ./docs/...`: ok.
- Workflow job graph checked (every job feeds `release`).
- Not run yet: the workflow itself. The `release` environment only deploys
  from master and `v*` tags, so the first run is a rehearsal on master after
  merge.

## Known gaps

- `e2e-mbp` (darwin tarball on a second, clean Mac over a real tailnet) is
  not in the release: the only second Mac is also a live tailworks host and
  the suite's cleanup stops every tailworks process there. The darwin
  tarball is validated hermetically on a clean hosted macOS runner.
- The amd64 tarball's microVM tier over a real tailnet: `e2e-linux-vm` is
  arm64 (Lima); amd64 microVM is tested from the shipped guest
  (`microvm-amd64`), not from an installed tarball.
- `e2e-regrate-real` is a benchmark (registrations/min), not a gate; left out.
- `e2e-vz-container` runs one image set per run (Rosetta amd64 when Rosetta
  is installed, else arm64).
