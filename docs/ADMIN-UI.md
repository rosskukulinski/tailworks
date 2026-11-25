# tailworks admin UI

tw-mgmt serves a small admin web UI next to its `/v1` API. It is rendered
by mgmt itself from Go `html/template` pages and two embedded static files
(one CSS, one vanilla-JS); there is no build step, no Node, no CDN, no
external font, and nothing is stored in the browser. The code is
`mgmt/ui`; it is mounted by `mgmt.Server.Handler` (`mgmt/v1.go`).

## URL and who gets in

    http(s)://tw-mgmt.<tailnet>.ts.net/ui/

Open it from any admin's browser over the tailnet. `GET /` from a browser
(`Accept: text/html`) redirects there; API clients hitting `/` still get
the typed `not_found`.

Admission is the connection's WhoIs identity, exactly as for the API
(`mgmt/server.go` `TailnetHandler`):

| principal | result |
|---|---|
| a user or tagged node holding `github.com/rosskukulinski/tailworks/cap/admin` (granted to `tag:tw-mgmt`; `autogroup:admin` gets it from the base snippet), or a login in `-admins`, or mgmt's own node (the operator console on the host machine, via the loopback proxy or the `-local-dev` socket), which is admitted as an admin without holding any grant | 200, the UI |
| a user or tagged node holding only `github.com/rosskukulinski/tailworks/cap/approve` (toward `tag:tw-mgmt`) | the **approvals** page only, defaulting to the tenants on its own tailnet (`GET /v1/whoami` `tenants`; `?tenant=<tenant ID>` for another); 403 `forbidden` elsewhere |
| a user or tagged node holding only `github.com/rosskukulinski/tailworks/cap/tenant-admin` ([teams.md](teams.md)) | 200, the UI for the tenants its grants name: their deployments, objects and audit records, without the hosts, status and audit-chain panels; admin-only actions are 403 `tenant_forbidden` |
| any other tailnet user | 403 `not_admin`, with the grant snippet to add (as an HTML page in a browser, see below) |
| a `tag:tw-host` node (registered or not) | 403 `forbidden` (or `host_not_registered`); hosts never reach the UI or its assets |

**Refusals in a browser.** Admission runs before the UI, so these refusals
come from mgmt itself, not from the UI's own error page. When a `GET` or
`HEAD` of `/`, `/ui` or `/ui/...` prefers `text/html` to JSON in its
`Accept` header (a browser's navigation; `*/*` alone or a tie stays JSON),
mgmt answers with a standalone HTML page instead of the JSON body
(`mgmt/errors_html.go`). It has the same status and `Tw-Error` header, the
code, the message, the grant snippet in a block (for `not_admin`), a fix
sentence and the `docs/errors.md` link. For `misdirected_request` it leaves
out the message (which echoes the request's `Host`) and lists mgmt's own
names instead. The page is self-contained (inline style, CSP
`default-src 'none'`, `no-store`, `nosniff`, no framing, no referrer).
`/v1`, `/mcp`, mutations and every client without an explicit `text/html`
preference (the CLI, hostd, curl) get the JSON body as before.

There is no login, no cookie and no token: the tailnet connection is the
credential, so the UI shows exactly what the API would show the same
caller.

**Guards you will notice.** Open the UI only at mgmt's own MagicDNS name
or tailnet IP: a CNAME, another DNS name you own, or a reverse proxy with
its own hostname gets 421
[`misdirected_request`](errors.md#misdirected_request). Actions must come
from the UI's own pages; a form posted from another origin gets 403
[`forbidden`](errors.md#forbidden) ("cross-site request refused"). The UI
stores nothing in the browser. It is an ordinary client of the public
API ([Public API only](#public-api-only)); the guards and their rationale
are in [Browser-origin guard](#browser-origin-guard-csrf-dns-rebinding).

## Pages

- **`/ui/` overview**: summary tiles (hosts online, deployments, tenants,
  objects with pending device deletes, and an audit tile only when the
  chain fails verification); then
  **deployments**, **hosts**, **tenants**, **objects** and **recent
  activity**, each with a one-line explanation; and a collapsed
  "Control plane details" panel for mgmt itself (DNS name, tailnet,
  version, listeners, transport, the tenants' mint modes).
  The sections show: **hosts** (status live / pending / stale, heartbeat age,
  nodes live/capacity, tiers, pool, push transport, leases, node ID);
  **tenants** (tailnet, mint mode, quotas, registrations/min and the
  effective rate, mint queue, pool/tier); **deployments** (latest per
  workload: workload, deployment ID, tenant, host, deployed by, age,
  status, endpoint MagicDNS link and status); **objects** as a lineage tree
  across every tenant (indented by depth; status badge; node name linking
  to the object page; MagicDNS link; tags; TTL; short node ID; acting-for;
  age; killed and failed rows dimmed); the **25 most recent audit
  records**: who did what, with `tw audit`'s descriptions.
- **`/ui/objects/{id}`**: every field of the object, the lineage up (root
  to parent), the subtree below (this object and its descendants), caps,
  the node certificate when the object has a node, the object's audit
  records (newest first; chain status only when it fails), a **Logs** link, and the
  **Kill** form (cascade checkbox, checked by default like `tw kill`; reason). A killed object renders its tombstone / kill record (the
  `/v1` 410 body: killed at, by, reason, cascade root, device deletion)
  and offers no kill form.
- **`/ui/deployments/{id}`**: the deployment, its endpoint, namespaces
  (class, tags, agent, ttl), the objects of the workload, a **Logs**
  link, and **Undeploy** (reason) unless already stopped or superseded.
- **`/ui/tenants/{id}/workloads/{workload}/logs`**: the workload's logs,
  read with `GET /v1/tenants/{id}/workloads/{workload}/logs` from the
  host (or hosts, for replicas) that runs it. Two tabs:
  - **Requests** (`source=requests`, the default): the structured request
    records (ADR-045), newest first. Each row has the time, method and
    path (or the invocation's event), status and outcome, duration, the
    node (linking to its logs), the verified caller and the trace (a link
    that filters to it, and one to the request's audit records); the
    code's `console.*` lines and exceptions (with the stack) sit under
    it, and error rows are marked. Filters go to the API, which applies
    them on the host: node (`object`), status (`404`, `5xx`), caller,
    trace and errors only.
  - **Console output** (`source=console`): the raw output (workerd's
    stdout, the Node runtime's `app.log`, the microVM console), oldest first,
    with terminal escapes removed. It is per workload, not per node.

  Both read the last 50, 100 (default), 200 or 500 records or lines; the
  page refreshes every 5 s like the others, so it follows the log. With
  replicas, each line or record names its host, and a host that did not
  answer is shown above the list. A workload that is not running, or has
  no log yet, is an empty list with the API's reason.
- **`/ui/objects/{id}/logs`**: a node's logs: its workload's request
  records filtered to this object (`object=<object ID>`), including a
  killed object's. The console tab links to the workload's console
  output.
- **`/ui/hosts/{id}`**: heartbeat, nodes, tiers, pool, push transport,
  node, the leases it holds, its assignments, the live objects it runs,
  **Cordon** / **Uncordon** with who cordoned it and when (`cordonedAt`,
  `cordonedBy`; a host an upgrade run cordoned, `cordonedBy` `upgrade:…`,
  shows a warning instead of Uncordon, since the run uncordons it), and
  **Remove host** (force checked by default: its objects are killed and
  its leases released; without it the API refuses with
  `host_holds_leases` while leases are held). Removing does not move
  anything: moving workloads off a host is `tw host drain` in the CLI.
  This is the scale-out view: every host registered with this mgmt, live
  or not.
- **`/ui/tenants/{id}`**: the configuration as the API returns it (mint
  mode, client IDs and file paths, quotas, OBO public key; secrets are
  never returned by the API and so never shown), object counts by status,
  deployments (each with a logs link), objects, and **Delete tenant** (force checkbox).
- **`/ui/objects`**: every object, newest first, 50 a page (Newer / Older
  links). Filters: tenant, app (workload), status, tag and acting-for go to
  `GET /v1/objects` (`tenant`, `workload`, `state`, `tag`, `human`). The tag
  may be given without its `tag:` prefix; acting-for is the exact login, as
  on the audit page. The status choices are the spec's `ObjectStatus`
  values. `/v1/objects` lists oldest first, so the page reads up to 5000
  matches to show the newest first, and says when it reached that limit.
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
  "What you may decide" lists the caller's `github.com/rosskukulinski/tailworks/cap/approve` grants
  (`approveGrants`). `all` adds the decided ones with who decided. Each
  waiting row has **Approve** and **Reject** buttons sharing one comment.
  The decision is made with the browser's own tailnet identity and checked
  against its `github.com/rosskukulinski/tailworks/cap/approve` grants by the host running the
  Workflow; an admin without a grant gets `cap_missing`, and the operator
  console `no_identity` ([approvals](approvals.md)). Approvers who are not
  admins don't see the Overview, Objects and Audit links.
- **`/ui/audit`**: newest first (`GET /v1/audit?order=desc`), with
  `nextCursor` paging (Older →, ← Newest) that keeps the filters. Filters
  combine and all go to the API: tenant, app (`workload`), event kind
  (suggested from the spec's `AuditKind` values), caller (the `human` the
  records act for), object and trace. Page size. The integrity card (the
  verification report and the per-chain status: `ok`, `signature ok` for
  host chains, `BROKEN at <seq>`) sits below the records, and moves to the
  top when a chain fails. Tenant-written notes are rendered as
  `~tenant_note` and labelled unverified (RT-02).

Every activity list (overview, object page, audit) is newest first; the
approvals list is oldest first because the oldest wait is the most urgent.

Vocabulary and status colours follow the CLI: ready / live / running
green, pending / minted / stale amber, killed / stopped / superseded / transferred grey,
failed red. IDs are monospace; the full ID is in the cell's tooltip when
abbreviated. Light and dark follow `prefers-color-scheme`.

## Mutations

The UI's actions stop things and decide approvals. Configuration,
placement and moves (`tw tenant set`, `tw deploy`, `tw host drain`, …)
stay in the CLI.

Each action is a plain HTML form that POSTs to the UI (for example
`/ui/objects/{id}/kill`). With JavaScript, `app.js` asks for confirmation
first. The UI then makes the same `/v1` call the CLI makes and redirects
back to the page with the outcome as a banner. A refusal shows
`code: message`.

| form | call |
|---|---|
| Kill | `DELETE /v1/objects/{id}?cascade=true&reason=…` (202) |
| Undeploy | `DELETE /v1/deployments/{id}?reason=…` (202) |
| Cordon / Uncordon | `PUT` / `DELETE /v1/hosts/{id}/cordon` (200) |
| Remove host (force) | `DELETE /v1/hosts/{id}?drain=true` (202 / 204) |
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

## Public API only

The UI is an ordinary client of the public `/v1` API, so it could be
moved into its own process later without changes. `ui.Handler` takes an
`*http.Client` and the API's base URL, and the UI:

- calls only operations, query parameters and headers documented in
  `api/openapi.yaml` (`docs/contracts/mgmt-api.md`), and reads only the
  documented JSON;
- never imports `mgmt`, and decides admission itself from
  `GET /v1/whoami` (`admin` and not `kind: host`). mgmt's own gate on
  `/ui/` (the admission table above) stays as a second layer;
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

## Browser-origin guard (CSRF, DNS rebinding)

The API has no cookies, so a classic CSRF cannot carry a credential; the
risk is a page on another origin (or a rebound DNS name) driving the
admin's tailnet connection. Two guards stand between the admin's browser
and mgmt.

**Host allowlist (DNS rebinding).** An attacker who controls a DNS name
can rebind it to mgmt's tailnet IP. A script on the attacker's page is then
same-origin with mgmt, so `Origin` and `Sec-Fetch-Site` look legitimate,
and its requests carry the admin's WhoIs identity. What the browser can't
hide is the `Host` header: it names the attacker's domain. So every request
on mgmt's tailnet listeners (`:80`, `:443`, the `-tsnet-service` VIP and the
operator console's loopback proxy), for **every method and path**, must
name mgmt itself, before WhoIs runs (`HostAllowlist`, `mgmt/hostguard.go`;
set by tw-mgmt at start):

- the node's MagicDNS FQDN and its short name;
- `-tsnet-hostname` and `-tsnet-service`, short and under the tailnet's
  MagicDNS suffix;
- any IP literal. Rebinding needs a DNS name the attacker controls, and a
  page loaded from an IP literal is not same-origin with anything the
  attacker serves. The `tw` CLI dials mgmt's tailnet IP through the loopback
  proxy, and `tw up` points the embedded host at it.

Anything else is 421 `misdirected_request`, naming the expected hosts (in
a browser, as the HTML page above).
Reaching mgmt through a CNAME or another DNS name you own is refused the
same way. The unix socket (`-local-dev`) and the in-process host aren't
checked because no browser can reach them.

**Origin / Sec-Fetch.** `mgmt.Server.Handler` wraps everything, API and UI,
in a second guard (`browserGuard`, `mgmt/server.go`):

- Requests that are not browser-originated (no `Sec-Fetch-Mode`, no
  `Origin`) are untouched: the `tw` CLI and `tw-hostd` send neither and
  are never affected.
- `GET`, `HEAD` and `OPTIONS` pass this guard (the Host allowlist above
  still applies to them).
- A browser-originated mutation must have `Sec-Fetch-Site` in
  `{same-origin, none}` (when present) **and** an `Origin` whose host equals
  the request's `Host` and is on the Host allowlist (when present); `/v1` mutations must additionally
  carry `X-Tw-UI: 1`. A custom header forces a CORS preflight that mgmt
  never answers, so cross-origin JavaScript cannot reach the handler at
  all. The UI's own `/ui/...` form posts are covered by the
  `Sec-Fetch-Site` / `Origin` checks. The UI no longer sends `X-Tw-UI`;
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

### Why plain HTTP on :80

mgmt always serves the API and UI on `:80` over
the tailnet. Hosts reach mgmt there, and so does `tw link` on tailnets
without HTTPS certificates. The connection is WireGuard-encrypted and
WhoIs-authenticated, so TLS would add no confidentiality between tailnet
nodes. HTTPS on `:443` is served as well when the tailnet has certificates.
Plain HTTP is what made rebinding cheap (no certificate mismatch to stop
it), and the Host allowlist closes that gap on both ports.

## Tests

- `mgmt/ui/*_test.go`: the fake `/v1` runs as a real HTTP server, so the
  UI reaches it only over HTTP. Covered: every page template over an
  empty and a populated fake (including a 410 tombstone, a broken chain
  and a hostile tenant note), the form posts, the UI's own admission via
  `/v1/whoami`, the public-API contract (`TestUsesOnlyPublicAPI`), the
  static assets and headers, and the tree and formatting helpers.
  `mgmt/ui/logs_test.go`: the logs pages (records newest first with their
  lines and exceptions, the console without terminal escapes, replica
  hosts that did not answer, the filters sent to the API, a node page
  pinned to its object, `no_logs` / `unknown_workload` /
  `host_unavailable`, and hostile log output escaped).
- `mgmt/ui_integration_test.go`: a real `Server` over the SQLite store with
  a fake WhoIs: admission per principal, the `/` redirect, the browser
  guard (cross-site refused, same-origin with `X-Tw-UI` and CLI-style
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
