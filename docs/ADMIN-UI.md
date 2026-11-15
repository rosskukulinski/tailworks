# twd admin UI

twd-mgmt serves a small admin web UI next to its `/v1` API. It is rendered
by mgmt itself from Go `html/template` pages and two embedded static files
(one CSS, one vanilla-JS); there is no build step, no Node, no CDN, no
external font, and nothing is stored in the browser. The code is
`mgmt/ui`; it is mounted by `mgmt.Server.Handler` (`mgmt/v1.go`).

## URL and who gets in

    http(s)://twd-mgmt.<tailnet>.ts.net/ui/

Open it from any admin's browser over the tailnet. `GET /` from a browser
(`Accept: text/html`) redirects there; API clients hitting `/` still get
the typed `not_found`.

Admission is the connection's WhoIs identity, exactly as for the API
(`mgmt/server.go` `TailnetHandler`):

| principal | result |
|---|---|
| a user or tagged node holding `twd.dev/cap/admin` (granted to `tag:twd-mgmt`; `autogroup:admin` gets it from the base snippet), or a login in `-admins` | 200, the UI |
| mgmt's own node (the operator console on the host machine) | 200, the UI |
| a user or tagged node holding only `twd.dev/cap/approve` (toward `tag:twd-mgmt`) | the **approvals** page only, defaulting to the tenants on its own tailnet (`GET /v1/whoami` `tenants`; `?tenant=<tenant ID>` for another); 403 `forbidden` elsewhere |
| any other tailnet user | 403 `not_admin`, with the grant snippet to add |
| a `tag:twd-host` node (registered or not) | 403 `forbidden` (or `host_not_registered`); hosts never reach the UI or its assets |

There is no login, no cookie and no token: the tailnet connection is the
credential, so the UI shows exactly what the API would show the same
caller.

## Public API only

The UI is an ordinary client of the public `/v1` API, so it could be
moved into its own process later without changes. `ui.Handler` takes an
`*http.Client` and the API's base URL, and the UI:

- calls only operations, query parameters and headers documented in
  `api/openapi.yaml` (`docs/contracts/mgmt-api.md`), and reads only the
  documented JSON;
- never imports `mgmt`, and decides admission itself from
  `GET /v1/whoami` (`admin` and not `kind: host`). mgmt's own gate on
  `/ui/` (the table above) stays as a second layer;
- never sends the browser to the API: pages contain no `/v1` URLs, and
  mutations are form posts to the UI, which makes the `/v1` call
  server-side.

Embedded in mgmt, the client's transport (`mgmt/uiclient.go`) serves
each request with the API handler directly, without a socket, using the
browser request's context. Each call therefore carries the browser's
principal, and the role gate applies to it as to any other caller. A UI
running as its own process would use a network transport instead. How
that process would act on behalf of the browser's user is not decided
yet; it would need a delegation mechanism in the API.

`mgmt/ui/public_api_test.go` (`TestUsesOnlyPublicAPI`) enforces the first
and third points. It records every request the UI sends while rendering
every page and submitting every form, and checks each one against the
spec.

## Pages

- **`/ui/` overview**: summary tiles (hosts online, deployments, tenants,
  objects with pending device deletes, audit chain verification); then
  **deployments**, **hosts**, **tenants**, **objects** and **recent
  activity**, each with a one-line explanation; and a collapsed
  "Control plane details" panel for mgmt itself (DNS name, tailnet,
  version, listeners, transport, WIF issuer, the tenants' mint modes).
  The sections show: **hosts** (status live / pending / stale, heartbeat age,
  nodes live/capacity, tiers, pool, push transport, leases, node ID);
  **tenants** (tailnet, mint mode, quotas, registrations/min and the
  effective rate, mint queue, pool/tier); **deployments** (latest per
  workload: workload, deployment ID, tenant, host, deployed by, age,
  status, endpoint MagicDNS link and status); **objects** as a lineage tree
  across every tenant (indented by depth; status badge; node name linking
  to the object page; MagicDNS link; tags; TTL; short node ID; acting-for;
  age; killed and failed rows dimmed); the **25 most recent audit
  records** with `twd audit`'s descriptions.
- **`/ui/objects/{id}`**: every field of the object, the lineage up (root
  to parent), the subtree below (this object and its descendants), caps,
  the node certificate when the object has a node, the object's audit
  records (newest first) with chain status, and the **Kill** form (cascade checkbox,
  reason). A killed object renders its tombstone / kill record (the
  `/v1` 410 body: killed at, by, reason, cascade root, device deletion)
  and offers no kill form.
- **`/ui/deployments/{id}`**: the deployment, its endpoint, namespaces
  (class, tags, agent, ttl), the objects of the workload, and
  **Undeploy** (reason) unless already stopped or superseded.
- **`/ui/hosts/{id}`**: heartbeat, nodes, tiers, pool, push transport,
  node, the leases it holds, its assignments, the live objects it runs, and
  **Drain & remove** (drain checked by default; without it the API refuses
  with `host_holds_leases` while leases are held). This is the scale-out
  view: every host registered with this mgmt, live or not.
- **`/ui/tenants/{id}`**: the configuration as the API returns it (mint
  mode, client IDs and file paths, quotas, OBO public key; secrets are
  never returned by the API and so never shown), object counts by status,
  deployments, objects, and **Delete tenant** (force checkbox).
- **`/ui/objects`**: every object, newest first, 50 a page (Newer / Older
  links). Filters: tenant, app (workload) and status go to
  `GET /v1/objects` (`tenant`, `workload`, `state`); tag and acting-for
  are filtered by the UI, over the first 5000 matches (the page says when
  that limit was reached).
- **`/ui/approvals`**: the Workflow approvals waiting, oldest first (the
  longest wait on top). By default: every tenant for an admin; for anyone
  else the tenants on the caller's own tailnet, from `GET /v1/whoami`
  (`tenants`), so an approver never needs a tenant ID. `?tenant=` picks
  one. The tenant box is shown only when there is not exactly one tenant
  to choose from. Each row shows workload / Workflow, the instance, step,
  host, the instance's numeric params as a definition list, age and
  status, and has an anchor (`id="<instance>"`, a `#` link): the deep link
  of an `approval_requested` notification is
  `/ui/approvals?tenant=<tenant ID>#<instance>` and highlights the row.
  "What you may decide" lists the caller's `twd.dev/cap/approve` grants
  (`approveGrants`). `all` adds the decided ones with who decided. Each
  waiting row has **Approve** and **Reject** buttons sharing one comment.
  The decision is made with the browser's own tailnet identity and checked
  against its `twd.dev/cap/approve` grants by the host running the
  Workflow; an admin without a grant gets `cap_missing`, and the operator
  console `no_identity` ([approvals](approvals.md)). Approvers who are not
  admins don't see the Overview, Objects and Audit links.
- **`/ui/audit`**: newest first (`GET /v1/audit?order=desc`), with
  `nextCursor` paging (Older →, ← Newest) that keeps the filters. Filters
  combine: tenant, event kind, caller (the `human` the records act for),
  object and trace go to the API; app (workload) is filtered by the UI
  within each page, which says how many records it hid. Page size; the
  verification report and the per-chain status (`ok`, `signature ok` for
  host chains, `BROKEN at <seq>`). Tenant-written notes are rendered as
  `~tenant_note` and labelled unverified (RT-02).

Every activity list (overview, object page, audit) is newest first; the
approvals list is oldest first because the oldest wait is the most urgent.

Vocabulary and status colours follow the CLI: ready / live / running
green, pending / minted / stale amber, killed / stopped / superseded grey,
failed red. IDs are monospace; the full ID is in the cell's tooltip when
abbreviated. Light and dark follow `prefers-color-scheme`.

## Mutations

Each action is a plain HTML form that POSTs to the UI (for example
`/ui/objects/{id}/kill`). With JavaScript, `app.js` asks for confirmation
first. The UI then makes the same `/v1` call the CLI makes and redirects
back to the page with the outcome as a banner. A refusal shows
`code: message`.

| form | call |
|---|---|
| Kill | `DELETE /v1/objects/{id}?cascade=true&reason=…` (202) |
| Undeploy | `DELETE /v1/deployments/{id}?reason=…` (202) |
| Drain & remove | `DELETE /v1/hosts/{id}?drain=true` (202 / 204) |
| Delete tenant | `DELETE /v1/tenants/{id}?force=true` (202 / 204) |
| Approve / Reject | `POST /v1/tenants/{id}/workloads/{w}/approvals/{workflow}/{instance}` with JSON `{decision, comment}` (200) |

The actor recorded in the audit trail is the WhoIs login (tagged nodes:
tag and node), as for the CLI.

`app.js` also refreshes the page's live regions every 5 seconds by
re-fetching the page (the server's templates stay the single renderer).
A region is replaced only when its data changed: relative times
(`<time data-ago>`) are computed in the browser, so the passing of time
alone never counts as a change. A region with text selected in it is
left alone until the selection is cleared. Pages are complete and
readable with JavaScript off; only the live refresh and the confirmation
dialog need it.

## Browser-origin guard (CSRF, DNS rebinding)

The API has no cookies, so a classic CSRF cannot carry a credential; the
risk is a page on another origin (or a rebound DNS name) driving the
admin's tailnet connection. `mgmt.Server.Handler` wraps everything,
API and UI, in a guard (`browserGuard`, `mgmt/server.go`):

- Requests that are not browser-originated (no `Sec-Fetch-Mode`, no
  `Origin`) are untouched: the `twd` CLI and `twd-hostd` send neither and
  are never affected.
- `GET`, `HEAD` and `OPTIONS` are never guarded.
- A browser-originated mutation must have `Sec-Fetch-Site` in
  `{same-origin, none}` (when present) **and** an `Origin` whose host equals
  the request's `Host` (when present); `/v1` mutations must additionally
  carry `X-Twd-UI: 1`. A custom header forces a CORS preflight that mgmt
  never answers, so cross-origin JavaScript cannot reach the handler at
  all. The UI's own `/ui/...` form posts are covered by the
  `Sec-Fetch-Site` / `Origin` checks. The UI no longer sends `X-Twd-UI`;
  the rule stays for any same-origin script that opts in.
- Anything else is 403 `forbidden` with the message `cross-site request
  refused: …`. (The spec's `ErrorCode` enum is closed, so no separate
  `cross_site_request` code was added.)

Every UI response carries
`Content-Security-Policy: default-src 'self'; img-src 'self' data:; frame-ancestors 'none'`
(no inline script or style anywhere in the templates),
`X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`,
`X-Frame-Options: DENY` and `Cache-Control: no-store`. The UI stores nothing
client-side: no cookies, no localStorage, no tokens.

## Tests

- `mgmt/ui/*_test.go`: the fake `/v1` runs as a real HTTP server, so the
  UI reaches it only over HTTP. Covered: every page template over an
  empty and a populated fake (including a 410 tombstone, a broken chain
  and a hostile tenant note), the form posts, the UI's own admission via
  `/v1/whoami`, the public-API contract (`TestUsesOnlyPublicAPI`), the
  static assets and headers, and the tree and formatting helpers.
- `mgmt/ui_integration_test.go`: a real `Server` over the SQLite store with
  a fake WhoIs: admission per principal, the `/` redirect, the browser
  guard (cross-site refused, same-origin with `X-Twd-UI` and CLI-style
  requests succeed), the tombstone page, and every `/v1` call the UI makes
  on every page and form passing the kin-openapi validator.
- `mgmt/approvals_test.go`: the approvals page per principal (approver,
  admin, no grant), and the Approve / Reject form forwarding the browser's
  identity to the host.
- `mgmt/whoami_tenants_test.go`: `whoami`'s `tailnet`, `tenants` and
  `approveGrants`, and the approvals page defaulting to them (no tenant
  box for one tenant; words for none). `mgmt/ui/search_test.go`: the
  objects and audit filters and paging, and the approvals page's anchors,
  facts list and tenant box.

    go test -race -count=1 ./mgmt/...
