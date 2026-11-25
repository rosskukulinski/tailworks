---
kind: security
upgrade: [schema-expand, protocol]
---
Public ingress: a public name now has one owner per tailnet (red-team
H10). The first tenant to deploy `"public"` routes under a workload name
owns `https://<name>.<tailnet>.ts.net/` on that tailnet, across undeploys,
redeploys and kills, until it releases the name. Another tenant on the same
tailnet that declares `"public"` under that name is refused with
`409 public_name_owned`, at deploy, at `--dry-run`, and when its host asks
for the public node. Before this, a second tenant could take the name after
a kill and redeploy and receive the first tenant's public traffic, such as
signed webhooks.

New API: `GET /v1/tenants/{id}/public-names` lists a tenant's names, and
`DELETE /v1/tenants/{id}/public-names/{name}` releases one once nothing
serves it (`409 public_name_in_use` otherwise). Deleting a tenant releases
its names. New audit kinds: `public_name_claim`, `public_name_release`.

Operator action: none. The store migration adds the `public_names` table.
Names already served before the upgrade are claimed for the tenant serving
them the first time anyone deploys or recreates them, so the upgrade can't
hand a name to another tenant. If several tenants share a tailnet, give
them all the same `tailnet` name (its MagicDNS suffix), because ownership is
scoped by that name.
