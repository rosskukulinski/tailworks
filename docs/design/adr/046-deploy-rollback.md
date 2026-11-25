# ADR-046: Deployment history and rollback as a new deploy of a stored bundle; data moves forward only, secrets are not versioned

**Date:** 2026-11-15
**Status:** Accepted (2026-11-16, built on master after v0.2.0: `tw deployments`,
`tw rollback`; issue #45 items 1 and 2; results in
`status/deploy-history-rollback-issue-45.md`). Drafted as `ADR-deploy-rollback`.
Preview deploys (issue #45 item 3) are not part of this decision.

**Context.**

- `tw deploy` replaces the workload's running deployment. Every deploy
  already leaves its row in `deployments`, bundle included (`GET
  /v1/deployments` lists them; `tw ls` asks for `latest=true`), but:
  - nothing in the CLI lists a workload's past deployments;
  - nothing bounds the bundles mgmt keeps: every deploy adds up to 64 MiB
    to the store, forever;
  - the only way back to an earlier version is to check out the old
    source and deploy it again, which only works if it rebuilds exactly.
- Every comparable platform has a history and a rollback (wrangler
  `deployments list` and `rollback`, `vercel rollback`, `fly releases`,
  `kamal rollback`). On Cloudflare a rollback changes the Worker's code
  and configuration; Durable Object storage, D1, KV and R2 keep their
  data, and D1 migrations only move forward.
- A deploy in tailworks is more than "serve this code" (ADR-011, ADR-028): it
  takes the `(tenant, workload)` lease at `epoch+1`, and the host fences
  itself on that epoch, so an older instance of a stateful workload stops
  serving. Durable Object storage lives on the host's disk; moving a
  stateful workload needs `--force-takeover` (`move_unconfirmed`).
- Secrets (ADR-034) are one custody entry per workload, released to the
  lease holder at its epoch when the deployment starts. The store holds no
  value. Vars are in the bundle.
- D1 migrations (ADR-038) are applied explicitly (`tw d1 migrations
  apply`), never by a deploy.

**Decision.**

1. **History is the deployment rows.** No separate "versions" table. A
   migration (`202611152330_deploy_history`, expand only) adds four
   columns to `deployments`, on SQLite and Postgres alike:
   - `bundle_sha256`: recorded after each deploy, and again before a
     bundle is pruned, so the hash outlives the bytes. Rows from before the
     migration are hashed lazily while their bundle is still there.
   - `bundle_pruned_at`: when retention dropped the bundle (0 = kept).
   - `active_at`: the first time the assigned host reported the deployment
     `active`. This is what "it ran" means below. Heartbeats repeat
     `active`, so the store reads before it writes.
   - `rollback_of`: on a rollback, the deployment whose bundle it
     redeployed.

   `GET /v1/tenants/{id}/workloads/{workload}/deployments` lists them
   newest first, paged by `(createdAt, deploymentID)` descending: id,
   created, deployed by, bundle SHA-256, status, the host's last report
   (`hostStatus`), error, host, `bundleRetained`, `rollbackOf`, and the
   page's `bundleRetention`. `tw deployments <workload> [--json]` prints
   it.

2. **Retention: the newest N bundles per workload.** N is tw-mgmt
   `-bundle-retention` (default 10; `-1` keeps every bundle). After each
   deploy of a workload commits, mgmt drops the bundles of that workload's
   deployments beyond the newest N that still have one. A pruned row keeps
   its record and hash; only the bytes go. The current deployment is
   always among the newest, so hosts can always fetch their assignment's
   bundle. Rows are never deleted: they are the history, and they are
   small. Pruning is best-effort (a failure is logged; the next deploy
   tries again) and never fails the deploy.

   A count, not an age, because it bounds both storage (N × the bundle
   size per workload) and how far back a rollback can reach, in terms an
   operator can predict.

3. **A rollback is a new deployment of a stored bundle, through the deploy
   path.** `POST /v1/tenants/{id}/workloads/{workload}/rollbacks`
   (`tw rollback <workload> [<deployment>]`) loads the target's bundle and
   calls `Mgmt.Deploy` with it, as `deploy --replace` and `host drain`
   already redeploy stored bundles. So it gets everything a deploy gets,
   with no second copy of the logic:
   - bundle validation, placement, `lease_held`, `tier_unavailable`;
   - the lease at `epoch+1`, so the host running the current deployment
     is fenced exactly as for a deploy;
   - `move_unconfirmed` and `takeover_unconfirmed` for stateful workloads.

   It is not a pointer flip back to the old row: the old row's epoch is
   stale, and reusing it would undo the fencing. The new deployment
   records `rollback_of`.

   **Placement is sticky, as for any deploy.** The issue asked for "the
   same path as `deploy --replace`"; we read that as the same code path
   (`Deploy`), not as re-placing by default. Re-placing would move a
   stateful workload away from its Durable Object storage just because a
   better host appeared. `--replace`, `--host` and `--force-takeover` are
   there, and behave as on `tw deploy`.

   Images the original deploy built and pushed to the tenant's namespace
   of tailworks' registry are passed back as `containerImages`, so the
   rollback runs the same digests without a rebuild. External images are
   pinned again (their tags may have moved).

4. **Which deployment.** With an ID, that one, if it belongs to the
   workload, is not the current deployment and still has its bundle.
   Without one, the newest earlier deployment that:
   - ran (`active_at` set, or, for rows older than the migration, a last
     host report of `active` or `draining`),
   - did not end `failed`,
   - still has its bundle, and
   - has a bundle different from the current one's (so a rollback after a
     rollback goes further back instead of redeploying what runs).

   If the newest such deployment's bundle was pruned, the answer is
   `409 bundle_pruned`, not an older one: going further back than the
   operator expects would be worse than refusing. None at all is
   `409 no_rollback_target`. Both refusals happen before anything is
   written.

5. **Authorization, approvals and audit are deploy's.**
   - The route has deploy's role (`x-tw-role: admin`: a tailnet caller
     with `github.com/rosskukulinski/tailworks/cap/admin`, or the local socket), and so does the
     history read. Hosts get `403`.
   - Like `tw deploy`, the CLI first preflights the stored bundle against
     the **current** policy (`?dryRun=true`), since grants may have changed
     since it was deployed. `--allow-missing-grants` overrides it, as on
     deploy.
   - Deploys have no approval gate today. Because a rollback is a call to
     `Deploy`, a gate added there later applies to rollbacks too; a
     rollback cannot go around it.
   - The audit record is the deploy's own record, committed with the
     deployment row through the audit outbox (H22), with `kind: rollback`
     in place of `deploy` and `rollbackOf` in its detail. `rollback` is a
     privileged kind that tenants can't submit.
   - There is no MCP `rollback` tool yet. One would need its own
     `github.com/rosskukulinski/tailworks/cap/mcp` grant name, as `deploy` has.

6. **Durable Object storage moves forward only.** A rollback changes code
   and configuration, never data. With sticky placement the objects keep
   their storage on the same host and are started by the older code, which
   must read whatever the newer code wrote. If the older bundle no longer
   declares a class, that class's objects and their storage stay, but
   can't be reached or spawned (`unknown_namespace`) until a later deploy
   declares the class again. A rollback that moves a stateful workload is
   a move like any other (`--replace --force-takeover`), and its Durable
   Objects start with empty storage on the new host.

7. **D1 migrations move forward only**, as on Cloudflare. A rollback
   neither reverts nor re-applies migrations: the database keeps every
   migration that was applied, and the older code runs against the newer
   schema. The docs (`docs/d1.md`) say so and recommend expand/contract
   migrations: add before use, drop a release later. KV, R2 and queues
   keep their data too. A queue's messages written by the newer producer
   are consumed by the older consumer.

8. **Secrets are applied at deploy time and not versioned.** A rollback
   gets the workload's secrets as they are **now**. Vars are in the bundle,
   so they do roll back. Storing secret values with each version would
   multiply copies of the most sensitive data mgmt holds, and it would
   bring back a rotated, possibly compromised credential on rollback. That
   is the opposite of what an operator rotating a secret wants. The
   cost: if the newer version renamed a secret, the older code finds its
   old name missing, and the operator must put it back (`tw secret put`).
   `docs/secrets.md` says so.

**Consequences.**

- A bad deploy can be undone in one command, without the source, for as
  long as its predecessor's bundle is retained. `tw deployments` shows
  which ones are (`*` marks a pruned bundle).
- The store no longer grows without bound with bundles; it still keeps
  every deployment row.
- Rollback inherits every deploy guarantee, and every future deploy
  check, at no extra cost. It also inherits deploy's costs: a new epoch,
  a fresh start on the host, and the workload node re-joining as on any
  redeploy.
- Older code must tolerate newer data. That holds for Durable Object
  storage, D1, KV, R2 and queue messages alike; the platform doesn't
  version data.
- Identical bundles are stored once per deployment. A content-addressed
  bundle store would deduplicate them; not needed at this retention.

**Not built.**

- Preview deploys and promotion (issue #45 item 3) are a separate ADR: a
  preview must not take the Durable Object lease.
- An MCP `rollback` tool.
- Per-tenant or per-workload retention. Retention is one mgmt-wide
  setting.

**Tests.** Besides `mgmt:TestV1DeployHistoryRollback` and
`TestStoreDeploymentHistory`, the data-plane chaos loop's deploys
scenarios (`make e2e-dataplane-deploys`, nightly; `e2e/dataplane/LOOP.md`
"Deploys", issue #421) race deploys, rollbacks naming a target, previews
and promotions through three mgmt replicas under host, replica and
Postgres faults, and check that each deploy or rollback takes its own
epoch and fences the previous holder (V1, V2), that records are never
deleted or changed and a rollback redeploys the bundle the client named,
as its audit record says (V5: `TestStoreSetDeploymentMetaKeepsRollbackOf`,
#563), and that the newest deploy or rollback is what serves once faults
stop (V6).
