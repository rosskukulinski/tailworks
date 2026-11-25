---
kind: security
upgrade: [protocol, cli-json, schema-expand]
---
Several teams on one tailnet (ADR-043, docs/teams.md).

- Tenant admins: grant a team `github.com/rosskukulinski/tailworks/cap/tenant-admin` on `tag:tw-mgmt`
  with values like `{"tenants": ["tn_example-pay"], "actions": ["read",
  "deploy", "kill"]}`. The actions are `read`, `deploy`, `kill`, `secrets`
  and `data`.
  - The team can `tw link`, deploy, list, read logs and audit, and kill in
    those tenants, through the CLI, the API, the registry and the admin
    console.
  - An `"mcp"` entry admits tailworks's MCP server for those tenants, and an
    `"approve"` entry makes the team an approver there only.
  - Hosts, tenant settings, releases and upgrades stay with admins.
  - Refusals are `403 tenant_forbidden` with the grant to add; another
    tenant's deployment or object is `404`.
- Owner tags and name prefixes: `tw tenant add <tailnet> --owner-tag
  tag:tw-t-<team> --name-prefix <p>` adds a second team's tenant to a
  tailnet. Each tenant on a shared tailnet must have its own owner tag
  (`409 shared_owner_tag`), and the OAuth client scoped to it can't mint
  another team's tags. Its tailnet names get the prefix (`<p>-…`,
  `svc:<p>-…`).
- `tw policy` and `tw deploy` warn about grants to `tag:tw-mgmt` that
  don't do what they look like (`accessWarnings`), and about tags another
  team owns (`tagOwnerProblems`).

Fix: a tailnet user holding only `github.com/rosskukulinski/tailworks/cap/approve` could push and
pull every tenant's images in mgmt's registry. The registry now admits only
admins, hosts and tenant admins.

Operator action: none for a tenant alone on its tailnet. To add a second
team's tenant to a tailnet, first move the existing tenant to its own owner
tag (`tw tenant set <tenant> --owner-tag … --oauth-client-file …`). Don't
scope a team by adding values to `github.com/rosskukulinski/tailworks/cap/admin`: older mgmts ignore
them and treat the holder as a full admin.
