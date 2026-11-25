Last update: 2026-11-25. One readiness loop for deploy, promote and rollback (#525, second half).

`cmd/tw/deployready.go` has `readyWait`, the loop that polls
`GET /v1/deployments/{id}` until the primary is active with a ready node, or
fails. `cmdDeploy`, `cmdPromote` and `cmdRollback` each call it once instead
of carrying their own copy. The copies had drifted: only deploy's handled
`crashloop`, so a promote or rollback whose workerd kept exiting waited out
`--wait` and reported `identity_pending`.

Behaviour change: `tw promote` and `tw rollback` report `deploy_crashloop`
with the detail lines and the `tw logs` hint. Everything else (the status
event stream, deploy's `active` timing, the replica wait, the node
failed/killed and timeout paths) is unchanged.

The diagnosis and the test shape follow PR #617 by an outside contributor,
which was closed in favour of this in-house change.

Checks:

- `go test ./cmd/tw -run 'TestPromoteRollbackReportCrashloop'`: fails on
  the old code with `identity_pending`, passes with readyWait.
- `make lint`, `go test ./cmd/tw`, `make test`: see the PR's Tests section.
