# Teams: several tenants on one tailnet

Several teams can share one tailworks install and one tailnet. Each team gets:

- its **own tenant**, with its own owner tag, OAuth client and name prefix,
  so neither team can mint the other's tags or collide with its names;
- a **`github.com/rosskukulinski/tailworks/cap/tenant-admin` grant** for that tenant, so the team can
  manage its own workloads, and nothing else: not other teams' tenants,
  not hosts, not releases or upgrades, and not its tenant's own limits.

The design is [ADR-043](design/adr/043-team-tenants.md), tracked in
issue #80.

If teams don't trust each other's tailnet admins, give each team its own
tailnet instead. A tailnet admin can grant themselves any tenant, or
change tag ownership.

## 1. Give every tenant on the tailnet its own owner tag

A tenant's **owner tag** owns its workload and object tags in the tailnet
policy's `tagOwners`, and its OAuth client is scoped to that tag. Tailscale
only lets a client mint tags its own tag owns, so team B's client can't
mint team A's tags, even if mgmt has a bug.

A tenant alone on its tailnet uses `tag:tw` and needs nothing new. Before
a second tenant joins the tailnet, move the existing tenant to its own
owner tag:

1. In the admin console, create an OAuth client with **Auth Keys (write)**
   scoped to the new tag, for example `tag:tw-t-ops`, and add the tag to
   `tagOwners` (owned by `autogroup:admin`).
2. Make that tag the owner of the tenant's workload tags in `tagOwners`,
   for example `"tag:tw-wl-api": ["autogroup:admin", "tag:tw-t-ops"]`.
3. On the tailworks host, run
   `tw tenant set <tenant> --owner-tag tag:tw-t-ops --oauth-client-file <the new client's file>`.

Existing nodes keep their tags; only who owns them changes. Hosts are
created with a tenant whose client can mint `tag:tw-host`, which is the
operator's tenant on `tag:tw`. Keep a separate operator tenant for that,
or add hosts before you move the operator tenant to an owner tag.

## 2. Add the team's tenant

With an OAuth client scoped to the team's own owner tag (the console only
offers tags already in `tagOwners`, so `tag:tw-t-payments` is there by then):

```sh
tw tenant add example.ts.net --oauth-client-file payments-oauth.env \
  --owner-tag tag:tw-t-payments --name-prefix pay
```

`tw tenant add` writes nothing to a Tailscale tailnet's policy: it checks
the owner tag is in `tagOwners` (when the client can read the policy) and
prints the line to add if not. It never adds owners to an existing tag.

- `--owner-tag` must differ from every other tenant's on the tailnet, and
  can't be one of tailworks' own tags (`tag:tw`, `tag:tw-mgmt`, `tag:tw-host`,
  `tag:tw-public`). Otherwise it fails with `409 shared_owner_tag` or
  `400 reserved_tag`.
- `--name-prefix` (up to 11 of a-z, 0-9 and inner hyphens) goes in front of
  every tailnet name tailworks generates for the tenant:
  - object and workload nodes: `pay-<object ID>`;
  - Services: `svc:pay-<name>`;
  - public URLs: `https://pay-<workload>.<tailnet>/`;
  - deploy endpoints: `pay-<workload>-<host>`.

  At most one tenant per tailnet may go without a prefix. Prefixes must
  differ, and neither may start with the other followed by a hyphen
  (`409 name_prefix_required`). A prefix is fixed once the tenant has
  deployed.
- The tenant's ID is `tn_<tailnet label>-<prefix>`. With several tenants on
  a tailnet, name one by ID: `--tenant tn_example-pay`. Naming the tailnet
  is `ambiguous_tenant`.

Each team's workload tags must be owned by its owner tag only:

```jsonc
"tagOwners": {
  "tag:tw-t-payments": ["autogroup:admin"],
  "tag:pay-api":        ["autogroup:admin", "tag:tw-t-payments"]
}
```

A tag that another team's owner tag also owns, or that the team's owner tag
doesn't own, fails the deploy with `400 tag_not_owned`. `tw policy` lists
these under `tagOwnerProblems`. Public routes need `tag:tw-public` owned
by each team's owner tag too; `tw policy` prints the line. Public nodes are
separate per workload, so sharing that tag is safe.

## 3. Grant the team its tenant

In the tailnet policy file, grant the team the capability on
`tag:tw-mgmt`, naming its tenants and what it may do there:

```jsonc
"grants": [
  {"src": ["group:payments"], "dst": ["tag:tw-mgmt"], "ip": ["tcp:443", "tcp:80"],
   "app": {"github.com/rosskukulinski/tailworks/cap/tenant-admin": [
     {"tenants": ["tn_example-pay"], "actions": ["read", "deploy", "kill", "secrets", "data"]}
   ]}}
]
```

`tenants` are tenant IDs (`tw tenant ls` as an admin). Both fields are
required. A value without `tenants` or `actions`, or with a wildcard, an
unknown action or an unknown field, grants nothing. `tw whoami` and
`GET /v1/whoami` (`invalidTenantGrants`) say why.

| Action | Lets the team |
|---|---|
| `read` | list and read its deployments, objects, logs, audit records, stores, queues, secrets' names, public routes, telemetry and notification settings; pull its images |
| `deploy` | deploy, roll back, promote previews, undeploy, change public routes, release public names, set telemetry and notifications; push its images |
| `kill` | kill its objects and set their TTL |
| `secrets` | set and delete secret values |
| `data` | read and write its data through mgmt: KV, D1, store journals, queue messages, and create or delete stores and queues |

**Each value is checked on its own.** A request is allowed only if one
single value names both the tenant and the action.
`{"tenants": ["a"], "actions": ["read"]}` plus
`{"tenants": ["b"], "actions": ["deploy"]}` doesn't allow deploying to `a`.

### MCP and approvals

A value can also carry:

- **`"mcp": {"tools": [...]}`**: admits the caller to tailworks' MCP server
  (`/mcp`) for that value's tenants.
  - It gets the read tools (`whoami`, `ls`, `logs`, `audit`) with `read`.
  - It gets the write and destructive tools (`deploy`, `undeploy`, `kill`)
    only if they're named in `tools` and their action is in `actions`.
  - The tools run as the caller, limited to the values that carry `mcp`.
    By contrast, a `github.com/rosskukulinski/tailworks/cap/mcp` holder's tools run as an admin.
- **`"approve": [<github.com/rosskukulinski/tailworks/cap/approve value>, ...]`**: makes the caller an
  approver for Workflow approvals in that value's tenants only, with the
  same fields as [approvals.md](approvals.md). A value with `approve`
  needs no `actions`. `actions` alone never make someone an approver.

```jsonc
{"tenants": ["tn_example-pay"], "actions": ["read", "kill"], "mcp": {"tools": ["kill"]},
 "approve": [{"workflows": ["refund"], "limits": {"amount": 500}}]}
```

## What a team sees

- `tw link` works for a tenant admin and prints the tenants and actions.
  `tw deploy`, `tw ls`, `tw logs`, `tw kill` and `tw audit` work in
  the granted tenants.
- Lists (`GET /v1/tenants`, `/v1/deployments`, `/v1/objects`,
  `/v1/audit`) show only the granted tenants, merged when there are
  several.
- Asking for a tenant it wasn't granted is `403 tenant_forbidden`, with the
  grant to add. Another tenant's deployment or object, by ID, is
  `404 not_found`, so its existence doesn't leak.
- The admin console (`/ui`) shows its tenants' deployments, objects and
  audit records, without the hosts, status and audit-chain panels.
- Admin-only routes are `403 tenant_forbidden`:
  - hosts (create, cordon, drain, delete);
  - creating, changing or deleting tenants, and purging all of a tenant's
    objects;
  - releases, upgrades and `GET /v1/status`;
  - container reservations, and pushing operator base images.

## Checks

`tw policy` and `tw deploy` read the tailnet policy and warn
(`accessWarnings`) about grants to `tag:tw-mgmt` that don't do what they
look like:

- **Values on `github.com/rosskukulinski/tailworks/cap/admin`.** They don't narrow it: the holder is a
  full admin of every tenant and host. Versions of tw-mgmt before this
  one also ignore those values. Use `github.com/rosskukulinski/tailworks/cap/tenant-admin`, which those
  versions don't know and refuse.
- **Invalid `github.com/rosskukulinski/tailworks/cap/tenant-admin` values,** and values naming tenant
  IDs that don't exist.
- **On a tailnet with several tenants, `github.com/rosskukulinski/tailworks/cap/mcp` and
  `github.com/rosskukulinski/tailworks/cap/approve` grants.** These apply to every tenant on the
  tailnet. Use the `mcp` and `approve` entries of a tenant-admin value to
  limit them to one team.

## Removing a team

`tw tenant rm <tenant> --force` (an admin) removes the team's tenant and
everything it owns. It kills the tenant's live objects (their devices go
too), then deletes:

- its custody entries: the data keys of its KV, D1 and R2 stores and its
  queues, its workloads' secrets and volume keys, its objects' auth keys,
  its notification and telemetry settings and its OBO roots. Deleting the
  data keys makes any copy of the sealed data unreadable;
- every row it owns in mgmt's store: the store journals, queue messages,
  objects, deployments and their bundles, leases, public names and the
  rest, and the tenant row last, in one transaction;
- its files on every host: each host removes `tenants/<id>/` (node state,
  Durable Object storage, logs) and the tenant's R2 blobs at its next
  heartbeat, audited `tenant_purge_host`. A host with an S3 R2 backend
  can't list the bucket, so it logs the `t/<tenant>/` prefix for the
  operator to delete.

The audit records stay. The `tenant_delete` record counts what was removed
(`removed`). If the delete fails part way (custody unreachable, say), the
tenant is still there; run the same command again to finish it.

## Admins

`github.com/rosskukulinski/tailworks/cap/admin` is unchanged: an admin can do everything, in every
tenant. Holding both capabilities makes a caller an admin; a tenant-admin
value never narrows an admin.

## Devices

tailworks deletes a device only by the node ID it recorded when it minted that
node for the tenant. It never sweeps the tailnet for "foreign" devices.
So one team's tenant never touches another team's devices, even though
the OAuth clients' device scopes are tailnet-wide.

## Headscale

On Headscale a tailnet keeps one tenant. A Headscale tenant can't have an
owner tag, so a second tenant on the same Headscale tailnet fails with
`409 shared_owner_tag`.
