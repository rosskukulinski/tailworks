Last update: 2026-11-25 13:00 PT. #493: contract rendered by audience; GET …/stores/r2/{name}/objects.

## What changed

- `api/render`: `## Route table` is now two tables, `### Operator API` and
  `### Host protocol`. Host protocol means `x-tw-role` is exactly `host`, which
  covers 11 operations. Their per-tag headings end in "(host protocol)".
  `docs/README.md` links `mgmt-api.md#operator-api`.
- Role strings in `api/openapi.yaml` use one order:
  `admin|host|approver|mcp|tenant:<action>`. Seven strings changed from
  `host|admin…`. This is cosmetic, because `roleAllowed` splits on `|`.
- New route `GET /v1/tenants/{id}/workloads/{workload}/stores/r2/{name}/objects`,
  role `admin|tenant:data`. It uses the shared `limit`/`cursor` paging and
  takes `prefix`. It is implemented in `mgmt/r2.go`, which folds the
  bucket's index journal over `ReadStore` pages. The fold checks the
  incarnation as `kvFold` does, and holds only keys inside the
  prefix/cursor window. The decoder is the new `internal/r2wire` package.
  It skips mpu, part and mpx entries. A newer `v` or an unknown op is an
  error and becomes `409 version_skew`. Its tests replay
  `hostd/gen/testdata/formats/r2-index-v{0,1}.jsonl`, which
  `TestJSFormatFixtures` replays with store-r2.js itself.
- `tw r2 object ls` uses the new route and no longer has its own fold.
  `unknown_store` still prints "no objects". `not_found` means an older
  tw-mgmt; the CLI returns that error with a fix line telling the user to
  upgrade tw-mgmt.
- ADR-036 §8 is amended to supersede "no new mgmt endpoint". ADR-038 §5
  gains one sentence on why `/d1/{name}/query|export` stay where they are.
  `docs/r2.md` and the `version_skew` entry in `docs/errors.md` are updated.
- `changes/api-audience-r2-list.md`: kind `feature`, upgrade `[protocol]`.

This branch includes `hostd-api-doc` (PR #643), because the renderer's
host-protocol intro links `hostd-api.md`.

## Tests

- `go test ./internal/r2wire ./mgmt -run 'TestR2Objects|TestV1RoleMatrix|TestKVAdmin'`
  and `go test ./cmd/tw -run TestCLIR2`: pass.
- `make lint`: pass.
- `make test` (mbp): pass.
- `make e2e-upgrade`: pass. `make e2e-placement`: pass, including
  TestR2MoveBetweenHosts.
- After merging origin/master again (deprecated GetObjectCertificate, admin UI filters): `make lint` pass; `go test ./mgmt/... ./internal/r2wire ./api/...` pass.
