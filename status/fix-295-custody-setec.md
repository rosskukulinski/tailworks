Last update: 2026-11-23 PT. Issue #295 item 2: `tw custody migrate` moves file custody to setec.

## What's built

- **`tw custody migrate --to setec --setec-url <url> [--dry-run]`**
  (`cmd/tw/custody.go`): for a `tw up` install with tailworks stopped. It
  maps every file custody entry to its name, refuses (`custody_unmapped`,
  nothing copied) if any can't be mapped, refuses a setec that holds a
  different value under one of the names, copies, reads every entry back
  and compares, backs up the state to
  `<state>/backups/<time>-before-custody-migrate`, then switches `tw.json`
  to `custody: setec`. File custody is left as it was. Running it again
  after an interruption is safe: entries already in setec with the same
  value are kept.
- **Name mapping and backfill** (`mgmt/custodymigrate.go`,
  `mgmt/sqlstore_custodyindex.go`): candidate names are every
  `custody_names` row, every name tenant purge derives from a tenant's rows
  (`tenantCustodyCandidates`, now a free function), for every tenant ID any
  row names (so also rows a pre-#107 tenant delete left), the shared ID
  secret from its `secret_choice` row, and the secrets of every workload
  that the audit trail records a `secret_put`/`secret_delete` for. A file
  maps to a name only if it decrypts under it (the name is the AEAD's
  additional data). `BackfillCustodyIndex` writes the derived tenant names
  into `custody_names`. Going forward every tenant write is already indexed
  (`indexedCustody`).

## Decisions

- **Which names can't be backfilled.** The audit-trail source matters:
  secrets on a workload that was never deployed have no row that names the
  workload. Without the audit lookup the test's two never-deployed
  workloads came back unmapped. What's left unmappable is an entry nothing
  in tailworks can read: an interrupted write (a lost mint, a claim
  loser's key whose delete didn't happen), data deleted before
  `custody_names` existed, or a never-deployed workload's secrets whose
  audit records were pruned before the index existed. The command refuses
  and lists the files. The operator moves them out of `custody/secrets`
  and runs it again. There's no flag that skips them.
- **The ID secret stays out of `custody_names`.** The index is per tenant
  and tenant purge deletes what it lists, so indexing `tw/mgmt/id-secret/…`
  under tenant "mgmt" would put it at risk. Its `secret_choice` row names
  it.
- **Name:** `tw custody migrate`, as the issue suggests. `tw-mgmt migrate`
  works on the store alone and doesn't know `tw.json`. This command has to
  switch `tw.json`.
- `tw up`'s refusal to switch custody now points at `tw custody migrate`
  when the target is setec.

## Tests

- `go test ./mgmt -run 'TestMigrateFileCustodyToSetec|TestDeleteTenantPurges|TestCustody'`: ok.
  `TestMigrateFileCustodyToSetec` covers two tenants with every kind of
  entry, the ID secret, and `custody_names` emptied. It checks that an
  orphan is refused and setec stays untouched, that a dry run writes
  nothing, the backfill, a setec conflict, the copy and read-back, that
  `LoadSharedIDSecret` works on setec, and an idempotent second run.
- `go test ./cmd/tw -run 'TestCustodyMigrateCLI|TestUpStoreFlags'`: ok. It
  covers the refusal leaving `tw.json` alone, moving the listed file aside,
  the switch and the backup, and that a later `tw up` keeps setec.
- `make lint`: ok. `make changes-check`: ok (cli-json fragment).
- `make test` (on mbp) and `make test-pg` (local Docker): see the PR.
