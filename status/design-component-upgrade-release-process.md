Last update: 2026-11-15 PT. Merged origin/master with Workflows (#34, ADR-040) and retrofitted it:
- `internal/version.WorkerdPin` is now 1.20261114.1.
- Feature `workflows`.
- A Workflows app counts as stateful: fenced, and not moved by a drain.
- `approvals.json` records carry a version.

Earlier 2026-11-15: built ADR-041: versions and skew, numbered migrations, feature-gated placement, cordon/drain, twd upgrade, release catalog, upgrade runs, release gates. make lint test and make e2e-upgrade pass.

## Built (2026-11-15)

Merged origin/master twice (observability, agent DX + MCP, R2, KV, D1, queues, locks) and retrofitted them. The ADR's "Phases and what is built" section lists what is and isn't built.

**Test results:**

- `make lint`: passes. That covers api-check (the spec's audit and role conventions), compat-check, gofmt, vet, `changes-check` and `check-secrets`.
- `make test`: passes. That covers the Go and JS suites.
- `make e2e-upgrade`: passes, in 25 s. It builds v0.1.0 from its tag in a temporary worktree and runs real twd-mgmt and twd-hostd processes on testcontrol.
  - DO storage survives: the counter goes 1→6 across the mgmt upgrade, the host upgrade, and a downgrade of mgmt and back.
  - The workload's node ID is unchanged after the host upgrade.
  - The store's migrations are applied, and v0.1.0's twd-mgmt still opens the migrated store.
  - A host stamped 9.0.0 is refused with `version_skew`.
  - Also run with `TWD_UPGRADE_TO_VERSION=0.2.0` (0.1.0 → 0.2.0) and with `TWD_UPGRADE_FROM=self`; both pass.
- `make install-test` (install.sh `.prev` and `--rollback`): passes.
- `make changes-check`: passes.
- New unit and API tests, all spec-validated:
  - `mgmt`: TestMigrationFiles, TestSchemaStatusAfterOpen, TestSchemaAheadAndDowngradeGuard, TestContractMigrationsWaitForOlderReplicas, TestRestoreFence, TestHostVersionSkew, TestFeatureGatedPlacementAndCordon (feature refusal, cordon, dry and real drain), TestUpgradeRun (plan, canary first, cordon during the step, pause/resume, operator-installed hosts, uncordon, done), TestReleaseArtifacts.
  - `internal/version`, `internal/features`, `internal/catalog`.
  - `hostd`: self-upgrade install and the tar escape checks; the helper and vz protocol ranges; guest hello; snapshot identity.
  - Format fixtures in Go and JS.

**Not built:** see the ADR. In short:

- The Headscale gate: twd has no Headscale profile yet.
- The root vmhelper updater (open question).
- A microVM upgrade gate in CI: it needs a KVM runner.
- Artifact attestations.
- `--release-source github`.
- A stale-skills check in doctor.
- A format version on queue `v8` bodies.
- Workload adoption across a hostd restart.

**Findings:**

- The D1 journal replay used to skip entries from a newer version, which loses writes silently. The R2 replay used to ignore unknown ops. Both now refuse.
- CLI test fakes that register hosts must report features (`features.Host()`), otherwise deploys with KV, D1 or R2 bindings fail with `feature_unavailable`. That is the intended behaviour.

Earlier 2026-11-15: resolved the three open questions (mgmt runs fleet upgrades, mgmt keeps a release catalog, gates cover both Headscale minors).

Earlier 2026-11-15: drafted ADR-041 (Proposed). Design only at that point.

# Component upgrades and the release process

The design is in `docs/design/adr/041-upgrades-and-releases.md`.

## Findings: the upgrade machinery on master (46ff0c3)

- **Versions.**
  - Only `cmd/twd` defines `main.version`. `release.sh`'s `-X main.version`
    does nothing for `twd-mgmt`, `twd-hostd`, `twd-netd` and `twd-vmhelper`.
    `twd-vz` has no version.
  - mgmt takes its version from a `-version` flag, which `twd up` fills in.
- **Version reports.** None:
  - `HostRegistration`, `HostStatus` and `Host` have no version field.
  - The `Replica` record has no version.
  - guestd's `hello` carries only a token.
- **Skew checks.**
  - hostd ↔ vmhelper: `helperapi.Version = 2`, strict equality
    (`hostd/vm/vm_linux.go`).
  - workerd: `twd doctor` checks the pin against a hand-kept copy in
    `cmd/twd/doctor.go`.
  - Nothing else checks skew.
- **mgmt schema.** `migrate()` is the only mechanism: `CREATE TABLE IF NOT
  EXISTS`, then `addColumns`, then `addIndexes`, run on every open, with a
  Postgres advisory lock. There is no version table and no downgrade guard.
- **Persisted formats.** These carry no version: `objects.json`, netd
  `object.json` and `twd.json`.
- **hostd restart.**
  - `Close()` stops every workload and VM.
  - On start, hostd re-registers and `syncAssignments` redeploys
    everything.
  - Nodes rejoin from `tailscaled.state` with no re-registration.
  - VMs can't outlive hostd: the helper or VMM connection closing kills them.
- **Missing verbs.** There is no `twd upgrade`, `upgrade --plan`,
  `host drain`, `host cordon` or `twd mgmt migrate`.
  - `host rm --drain` kills the host's objects and deletes the host.
- **`install.sh`** swaps `bin/` and `share/`. It does no restart, deletes the
  old copy, and doesn't touch the root-owned vmhelper.
- **Runtime vs bundle.** `platform.js` and `__twd_rt.js` are embedded in
  hostd, but `@twd/*` client code is frozen into bundles at deploy. Upgrading
  a host therefore changes the runtime under old bundles.
- **Tests.** No test installs release N−1 and upgrades from it, and none runs
  mixed versions. The closest are same-binary rolling restarts
  (`e2e/multimgmt` S1) and the legacy objects-table migration tests.

## Hazards from in-flight branches (snapshot 2026-11-15)

1. The R2 index journal entries have no version field. Fix this before #16
   merges, because journals are permanent.
2. Queues (#18) adds tables by appending to the `migrate()` DDL line, and
   there is no schema version.
3. The workerd bump (1.20261108.1 → 1.20261114.1) sits inside
   `workflows-approvals`. Workflow instances use upstream's experimental
   on-disk format.
4. #20 and #25 define incompatible `twd logs --json` shapes, and #20 changes
   the default `twd logs` output.
5. Host-local state doesn't move with a lease: R2 `fs` blobs,
   `approvals.json`, request logs, DO storage.
6. Lock and fence tokens come from lease epochs, so a mgmt restore must never
   reuse an epoch.
7. do-split (PR #11) changes how DO IDs are derived.
8. New mgmt↔hostd calls (telemetry, queues, approvals, D1 store-admin) have
   no capability negotiation.

The ADR's "What in-flight branches must do before they merge" table gives the
fix for each hazard.

## Next steps

- Decisions taken on the first three open questions:
  - Fleet upgrades run in mgmt as durable upgrade runs; the CLI asks for a
    plan and controls the run.
  - mgmt keeps a release catalog (embedded, plus `twd release import`), so
    planning works offline.
  - The upgrade and skew gates run against both supported Headscale minors.
- Still open: whether the root-run vmhelper updater should exist, and how
  many imported releases mgmt keeps.
- Phase 0 as small PRs, landed before the feature branches:
  - `internal/version`;
  - version, protocol and feature reporting;
  - `schema_migrations` with a baseline;
  - format version fields;
  - `changes/` fragments and the lint check.
- Tell the branch owners about the pre-merge items, above all R2's `v` field,
  extracting the workerd bump, and the `logs --json` shape.
