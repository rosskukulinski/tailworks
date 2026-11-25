# ADR-047: tenant notification webhooks, starting with `approval_requested`

**Date:** 2026-11-15
**Status:** Accepted (2026-11-16, built on master after v0.2.0: `tw notify`;
results in `status/approver-ux-issue-50.md`). Drafted as
`ADR-approval-notifications`.
Builds on ADR-040 (Workflows and approvals) and ADR-045 (the
tenant's OTLP export, whose custody pattern it reuses).

**Context.**

- A Workflow that waits for an approval (ADR-040) is listed by
  `tw approvals`, the admin UI and `GET /_tw/approvals`, and recorded as
  `approval_requested` in the host's audit chain. Nobody is told. An
  approval waits until someone happens to look, and the default timeout
  (24 hours) then rejects it.
- GitHub environment approvals and Vercel's rolling-release approvals notify
  the reviewer and link to the decision. The issue (#50) asked for either
  an `approval_requested` webhook or OTLP event carrying a deep link to the
  console row, or a documented recipe that notifies from the Workflow
  itself.
- Options considered:
  1. **An OTLP log event** through the tenant's existing OTLP export. No new
     setting, but it reaches people only through a collector pipeline with
     alert routing, which most small installs don't run. It is also emitted
     from the workload's node, which knows neither the console URL nor who
     should be told.
  2. **Only a recipe** (a `step.do` that calls a notifier before the wait).
     It works today and stays documented, but every app must repeat it,
     and the message can't carry a link the platform vouches for.
  3. **A tenant webhook that mgmt calls.** mgmt already receives every
     `approval_requested` record (host audit ingest), knows its own
     MagicDNS name for the deep link, and has custody for secrets. One
     setting per tenant covers every Workflow of every app.

**Decision.**

1. **One webhook per tenant, set by an admin.** `PUT
   /v1/tenants/{id}/notifications` with `{"webhook": {"url", "headers",
   "events"}}` (`tw notify set --webhook -`); `GET` (origin and header
   names only), `DELETE`, and `POST …/notifications/test` (`tw notify
   test`). Events today: `approval_requested` (the default). The list is
   one value per line in the spec so later events (`approval_timeout`,
   deploy failures) add cleanly.
2. **The config is a secret, kept in custody.** A chat webhook's path is
   its credential (Slack, Discord, Google Chat). The whole config lives in
   custody under `tw/<tenant>/notifications`, like the OTLP export; the
   store holds nothing. No API answer, log line or audit record carries the
   URL's path or query or a header value: only the origin
   (`https://hooks.slack.com`) and the header names.
3. **The trigger is mgmt's ingest of the host record.** When
   `IngestAudit` stores an `approval_requested` record, and the record's
   object is known (so tenant and workload come from mgmt's store, not
   from the record), mgmt sends the event in the background. A record is
   stored once per chain position, so each wait notifies once; a host
   retrying a batch does not notify twice. A record naming a workload other
   than its object's is ignored.
4. **The payload.** JSON `NotificationEvent`: `event`, `tenantID`,
   `workload`, `workflow`, `instance`, `step`, `facts`, `link`
   (`https://<mgmt>/ui/approvals?tenant=<id>#<instance>`), `cli`
   (`tw approve <workload> <instance> --workflow <name>`), `sentAt`, and a
   one-line `text`, which Slack, Mattermost and Google Chat incoming
   webhooks show as is. The link is a convenience, not a capability:
   whoever opens it still decides with their own tailnet identity and
   grants. Header `Tw-Event` names the event.
5. **Egress from mgmt.** mgmt dials the URL itself: a one-label name, a
   `*.ts.net` name or a Tailscale address over its tailnet node; anything
   else over the host's network. Loopback, link-local and unspecified
   addresses are refused (no reaching the host's own services or a cloud
   metadata endpoint), redirects are not followed, and each attempt has a
   10 s timeout.
6. **Delivery.** 3 attempts (2 s and 8 s apart). A final failure is
   audited (`notification_failed`, with the origin and the error, never
   the URL). Sets, deletes and tests are audited (`notification_put`,
   `notification_delete`, `notification_test`).

**Consequences.**

- Approvers learn about a waiting approval within seconds, with a link to
  the row, and need no tenant ID (`GET /v1/whoami` now names the
  caller's tenants; issue #50).
- Delivery is best effort and at most once per wait: a mgmt restart
  between ingest and delivery loses that notification (the approval is
  still listed). A durable outbox is not worth it until there is a second
  event that needs it.
- Per-app or per-person routing stays in the Workflow (the documented
  recipe), or in the receiving chat tool.
- Email is not sent directly: point the webhook at a mail-sending service's
  HTTP endpoint, or send from the Workflow.
