Last update: 2026-11-15 PT. Public ingress connection limits (#69, red-team H09 / fix F6).

## What's built

- `netd/public_conns.go` (new): the public listener's connection limits.
  - `maxConnsPerIp` (default 32, ceiling 1024): per-client cap, below the
    per-workload `maxConns`. IPv6 clients are keyed by their /64.
    Refusals are counted as `IPConnsRefused` in `PublicRejectSummary`
    (separately from `ConnsRefused`, over `maxConns`).
  - First-request deadline: a new connection gets 5 s (`publicFirstRequestTimeout`)
    to finish its TLS handshake and send its first request's headers. It's
    applied by clamping read deadlines in `limitedConn` until net/http
    reports `StateActive` (the `ConnState` hook), after which the server's
    own deadlines apply. Slow bodies and keep-alive idling are not affected.
  - `upgradeLifetimeSeconds` (default 3600, ceiling 86400): an upgraded
    request's context times out and the client conn is closed then
    (`boundUpgrade`), so the slot is freed whatever the upstream does.
  - Saved node options from before this change (zero fields) get the
    defaults (`withConnDefaults`), so a new host behind an older mgmt is
    still capped.
- `internal/publicspec`: the two fields, defaults, ceilings, validation;
  `api/openapi.yaml` `PublicLimits` (+ `make api`); `schema/twd.schema.json`.
- hostd: `public_rejected` audit detail gains `ipConnsRefused`; new counter
  `twd.public.rejected{twd.tenant.id, twd.workload, twd.public.reason}`
  (`hostd/public_metrics.go`), fed from the same per-minute summary.
- Docs: `docs/public-ingress.md` (limits table, "Connections"), `docs/twd-jsonc.md`,
  `docs/observability.md` (metric row). Release note: `changes/public-conn-limit-issue-69.md`.

## Tests

- `go test ./netd -run 'TestRT3_H09|TestPublic' -count=1`: ok.
  - `TestRT3_H09_MaxConnsOneIP`: one address trying to take all `maxConns`
    keep-alive conns holds only `maxConnsPerIp`; other addresses get through;
    `maxConns` still bounds the total; summary counts and top IP; slot release.
    With the per-IP check disabled it fails:
    `one address holds 8 connections, want 3 (maxConnsPerIp)`.
  - `TestPublicMaxConnsPerIPv6Prefix`, `TestPublicFirstRequestTimeout`,
    `TestPublicUpgradeLifetime`.
- `go test ./internal/publicspec ./schema`: ok (`TestConnLimits`).
- `make lint`: ok. `make test-js`: ok (32/32).
- `make test` (test-go, -race): everything passed except three tests that
  passed on a targeted rerun: `TestWorkersKVMove` (a data race in
  `hostd/mgmtapi.(*Client).setHeaders` vs `retryNode`, not in code this
  branch touches), `TestR2Streaming` (1 GiB client timeout) and
  `TestGeneratedWorkflows/AppCannotApprove`. CI quarantines the last two (#75).
  `go test -race ./hostd -run 'TestWorkersKVMove$|TestR2Streaming$|TestPublic'`: ok.

## Gaps / next

- `security/LEDGER.md` H09 / F6 rows not flipped to fixed here (they sit next
  to H10 / F7, which the #70 branch edits; update after both land).
- Not run at scale on a real Funnel; the 5 s first-request deadline over
  Funnel's relay path is a guess worth checking on a real tailnet.
- The first-request timeout and keep-alive idle timeout (60 s) are fixed,
  not per-workload limits.
