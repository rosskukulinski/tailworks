# ADR-043: Several tenants per tailnet, one per team: a tenant-admin capability, a per-tenant owner tag, and per-tenant name prefixes

**Date:** 2026-11-15 (implemented 2026-11-16)
**Status:** Accepted (Ross). Implemented (results in
`status/explain-tailnet-multi-tenancy-team-workloads.md`); issue #80. User
guide: [docs/teams.md](../../teams.md).

**Context.**

- The question: how can two teams in one tailnet each manage their own
  workloads, without being able to deploy, kill, read or impersonate the
  other team's?
- In the RFC (§4.3, identity table), a tenant *was* a tailnet. In code it's
  a row with a `tailnet` column (`mgmt/sqlstore.go`). Nothing enforced one
  tenant per tailnet, and nothing was designed for more than one either.
- Everything that isolates tenants is already per tenant:
  - `netd[tenant]` and `stord[tenant]` on each host;
  - the bucket prefix, the OBO root key and the OAuth client (ADR-042);
  - the namespace leases, the quotas and the pool (ADR-020).

  By default, plain DO classes get `isolation: "tenant"`, which means one
  VM per tenant.
- mgmt access was all or nothing. `TailnetHandler` (`mgmt/server.go`)
  admitted a tailnet user only as:
  - an admin (`github.com/rosskukulinski/tailworks/cap/admin` from a grant to `tag:tw-mgmt`, or
    `autogroup:admin`);
  - an approver;
  - an MCP caller.

  An admin can act on every tenant, every host and every release. The
  registry's user branch admitted every `user` principal as if it were an
  admin, an approver included.
- **Tag ownership wasn't separated either.** Every tenant's OAuth client is
  scoped to `tag:tw`, and tailworks' generated `tagOwners` make `tag:tw` the
  owner of every tag a workload asks for (`tagOwnersFor`, the deploy
  preflight). If two tenants shared a tailnet, either one's client could
  mint the other's tags, and so could join a node that the other tenant's
  grants trust.
- **Names collided.** mgmt names every node after its object ID
  (`<class or workload>-<hash10>`, `objectID` in `mgmt/service.go`), and
  names a public node after its workload. Services are `svc:<workload>`, or
  a name from `tailworks.jsonc`. Two teams that each deploy a workload `api`
  would get the same Service and public name.
- The RFC's threat model said a reconciler "deletes tagged devices that tailworks
  didn't mint". With two tenants on one tailnet and a tailnet-wide device
  scope (TS-5), one tenant's sweep would find the other's devices.
- Scoped app capabilities are already an established pattern:
  `github.com/rosskukulinski/tailworks/cap/mcp` values name tools (ADR-035), and `github.com/rosskukulinski/tailworks/cap/approve`
  values narrow by workload, workflow and step, failing closed (ADR-040).

**Options considered.**

1. **One tenant shared by both teams, with namespaces or projects inside
   it.** This is the lightest change to the data model, but the teams would
   share a VM under `isolation: "tenant"` (the RFC's V8-escape residual
   risk), a stord, a mint credential, an OBO root and a quota. Every
   per-tenant boundary would have to be rebuilt one level down. Rejected.
2. **One tailnet per team (what already works).** This is the strongest
   boundary and needs no change. It doesn't fit teams that need to share a
   tailnet so they can reach each other's services and the same databases.
   It stays the recommended answer when teams don't trust each other's
   tailnet admins.
3. **Roles stored in mgmt's database (RBAC tables).** This duplicates the
   tailnet policy, and it contradicts the RFC's "grants are the source of
   truth". Rejected.
4. **Several tenants in one tailnet, one per team, with access granted by
   a new tenant-scoped capability.** This reuses every per-tenant boundary,
   and authorization stays in the tailnet policy. **Chosen.**
5. **The same, but scoped by adding values to `github.com/rosskukulinski/tailworks/cap/admin`.**
   Rejected: mgmt ignores that cap's values, so on any older version a
   scoped grant would mean full admin (D2).

**Decision.**

1. **A tenant is a trust domain, not a tailnet.** A tailnet may hold several
   tenants, one per team. The RFC identity table says so now. A tenant alone
   on its tailnet changes in no way (D7).

2. **Team access is a new capability, `github.com/rosskukulinski/tailworks/cap/tenant-admin`. The
   existing `github.com/rosskukulinski/tailworks/cap/admin` stays operator-only.**
   (`mgmt/tenantscope.go`.)

   ```jsonc
   {"src": ["group:payments"], "dst": ["tag:tw-mgmt"], "ip": ["tcp:443"],
    "app": {"github.com/rosskukulinski/tailworks/cap/tenant-admin": [
      {"tenants": ["tn_example-pay"], "actions": ["read", "deploy", "kill"],
       "mcp": {"tools": ["kill"]}, "approve": [{"workflows": ["refund"]}]}
    ]}}
   ```

   - **Why a new capability.** The gate (`allowed` in `mgmt/server.go`)
     treats *holding* `github.com/rosskukulinski/tailworks/cap/admin` as full admin and ignores its
     values. A mgmt that predates this ADR would read a team-scoped value on
     that cap as full operator admin. That happens when a grant is written
     before an upgrade, or on old replicas during a rolling upgrade
     (ADR-041). A capability that old versions don't know is refused by
     them. The rule generalizes: **never narrow an existing capability by
     adding a field to its values; older readers ignore the field and widen
     the access.** The deploy preflight warns about values on
     `github.com/rosskukulinski/tailworks/cap/admin` (D3).
   - **Required values.** `tenants` is required and holds tenant IDs, with
     no wildcard. `actions` is required unless the value carries `approve`.
     The actions are:
     - `read`;
     - `deploy` (also undeploy, rollback, promote, public routes, telemetry
       and notifications);
     - `kill` (also TTL);
     - `secrets`;
     - `data` (KV, D1, store journals, queues).

     A value that is missing a required field, or that names an unknown
     action, field or MCP tool, grants nothing. `GET /v1/whoami`
     (`invalidTenantGrants`) and `tw whoami` say why.
   - **One value at a time.** A request is allowed only if one single value
     permits both the tenant and the action. Values are never merged; this
     is the same per-value matching as `internal/approvecap`.
   - **Operators only.** These routes have no `tenant:` part in their role,
     so a tenant admin can't reach them:
     - hosts;
     - creating, changing or deleting tenants (limits, pool, tier,
       credentials, owner tag);
     - purging all of a tenant's objects;
     - releases, upgrades and status;
     - container reservations, device-delete runs, and pushing operator
       base images.
   - **Kind of principal.** A caller that holds the capability but not
     `github.com/rosskukulinski/tailworks/cap/admin` stays a plain `user` principal that isn't an admin;
     its values travel in `CapValues`. Every existing "is this user an
     admin?" check still says no. A caller that holds both is an operator.
     A holder whose values are all invalid is admitted only to `whoami`.
   - **`mcp` entry** (`mgmt/tenantscope_mcp.go`). It admits `/mcp` for the
     value's tenants:
     - the read tools need `read`;
     - `deploy`, `undeploy` and `kill` must be named in `tools`, with their
       action in the same value's `actions`;
     - `status` is never offered.

     Unlike a `github.com/rosskukulinski/tailworks/cap/mcp` holder's tools, which run as an admin
     (ADR-035), these run as the caller, limited to the values that carry
     `mcp`. So the `/v1` gate decides every call they make.
   - **`approve` entry** (`mgmt/tenantscope_approve.go`). Its values have
     the same fields as `github.com/rosskukulinski/tailworks/cap/approve` and apply only to approvals in
     the value's tenants. mgmt sends the host only the values that apply to
     the tenant being decided, so hostd's check is unchanged, and a host
     from before this ADR sees only what applies. `actions` never make
     anyone an approver.
   - The existing `github.com/rosskukulinski/tailworks/cap/mcp` and `github.com/rosskukulinski/tailworks/cap/approve` keep their
     meaning, which is across all tenants. On a shared tailnet the
     preflight warns about them (D3).
   - **The admin console** (`/ui`) opens to tenant admins. It shows their
     tenants, and leaves out the hosts, status and audit-chain panels.

3. **The spec says which role and action every operation needs, in the
   existing `x-tw-role` field.**
   - **The role part.** A `tenant:<action>` part (`admin|tenant:deploy`)
     opens an operation to tenant admins. An operation without one is closed
     to them, so a new route is operator-only until someone decides
     otherwise (fail closed, ADR-027).
   - **The gate.** When `roleAllowed` refuses a tenant admin, `roleGate`
     hands the request to `tenantGate`. For routes under `/v1/tenants/{id}`,
     it checks the tenant in the path. Naming a tenant you weren't granted
     is `403 tenant_forbidden`, decided before any lookup, so it says
     nothing about whether the tenant exists.
   - **Routes whose tenant isn't in the path** must be listed in
     `tenantCheckedInHandler`, or the gate refuses them. Their handlers
     check the tenant once they know it:
     - A tenant named in the request (the body of `POST /v1/deployments`,
       or `?tenant=`) is a `403` if it isn't granted.
     - A resource looked up by ID in another tenant is a `404`.
     - Lists (`/v1/tenants`, `/v1/deployments`, `/v1/objects`,
       `/v1/audit`) keep to the granted tenants, merged when there are
       several.
     - Host audit-chain statuses aren't shown to tenant admins.
   - Two read-only routes (operator base images and their aliases) need
     only `read` in any tenant.
   - **The registry** (`/v2/`, `mgmt/tenantscope_registry.go`). A tenant
     admin pulls its tenants' namespaces with `read`, pushes them with
     `deploy`, and pulls `public/…` but never pushes it. Approvers no longer
     reach the registry.
   - **The consistency test.** `TestTenantRolesConsistent` keeps the spec
     and the handler lists consistent. Every operation under
     `/v1/tenants/{id}` is either tenant-scoped or on an explicit list of
     admin-only routes.
   - **Access warnings.** The deploy preflight (`tw policy`, `tw deploy`)
     reads the tenant's policy anyway. It now also reports `accessWarnings`
     (`mgmt/tenantscope_lint.go`); they never fail a deploy:
     - values on `github.com/rosskukulinski/tailworks/cap/admin`;
     - invalid tenant-admin values, and values naming unknown tenant IDs;
     - on a shared tailnet, `github.com/rosskukulinski/tailworks/cap/mcp` and `github.com/rosskukulinski/tailworks/cap/approve`
       grants.

     A separate `tw policy lint` is not built.

4. **Each tenant has its own owner tag, and its OAuth client is scoped to
   it** (`mgmt/teams.go`, column `tenants.owner_tag`).
   - **Default.** `""` means `tag:tw`, as before. `tw tenant add
     --owner-tag` and `tw tenant set --owner-tag` set it. Generated
     `tagOwners` snippets, the spawn delegation snippet and the public
     Funnel snippet name it.
   - **The hard boundary.** Tailscale only lets a credential mint tags its
     own tag owns, so tenant B's client *can't* mint tenant A's tags, even
     if mgmt is wrong or compromised.
   - **A second tenant on a tailnet.** It is refused with
     `409 shared_owner_tag` when either tenant uses `tag:tw`, or when both
     use the same owner tag. The error names the `tw tenant set` command
     that moves the existing tenant.
   - **Which tags can be owner tags.** An owner tag can't be one of tailworks'
     own tags (`tag:tw`, `tag:tw-mgmt`, `tag:tw-host`, `tag:tw-issuer`,
     `tag:tw-public`). No workload may ask for any tenant's owner tag
     (`reserved_tag`).
   - **mgmt checks too, as a second layer.** For a tenant with its own owner
     tag, each tag a workload asks for must be owned by that tag, and by no
     other tenant's owner tag on the tailnet. Otherwise:
     - at deploy and spawn it is `400 tag_not_owned`;
     - in the preflight it is listed under `tagOwnerProblems`, and `ok` is
       false.

     A deploy checks only when the tenant's policy is readable; otherwise
     the OAuth client is the boundary. `tag:tw-public` is exempt from the
     "other tenant" half: every public node carries it, and each public
     node is its own node.
   - **Hosts** are minted with a tenant whose client can mint
     `tag:tw-host`, which is the operator's tenant on `tag:tw`.
   - **Headscale** keeps one tenant per tailnet: a Headscale tenant can't
     have an owner tag.

5. **Each tenant on a shared tailnet has its own name prefix**
   (`tenants.name_prefix`, `Tenant.Name`).
   - **Where it goes.** When set, it goes in front of every name mgmt
     generates for the tenant:
     - object and workload node hostnames: `<prefix>-<object ID>` (the ID
       itself doesn't change);
     - public node hostnames and public-name claims:
       `<prefix>-<workload>`;
     - Services: `svc:<prefix>-<name>`;
     - the predicted deploy endpoint.

     hostd adopts whatever hostname mgmt returns, so no host change is
     needed.
   - **Format.** A prefix is up to 11 of a-z, 0-9 and inner hyphens. An
     object ID is at most 51 characters, so a prefixed name stays a DNS
     label. Deploy refuses a workload whose prefixed name would be more
     than 63 characters.
   - **At most one tenant per tailnet may go without a prefix.** That way an
     existing tenant never has to be renamed to let a team in. Prefixes must
     differ, and neither may start with the other followed by a hyphen
     (`409 name_prefix_required`). A tenant without a prefix can't deploy a
     workload that starts with another tenant's prefix, and a prefix that an
     unprefixed tenant's workloads already use is refused.
   - **When it's set.** A prefix is fixed once the tenant has deployed
     (`immutable_field`).
   - **Tags aren't prefixed.** `tw init` still writes `tag:tw-wl-<name>`.
     Ownership (D4) decides who may use a tag, not its name, and the
     preflight prints the `tagOwners` line for the tenant's owner tag.
     `tw init` runs before `tw link` and doesn't know the tenant.
     (2026-11-24, #491: a `tailworks.jsonc` without `tailnet.tags` now
     gets the same `tag:tw-wl-<name>` from `tw deploy`, written into the
     bundled file, as a project with no `tailworks.jsonc` always has. mgmt
     checks it like a written tag, so the same rule holds: a team whose
     workload shares a name with another team's is refused (tag ownership) for
     that tag, not its grants.)
   - **The tenant ID.** `tw tenant add --name-prefix p` gives the tenant
     the ID `tn_<label>-p`. Naming a shared tailnet with `--tenant` is
     `ambiguous_tenant`, so name tenants by ID.

6. **Devices are deleted only by the node IDs tailworks recorded for the
   tenant.** No code sweeps the tailnet for devices tailworks "didn't mint", and
   Tier-3 posture kill isn't implemented. So one tenant never acts on
   another's devices, whatever the answer to TS-5. The RFC's threat model
   now says this. A future sweep must keep to the tenant's own minted node
   IDs.

7. **Moving an existing tenant.** Existing tenants keep owner tag `""`
   (`tag:tw`) and no prefix, and nothing about them changes.
   - Before a team joins its tailnet, the operator moves the existing
     tenant with `tw tenant set <t> --owner-tag tag:tw-t-<team>
     --oauth-client-file <client scoped to it>`, after adding the new tag to
     `tagOwners`. Existing nodes keep their tags; only who owns them
     changes.
   - The existing tenant keeps no prefix (D5), so there is no node rename.

8. **Unchanged.** The data plane doesn't change. Cross-team calls go over
   the tailnet, governed by grants, and DO bindings and in-process RPC still
   work only within a tenant. One host may serve several tenants from the
   same tailnet, with one netd and one stord each.

**Consequences.**

- Two teams on one tailworks install and one tailnet each get their own tenant.
  Each manages its own workloads, secrets, data and approvals, and neither
  can deploy, kill, read or mint as the other. The operator keeps hosts,
  pools, quotas and releases.
- Authorization stays in the tailnet policy file. Audit records carry the
  WhoIs login.
- **Teams can't edit their own grants.** Only tailnet admins edit the
  policy file. Delegating it (for example GitOps with a code owner per
  team's section) is outside tailworks.
- **This boundary is only as strong as the tailnet's admins are
  trustworthy.** Teams that need a boundary against each other's tailnet
  admins should use separate tailnets (option 2).
- New error codes, all in `docs/errors.md`: `tenant_forbidden`,
  `tag_not_owned`, `shared_owner_tag`, `name_prefix_required`,
  `ambiguous_tenant` (CLI).
- `tenants.owner_tag` and `tenants.name_prefix` are added as an expand
  migration (ADR-041), `202611161200_tenant_owner_tag_name_prefix.sql`.
- **A registry fix ships with this.** A tailnet user holding only
  `github.com/rosskukulinski/tailworks/cap/approve` could push and pull every tenant's images.

**Open.**

- **Headscale:** owner tags need verifying against Headscale's tag ownership
  before a Headscale tailnet can hold several tenants.
- **Callers outside mgmt's tailnet** can't be identified by WhoIs. This ADR
  covers callers in mgmt's tailnet, which is the shared-tailnet case.
- **Teams creating their own tenants** (bounded by an operator-set pool)
  remain out of scope.
- **Whether `data` should be split** into read and write.

**What was built.** These are separate commits on the branch, each
following AGENTS.md (new files for the logic, small edits to the hotspots):

1. D2 and D3 for `/v1`, the registry and the CLI: `mgmt/tenantscope.go`,
   `mgmt/tenantscope_registry.go`, `tw link`, `tw whoami`.
2. D2 for `/mcp`, approvals and `/ui`, and D3's access warnings:
   `mgmt/tenantscope_mcp.go`, `mgmt/tenantscope_approve.go`,
   `mgmt/tenantscope_lint.go`.
3. D4, D5 and D7: `mgmt/teams.go`, the migration, and `tw tenant add/set
   --owner-tag --name-prefix`.
4. Docs: `docs/teams.md`, `docs/errors.md`, and the RFC's identity table
   and threat model.

Tests are in `mgmt/tenantscope_test.go` and `mgmt/teams_test.go`. No
real-tailnet test yet shows that one team's OAuth client is refused when it
mints another team's tag (`make e2e-real`, which needs credentials).
