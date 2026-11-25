Last update: 2026-11-24 PT. Issues #449 and #465: admin UI uses the CLI's vocabulary; host page says "Remove host" (force) and gains Cordon / Uncordon.

# Admin UI vocabulary and host cordon (issues #449, #465)

Branch `admin-ui-vocabulary`. Part of the simplify audit (#434).

## Built

- **#449, one vocabulary with the CLI.**
  - "Kind / name" is "Class / name" (objects table header in
    `layout.html` and `objects.html`); the deployment page's namespaces
    table says "Class" ("The classes of node …"). "Kind" is left for
    audit event kinds.
  - "Auth" / "Auth mode" / "Tenant auth modes" are "Mint mode" /
    "Tenant mint modes"; "Waiting to join" is "Mint queue" (overview and
    tenant pages). The mint headers carry a plain-words `title`.
  - `/ui/objects`' acting-for filter key is `human` (as on `/ui/audit`
    and in `GET /v1/audit`). `caller`, the old key, is still read when
    `human` is absent, so old links keep filtering; paging links write
    `human`. (The logs page's `caller` filter is a different thing, the
    request records' verified caller, and keeps its name.)
- **#465, host page.**
  - "Remove this host": the checkbox (form field still `name="drain"`,
    API still `?drain=true`) reads "Force (kill its objects and release
    its leases)"; the confirm and hint say removal moves nothing and point
    at `tw host drain <id>`. Default stays checked (unchanged behaviour).
  - New "Cordon" card in a live region (`data-live="cordon"`): state with
    `cordonedAt` / `cordonedBy`, a Cordon button
    (`PUT /v1/hosts/{id}/cordon`) or an Uncordon button
    (`DELETE /v1/hosts/{id}/cordon`). When `cordonedBy` starts with
    `upgrade:`, there is no Uncordon button, only a warning pointing at
    `tw upgrade status` (the run uncordons the host itself).
  - Code in `mgmt/ui/cordon.go` (handler, `Cordoned`,
    `CordonedByUpgrade`); one route line in `ui.go`, one field group on
    `host` in `views.go`.
  - `docs/ADMIN-UI.md`: host page description, mutation table rows, and
    the UI's line: its actions stop things and decide approvals;
    configuration, placement and moves stay in the CLI.
- #464 (the CLI's `tw host rm --drain` → `--force`) is a separate issue;
  this branch only changes the UI's wording, which already says "force".

## Tests

- `mgmt/ui/cordon_test.go` `TestHostCordon`: page states, both forms'
  calls and redirects, the upgrade-cordon warning.
- `TestUsesOnlyPublicAPI` drives the cordon and uncordon forms
  (`Content-Length` added to the transport headers: Go's client sends
  `Content-Length: 0` on a body-less PUT).
- `TestObjectsSearch`: `human` key kept in paging links; legacy `caller`
  still filters.

Commands run (Go 1.27.4):

- `go test ./mgmt/ui/`: ok
- `make lint`: ok
- `make changes-check`: ok (no upgrade-relevant paths changed)
- `make test`: every package ok except timing failures in `cmd/tw`
  (TestCLIOAuth, TestCLIEndToEnd), `mgmt` (TestWarmKeysRevokedOnCloseInParallel)
  and `hostd` (TestStoreReadDeadline, TestPythonQueues, TestLockTemplate,
  TestHostd, TestContainerFenceInheritance, TestCronTriggers): client
  timeouts with the machine at load average ~130 from parallel agents. All
  of them pass rerun alone (`go test -run … -count=1`). None touch `mgmt/ui`.
- After merging `origin/master` (no changes to `mgmt/ui` or
  `docs/ADMIN-UI.md`): `make lint` ok, `go test ./mgmt/ui/ ./docs/` ok.

No extra suites from AGENTS.md's table apply (UI templates and handlers
only; no store, API spec, leases or CLI change).
