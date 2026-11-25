# fix-158-warm-keys (issue #158, ledger H25)

Warm keys are preauthorized single-use join keys mgmt pre-mints and holds
in custody for up to 30 min. Before this branch, `tw tenant rm` revoked a
tenant's unused ones (#164) and `tw down --purge` did through the object
purge, but a plain `tw down` left them in custody until they expired, and
nothing showed how many there were.

## What changed

- `mgmt/warmcustody.go` (new): `warmKeysHeld` counts a tenant's pool rows
  (warm, being adopted, or expired with the revocation still to retry);
  `revokeWarmOnClose` drains every unused key (revoke at the API, delete
  the custody copy and row) and retries failed revocations.
- `mgmt.Config.RevokeWarmKeysOnClose`: `Close` calls it after the pool loop
  has stopped, so nothing refills behind it. tw-mgmt flag
  `-revoke-warm-keys-on-exit` (default off: replicas sharing a store would
  revoke each other's pool on restart). `tw up` sets it in both layouts
  (child tw-mgmt and the single-process embedded mgmt), so `tw down` (and
  any supervisor stop) revokes them.
- `GET /v1/tenants/{id}` has `warmKeys` (spec, `make api`). `tw status`
  prints `N warm keys in custody` on the mgmt line and per tenant, and
  `--json` has `warmKeys` (top level and `tenants[]`; cli schema updated).
  `tw status` fits better than `tw doctor`: doctor checks the machine, and
  works without mgmt running.
- Two leaks the new tests found under `-race`, both fixed in
  `mgmt/warmkeys.go` / `warmcustody.go`:
  - A refill cut off by shutdown: its CreateKey (or its undo DeleteKey)
    ran on the cancelled context, so a key the API had made stayed live
    there, unknown to mgmt. CreateKey and the undo now run on a context of
    their own (bounded), and Close waits for them.
  - A refill racing `tw tenant rm`: a pool key minted after the delete's
    drain stayed live at the API (the tenant row, and so its credential,
    was gone by the time expiry looked at it). The delete now marks the
    tenant first (`markWarmGone`); refills skip it, and one already in
    flight revokes its own key after its row commits.
- #222: `testEnv.logs` is now a `lockedBuffer` whose reads take the same
  lock as the logger's writes (the data race in `TestWarmKeyPool`).
- Docs: INSTALL.md tuning table and uninstall note, security-model.md.
  Ledger H25 set to fixed. Release note `changes/fix-158-warm-keys.md`.

## Tests

- `mgmt.TestWarmKeysRevokedOnClose` (tw down: every pool key revoked at the
  fake Tailscale API, none in custody, no row; without the flag, a replica
  restart keeps the pool). Fails with the Close call removed.
- `mgmt.TestWarmKeysRevokedOnTenantDelete` (tw tenant rm, same checks).
- `mgmt.TestWarmKeysCountedInTenant` (the `warmKeys` count).
- `cmd/tw.TestCLIEndToEnd` now runs `downRevokesWarmKeys`: the real
  binaries, `tw status --json` count, then `tw down` revokes at least that
  many keys at the fake API and mgmt's store holds no warm key afterwards.
- `make lint`: ok. `make changes-check`: ok.
- `go test -race ./mgmt -run 'TestWarmKey|TestWarmKeys|TestDeleteTenant|TestCustodyTenant' -count=8`: ok.
- `make test` on the mbp runner: ok. `make e2e-upgrade e2e-tw-upgrade` on mbp: ok.
- No mgmt store or migration change, so `make test-pg` doesn't apply.
