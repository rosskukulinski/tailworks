Last update: 2026-11-16 PT. ADR-043 (was ADR-team-tenants) implemented and numbered; lint, test, test-pg, e2e-upgrade pass; PR opened.

## Steps 1b to 4 (2026-11-16)

Built:

- **1b** (`mgmt/tenantscope_mcp.go`, `tenantscope_approve.go`,
  `tenantscope_lint.go`):
  - A tenant-admin value's `mcp` entry admits `/mcp` for its tenants. Those
    tools run as the caller, not as an admin.
  - An `approve` entry makes the caller an approver in its tenants only.
    mgmt sends the host just that tenant's values, so hostd is unchanged.
  - The console opens to tenant admins, without the hosts, status and
    audit-chain panels.
  - Lists merge all of a tenant admin's tenants.
  - The deploy preflight reports `accessWarnings`: values on
    `twd.dev/cap/admin`, invalid or unknown tenant-admin values, and
    mcp/approve grants on a shared tailnet. There is no `twd policy lint`
    command; the ADR now says so.
- **Owner tags** (`mgmt/teams.go`; migration
  `202611161200_tenant_owner_tag_name_prefix.sql`):
  - A second tenant on a tailnet needs its own owner tag, refused with
    `409 shared_owner_tag` otherwise.
  - Snippets name the owner tag.
  - Deploy, spawn and preflight refuse tags that another team owns
    (`tag_not_owned` / `tagOwnerProblems`), and refuse owner tags
    themselves. The deploy checks only when the policy is readable.
  - `tag:twd-public` is exempt from the cross-team check.
- **Name prefixes:** mgmt prefixes node hostnames (not the IDs), public
  names, Services and deploy endpoints. At most one tenant per tailnet may
  go without a prefix, so an existing tenant never needs a rename.
- **D6 needed no code.** There is no device sweep (the agent survey found
  only per-node-ID deletes), and Tier-3 posture isn't implemented. The RFC
  threat model now says so.
- **CLI:**
  - `twd tenant add/set --owner-tag --name-prefix`;
  - `--tenant <shared tailnet>` is `ambiguous_tenant`;
  - `twd policy` and `twd deploy` print the access warnings.
- **Docs:** `docs/teams.md` rewritten as the full guide, four more codes in
  `docs/errors.md`, the RFC identity table and threat model, and the ADR
  rewritten to what was built.
- **Existing tests:** the H10 public-name squat tests now create their
  second same-tailnet tenant directly in the store
  (`legacySharedTenant`), since the API refuses one without owner tags.

Tests (2026-11-16, at 1b10f79):

- `make lint`: pass (changes-check needs `cli-json protocol schema-expand`,
  and the fragment has them).
- `make test`: pass.
- `make e2e-upgrade`: pass (`TestUpgradePreviousToCandidate`, which runs the
  new migration on the previous release's store).
- `go test ./mgmt/ -run 'TestTenant|TestTeams'`: pass.
- `make test-pg`: pass, once OrbStack was started (the first attempt found
  no Docker daemon).
- The team tests on Postgres: pass (18 tests). `test-pg` doesn't select
  them, so they were run by hand with `TWD_TEST_API_POSTGRES=1` against a
  throwaway `postgres:17-alpine`:
  `go test -race -p 1 -run 'TestTeams|TestTenant|TestRT3_H10' ./mgmt/`.

Not done:

- The ADR is numbered ADR-043 (Accepted) in the merging change (AGENTS.md
  rule 5), with the slug replaced everywhere.
- No real-tailnet test shows Tailscale refusing one team's client minting
  another team's tag (`make e2e-real` needs credentials).


## Step 1a: tenant admins (ADR-043 D2, D3)

Built:

- `mgmt/tenantscope.go`: `TenantAdminCap`, value parsing (`tenants` and
  `actions` required; wildcards, unknown actions and unknown fields grant
  nothing), per-value matching, `tenantGate` (called by `roleGate` when
  `roleAllowed` refuses a tenant admin), and the handler-side `tenantOK` /
  `tenantListScope`.
- `api/openapi.yaml`: `tenant:<action>` parts on 66 operations, the
  `tenant_forbidden` error code, and `tenantGrants` / `invalidTenantGrants`
  on `WhoAmI`. Regenerated with `make api`.
- `mgmt/v1.go`: checks in the handlers for deployments, objects, audit and
  tenants, whose tenant isn't in the path. Another tenant's resource by ID
  is a 404, and a named tenant that isn't granted is a 403.
- Registry: `mgmt/tenantscope_registry.go`. **Fixed an existing hole:** the
  registry admitted every `user` principal as an admin, so a tailnet user
  holding only `twd.dev/cap/approve` could push and pull every tenant's
  images. `Handler` now refuses non-admin users who aren't tenant admins.
- `internal/mgmtd/mgmtd.go`: WhoIs keeps the tenant-admin cap's values (it
  kept only the MCP and approve caps' values before).
- CLI: `twd link` accepts a tenant admin. `twd whoami` shows `tenantAdmin`
  and `invalidTenantAdmin` (`schema/cli.schema.json` updated).
- Docs: `docs/teams.md` (new), `docs/errors.md#tenant_forbidden`,
  `INSTALL.md`, `ADMIN-UI.md`, `security-model.md`, `docs/README.md`,
  `llms.txt`. Release note: `changes/explain-tailnet-multi-tenancy-team-workloads.md`.
- The ADR was updated to match what was built (a tenant admin is a plain
  non-admin `user`, not a new principal kind), and its plan step 1 is split
  into 1a (done) and 1b.

Tests (2026-11-16, after merging origin/master at a6a8974):

- `go test ./mgmt/ -run TestTenant`: pass (parsing, per-value matching,
  spec/route consistency, scope over HTTP, values never merged,
  invalid-only and operator callers, registry).
- `make lint`: pass (api-check, changes-check: needs `cli-json protocol`,
  found in the fragment).
- `make test`: pass.
- `make e2e-upgrade`: pass (`TestUpgradePreviousToCandidate`). It doesn't
  yet assert that an older mgmt refuses a tenant-admin-only caller. That
  holds by construction (older mgmts don't know the cap), but no test
  checks it.
- Not run: `make test-pg` (no store or migration change), and the suites
  that need a real tailnet.

Not built yet (step 1b, then steps 2 to 4 of the ADR):

- `/mcp` and `/ui` for tenant admins (both refuse them for now), the
  `approve` sub-value, and `twd policy lint` warnings.
- Owner tags, name prefixes and reconciler scoping. Until these land, two
  tenants on one tailnet aren't separated on the data plane; `docs/teams.md`
  says so.


## Revision: a new cap instead of values on `twd.dev/cap/admin`

`allowed` (`mgmt/server.go`) treats holding `twd.dev/cap/admin` as full admin
and ignores its values. If scope were a value on that cap, a mgmt that
predates the ADR would read a team-scoped grant as full operator admin,
hosts included. The draft now uses `twd.dev/cap/tenant-admin` (unknown to old
versions, so they refuse it) and keeps `twd.dev/cap/admin` operator-only.
MCP and approval scoping are sub-values of the tenant-admin cap, for the same
reason. Route scoping extends the existing `x-twd-role` (read at runtime by
`OperationRoles`, enforced by `spec_test.go`) with `tenant:<action>`, instead
of adding a new `x-twd-scope` field.

Who manages hosts, today and after the ADR: only admins (operators). Host,
pool, release and upgrade routes are `x-twd-role: admin`, and the ADR keeps
them closed to tenant-admins.

## First draft: several tenants per tailnet, one per team

Draft: `docs/design/adr/043-team-tenants.md`. The question was how two teams in one
tailnet each manage their own workloads.

What I found in the code, which the ADR builds on:

- mgmt's tailnet gate (`TailnetHandler`, `mgmt/server.go`) gives full admin to
  every caller holding `twd.dev/cap/admin` `[{}]`, across all tenants. The
  registry's user branch also admits any admin to any tenant's images
  (`mgmt/registry.go`).
- Trust credentials and generated `tagOwners` use `tag:twd` as the owner of
  every workload tag (`tagOwnersFor`, the deploy preflight). If two tenants
  shared a tailnet, either one could mint the other's tags.
- The `tenants.tailnet` column has no uniqueness constraint, and I found no
  code that refuses a second tenant on the same tailnet.
- Generated names (`<workload>-<host>`, `svc:<workload>`,
  `tag:twd-wl-<workload>`) would collide between tenants on one tailnet.
- The reconciler's "delete tagged devices twd didn't mint", with the
  tailnet-wide lifecycle credential, would delete another tenant's devices.

The ADR has an implementation plan in four PRs: the scoped admin cap, owner
tags, name prefixes, then docs. It's meant to be implemented later, after the
in-flight PRs land and master CI is stable.

Tests: none run. This is a docs-only change, and the ADR's own wording is the
only content.
