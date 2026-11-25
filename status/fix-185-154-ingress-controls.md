Last update: 2026-11-21 PT. Control characters refused at public ingress and in new object names (#185); once-decoded path pinned (#154).

# fix-185-154-ingress-controls

## What changed

- **#185, public paths.** `publicspec.CanonicalPath` refuses a decoded path
  with a C0 control or DEL (so the raw `%00`..`%1f`, `%7f` forms too):
  `400 bad_request`, counted in the `public_rejected` summary with the other
  route refusals (same code path in `netd/public_serve.go`). Before, `%00`,
  `%1b` and `%0d%0a` reached the app.
- **#185, object names.** `termsafe.CheckName` (`internal/termsafe/input.go`)
  refuses C0 (TAB included), DEL, C1, bidi controls and marks, U+2028/2029
  and invalid UTF-8. Applied where a *new* name enters: hostd `ensure`
  (`hostd/objname.go`, one call in `objects.go` after the existing-object
  lookups), `admitCreate` and `admitPublic` (after the existing / killed /
  already-admitted checks, before public-create counting), and mgmt
  `CreateObject` (`mgmt/objname.go`, one call after the idempotent by-name
  lookup). New error code `bad_object_name` (400) in `api/openapi.yaml`
  (`make api`), `docs/errors.md`, hostd `errStatus`.
- **No backfill.** Existing objects with such names keep working: hostd
  finds them in `byKey` before the check, mgmt answers them idempotently
  before the check. Corner case: if such an object's workload moves to a
  new host, the new host's admit/ensure refuses to re-create it. Names
  with control characters come only from hostile or buggy code, so this is
  accepted; they render safely everywhere through `internal/termsafe`.
- **#154.** `netd.TestRT3_H21_OnceDecoded`: `/%252e%252e/x` reaches the app
  as `/%2e%2e/x` (also double-encoded `/`, `\`, NUL). One sentence in
  `docs/public-ingress.md`; the control-character refusal is listed there
  too.
- Ledger: H13 input half and F9 DONE, H21 fixed, run-log row.

## Tests

Written first and seen failing before the fix:
`netd.TestPublicPathTricksNeverReachPlatformPaths` (9 control paths got 200
and reached the app), `hostd.TestObjectNameControlsRefused` (16 creates
reached mgmt), `mgmt.TestCreateObjectRefusesControlNames` (202).
`netd.TestRT3_H21_OnceDecoded` passed before and after (it pins existing
behaviour).

Full runs (branch up to date with origin/master at addf27d):

- `make lint`: ok (api-check, changes-check `protocol`, staticcheck, vet).
- `make test` via `mbp-test.sh test` (mbp slot2): exit 0, no failures.
- `make test-pg`: not run. No store or migration change (`mgmt/service.go`
  gets one validation call before the insert; no SQL touched).
