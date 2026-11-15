---
kind: feature
upgrade: [protocol, cli-json]
---
Approvers no longer need a tenant ID, and they hear about waiting approvals
(issue #50).

- `GET /v1/whoami` now returns `tailnet` (the caller's own), `tenants` (the
  tenants on that tailnet; every tenant for the local operator) and
  `approveGrants` (the caller's `twd.dev/cap/approve` grants). The admin
  UI's approvals page and `twd approvals` / `twd approve` from a linked
  machine default to those tenants; the page hides the tenant box when
  there is exactly one.
- `twd notify set --webhook -` sets a tenant webhook (stored in custody;
  only its origin is ever shown). When a Workflow starts waiting for an
  approval, mgmt POSTs a JSON `approval_requested` event with a deep link
  to the request's row (`/ui/approvals?tenant=…#<instance>`); Slack-style
  incoming webhooks show its `text`. New API: `GET/PUT/DELETE
  /v1/tenants/{id}/notifications`, `POST …/notifications/test`; new audit
  kinds `notification_put`, `notification_delete`, `notification_failed`,
  `notification_test`.
- `GET /v1/audit?order=desc` lists newest first. The console's audit page
  uses it and combines tenant, event, caller, object, trace and app
  filters; a new **Objects** page filters by tenant, app, status, tag and
  acting-for, with paging; every activity list is newest first.
- The approvals page matches the rest of the console: card filter bar,
  facts as a definition list, an anchor per row, and "What you may decide".
- Browsers on public (Funnel) routes get a minimal HTML error page (status,
  code, docs link, nothing about the request) instead of JSON when they ask
  for `text/html`. JSON stays the default.
