Last update: 2026-11-15 16:00 PT. Issue #50: approver UX, approval notifications, console search, public HTML errors, and the `twd dev` data console. All six items built.

# Approver UX and console (issue #50)

Branch `approver-ux-issue-50`. History: the work started on
`approver-ux-console-issue-50`, based on the benchmark branch. Its PR #62
was merged into `benchmark-dx-infra-vs-cloudflare-workerd-vercel` after
that branch had already landed on master (PR #49), so it never reached
master. This branch is that work with `origin/master` merged in (the
`api/openapi.yaml` and `mgmt/v1.go` `WhoAmI` conflicts with deploy
history/rollback and #54's `caps`/`approver`/`mcpTools` resolved by hand,
generated files rebuilt with `make api`; `notify` added to `twd help`'s
groups) plus item 6 below.

## Built

1. **Approvers without a tenant ID.**
   - `GET /v1/whoami` gains `tailnet` (the MagicDNS suffix of the caller's
     WhoIs node name; mgmt's own tailnet when the name has no suffix),
     `tenants` (`[{tenantID, tailnet}]`: tenants whose tailnet is the
     caller's; every tenant for the local operator; none for hosts) and
     `approveGrants` (the caller's `twd.dev/cap/approve` values toward
     mgmt). Code: `mgmt/whoami_tenants.go`; one call in `mgmt/v1.go`
     `WhoAmI`, one field group in `mgmt/server.go` `WhoAmI`.
   - `/ui/approvals`: admins default to every tenant, everyone else to
     whoami's tenants; the tenant box is hidden when there is exactly one
     (a datalist of tenants otherwise); no tenant on the caller's tailnet
     says so. "What you may decide" renders the grants.
   - `twd approvals` / `twd approve` from a linked machine: no `--tenant`
     needed (whoami's only tenant; an error listing them when several);
     `--tenant <tailnet|label>` resolves through whoami too, so non-admins
     can use tailnet names.
   - Coordination with PR #54 (`twd whoami`, which adds `caps`,
     `approver`, `mcpTools`): my fields are a separate group placed before
     `hostID` in the spec and the Go struct, so the two changes touch
     different lines except the `WhoAmI` handler body in `mgmt/v1.go`
     (both edit the `writeJSON(… WhoAmI{…})` call; resolve by keeping
     #54's fields in the literal and my `a.s.whoamiScope(...)` call).
     #54's CLI can show `tenants` for non-admins from this field.
2. **Approval notifications** (`docs/design/adr/approval-notifications.md`,
   ADR-approval-notifications). A tenant webhook in custody
   (`twd/<tenant>/notifications`); mgmt POSTs `approval_requested` with a
   deep link `https://<mgmt>/ui/approvals?tenant=<id>#<instance>` when it
   ingests the host's audit record. API: `GET/PUT/DELETE
   /v1/tenants/{id}/notifications`, `POST …/notifications/test`. CLI:
   `twd notify [show] | set --webhook <url|-> [--header] [--events] |
   test | rm`. Audit kinds `notification_put`, `notification_delete`,
   `notification_failed`, `notification_test`. Egress: tailnet names /
   `*.ts.net` / 100.x over mgmt's tsnet dialer, else the host network with
   loopback, link-local and unspecified refused, no redirects, 10 s
   timeout, 3 attempts. The URL's path/query and header values never leave
   custody. Code: `mgmt/notify.go`; one line in `IngestAudit`
   (`mgmt/service.go`), one field in `Mgmt`, one line in
   `internal/mgmtd/mgmtd.go` (`SetConsoleURL`).
   Why not OTLP: see the ADR (no alert routing on most installs; the
   workload's node knows neither the console URL nor who to tell).
3. **Console search and filter.**
   - `GET /v1/audit?order=desc` (store: `AuditFilter.Desc`, keyset cursor
     reversed). Overview "recent activity", the object page's activity and
     `/ui/audit` are all newest first now.
   - `/ui/audit`: combined filters (tenant, event kind, caller=`human`,
     object, trace server-side; app/workload client-side per page, with a
     count of hidden rows), Older / Newest paging keeping the filters.
   - New `/ui/objects` (nav link): tenant, app, status server-side
     (`tenant`, `workload`, `state`); tag and acting-for client-side over
     the first 5000; newest first; 50 per page with Newer/Older links.
   - Code: `mgmt/ui/search.go`, `templates/objects.html`, `audit.html`.
4. **Approvals page polish.** Card toolbar with inline labels, the
   table in the `.tbl` card with `sect-head`, facts as `<dl class="facts">`,
   `id="<instance>"` per row with a `#` link and `:target` highlight, the
   form fallback redirects back to `#<instance>`. Non-admins no longer see
   Overview / Objects / Audit nav links (they were 403s for them).
5. **Public-route HTML errors.** `netd/errors_html.go`: `WantsHTML`
   (explicit `text/html` must beat JSON; `*/*` or a tie keeps JSON) and
   `WriteErrorHTML` (status, code, one fixed sentence per code,
   Retry-After, docs link; no detail, path or object; CSP
   `default-src 'none'`). Wired only into the public path
   (`pubWriter.html`, `netd/public_serve.go`). The issue's
   `public_disabled` isn't a netd code; `not_public`, `rate_limited` etc.
   are covered.

6. **`twd dev` data console.** `cmd/twd/devconsole.go`: a read-only
   page set on `127.0.0.1:<--console-port>` (default 0: a free port; -1:
   off), opened with the `e` hotkey (`devhotkeys.go`) as in `wrangler
   dev`. Option (b) of the earlier design note: it reads dev's own stores,
   not `/v1`, so fakemgmt stays minimal. Pages: overview (KV namespaces
   with key counts, D1 databases with migrations applied, other stores
   listed), KV keys (folded from fakemgmt's journals with
   `kvwire.Fold`/`Page`: deletes and expired keys gone, prefix filter, 100
   per page), a KV value (JSON indented, binary as hex, capped at 256 KiB),
   D1 tables with row counts and rows (50 per page) through the dev host's
   store admin `query` op (SELECTs only; `devD1Exec`, shared with the
   migrations in `dev_d1.go`). Shares the admin UI's `app.css`
   (`mgmt/ui/static_export.go`). GET only; requests whose `Host` isn't the
   loopback listener get 421 (DNS rebinding); CSP `default-src 'none';
   style-src 'self'`. `--json` emits a `console` event (schema enum, docs
   table). Edits to hotspots: one flag, five lines around
   `devInteractive`, one `con.setD1` on reload in `cmd/twd/dev.go`.
   Docs: `docs/dev-debugging.md` ("Look at the data"), `docs/COMPAT.md`
   parity row, `docs/cli-json.md`.

## Tests

After merging origin/master (2026-11-15, at 9d0c013):

    make lint                                         # ok
    make test                                         # ok except netd TestEgressWaitsForNewPeerPath
                                                      #   (quarantined in CI, issue #75; this branch
                                                      #   doesn't touch egress; passed in an earlier run)
    make test-pg                                      # ok
    make e2e-upgrade                                  # ok (TestUpgradePreviousToCandidate)

New: `mgmt/whoami_tenants_test.go` (`TestWhoAmITenants`,
`TestAuditOrderDesc`), `mgmt/notify_test.go` (config + secrets never
returned/audited, delivery with link/CLI/text/headers, filtered records,
retries then `notification_failed`, dial policy), `mgmt/ui/search_test.go`
(objects filters/paging/order, audit filters/paging, approvals defaults,
anchors, facts, tenant box, redirect fragment), `cmd/twd/notify_test.go`,
`TestApproveCLI` without `--tenant`, `netd/errors_html_test.go`,
auditfmt cases, `cmd/twd/devconsole_test.go` (`TestDevConsole`: overview,
KV fold/prefix/value/binary, D1 tables/rows, HTML escaping of app data,
SELECT-only, POST refused, foreign Host refused, app.css;
`TestDevConsoleListen`; `TestDevHotkeyConsole`), and
`cmd/twd/devconsole_live_test.go` (`TestDevConsoleLive`: a real `twd dev
--json` run, its `console` event, and the KV key and D1 row the app
wrote, seen through the console). Writing that test found that `twd dev
--json` printed its `d1 …` ready line to stdout, breaking the NDJSON
stream; fixed (it goes to the text writer like the other lines). `TestUsesOnlyPublicAPI` and the UI spec-conformance
integration test walk the new pages. Reviewed the approvals page in a
browser through an uncommitted harness over `populatedAPI()`.

## Open

- **Dev console gaps.** Durable Object storage isn't shown (it lives in
  each object's SQLite in workerd, with no read op from hostd); R2 and
  queues are listed, not browsable; no SQL box (read-only by design).
- Notifications are at most once per wait (no outbox); a mgmt restart
  between ingest and delivery drops that one. Per-app routing stays in the
  Workflow (documented recipe in docs/approvals.md).
- The audit page's app filter is per page; a server-side `workload`
  param on `/v1/audit` would need a stored column.
