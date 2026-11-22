Last update: 2026-11-21 PT. Design only (issue #38): ADR-platform-stores-off-mgmt (Proposed), options for taking mgmt off the platform-store write path. Not merged; for Ross to review.

# design-38-stores-off-mgmt

Issue: #38 (architecture review of ADR-044 / ADR-038). Ross's position: the
control plane should not be on the write path for tenant data at all.

## What this branch adds

- `docs/design/adr/platform-stores-off-mgmt.md`, `ADR-platform-stores-off-mgmt`
  (Proposed, no number per AGENTS.md rule 5). No code changes.
- Options sketched, each with write path, RPO, mgmt-down availability,
  fencing, custody, move/takeover, compaction, replay exactness, operator
  burden, workerd layout, migration and failure modes:
  A host-local + periodic snapshots; B fenced journal in an object store
  (barrier entries in a compare-and-append log, host-side sealing with a
  key released at the lease, snapshot compaction); C separate journal
  service with signed epoch tokens; D peer-host replication; E DO-storage
  WAL/page shipping; F row-level CDC; G per-binding durability classes.
- Recommendation: B for KV, D1 and R2 indexes; C as fallback if an object
  store can't be required for multi-host; E as a separate track for tenant
  DOs with an upstream hook; Queues stay in mgmt for now.

## Findings while reading the code

- mgmt seals and opens every entry itself (`mgmt/stores.go`), and admin KV
  reads fold plaintext in mgmt (`mgmt/kv.go`): mgmt's *database* holds only
  ciphertext, but the mgmt *process* sees tenant plaintext.
- `fenceStore` runs before, not inside, the append transaction. Safe
  because the conditional append keeps the journal linear; the lease check
  is access control, not what gives consistency.
- The platform `Store` class is not fenceable (`mgmt.BundleStateful` counts
  tenant classes only), so KV/D1/R2-only workloads keep serving through an
  mgmt outage; their stores are fenced only per call by mgmt.
- `docs:TestADRIndex` fails under `CI=true` while a slug ADR file exists, so
  this PR's CI is expected to fail that one test until the ADR is numbered
  at merge.

## Tests

- `make lint`: pass (exit 0).
- `go test ./docs/`: pass locally. With `CI=true`, `TestADRIndex` fails on
  the slug file, as designed (AGENTS.md rule 5); expected in this PR's CI.
- No `make test` or extra suites: no code changed.
