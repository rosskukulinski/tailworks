Last update: 2026-11-15 15:45 PT. Public-name ownership (red-team H10 / F7, issue #70): built, tests green.

# Public-name ownership (issue #70, red-team H10 / RT3-10, fix F7)

## What's built

- **The finding.** A public workload is served at
  `https://<workload>.<tailnet>.ts.net/`. mgmt never checked whether
  another tenant on the same tailnet already published that name. After a
  kill and redeploy, a second tenant could take over the hostname and
  receive the first tenant's webhooks.
- **The fix.** `mgmt/publicnames.go` keeps one owner per `(tailnet, name)`.
  The store side is `mgmt/sqlstore_publicnames.go`, and the migration is
  `mgmt/migrations/202611152337_public_names.sql` (expand only).
  - The first deploy that declares `"public"` claims the name. A deploy by
    another tenant is refused with `409 public_name_owned`, and so are a
    dry run (check only, nothing written) and a host's create of the
    `_public` node (`findPublicClass`).
  - The claim survives undeploys, redeploys without `"public"` and kills.
    It ends when the owner releases it with
    `DELETE /v1/tenants/{id}/public-names/{name}`, which returns
    `409 public_name_in_use` while the name is still served. Deleting the
    tenant also ends it.
  - `GET /v1/tenants/{id}/public-names` lists a tenant's names.
- **Scope.** The scope is `Tenant.Tailnet`, lower-cased and without a
  trailing dot, which is what the Funnel hostname resolves under. Tenants on
  different tailnets don't collide. A global scope was rejected: on a hosted
  mgmt it would let one customer block a name for every other tailnet.
- **Upgrade.** Names already served before the upgrade have no claim row.
  The first claim goes to the tenant serving the name: a live `_public`
  node first, then the earliest live public deployment. The claim is
  recorded with `claimedBy: legacy`.
- **Audit.** New kinds `public_name_claim` and `public_name_release`. A
  refusal is a `deny` record with code `public_name_owned`, and the owner is
  named only in its detail. The error message never names the other tenant.
- **Edits to hotspot files**, one call each: `service.go` (Deploy claims),
  `service_v1.go` (PreflightBundle checks), `public.go` (`findPublicClass`
  claims), and `sqlstore.go` (one line in DeleteTenant).
- **Docs.** `docs/public-ingress.md` § "Name ownership", two entries in
  `docs/errors.md`, the release-note fragment
  `changes/public-name-claim-issue-70.md`, and the spec (`make api`).

## Tests

- `go test ./mgmt/ -run 'TestRT3_H10|TestPublic' -count=1`: ok.
  - `TestRT3_H10_PublicNameSquat` covers the attack: B's deploy of A's name
    on the same tailnet, before and after A's node is killed and A
    undeploys. It also covers the dry run, a differently spelled tailnet
    (`T.ts.net.`), the release rules, and release on tenant delete. Without
    the fix (claim short-circuited), it fails with
    `POST /v1/deployments: got 202 map[], want 409 public_name_owned`.
  - `TestRT3_H10_PublicNameLegacyOwner` covers the backfill for names
    served before the upgrade.
- `make lint test`: see the PR.

## Gaps and next steps

- **Ledger.** `security/LEDGER.md` rows H10 and F7 still say open/queued.
  Flip them when this merges. They weren't edited here because they sit
  right next to H09/F6, which the parallel #69 branch edits.
- **No CLI yet.** There's no `twd` command for list/release. A follow-up
  could add `twd expose names` and `twd expose release <workload>`; that
  also touches `schema/cli.schema.json`.
- **Tailnet spelling.** Tenants that name the same tailnet differently
  (for example an org name for one and the MagicDNS suffix for the other)
  are not detected. The docs tell operators to use the MagicDNS suffix.
- **Failed deploys keep their claim.** The claim is made when the deploy
  is accepted, before placement, so a deploy that then fails with
  `no_host` keeps it. This is intended and documented.
