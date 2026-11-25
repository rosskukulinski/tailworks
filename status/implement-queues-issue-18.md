Last update: 2026-11-15 09:40 PT. Queues (issue #18, ADR-039): producers, push consumers, retries, DLQ, HTTP pull, `twd queues`; multi-host e2e green.

# Queues (issue #18)

Design: [ADR-039](../docs/design/adr/039-queues.md). User docs:
[docs/queues.md](../docs/queues.md).

## Decision in one paragraph

A queue is a tenant resource **brokered by mgmt** (sealed rows in its
database), not a platform-store kind. For a queue nearly every operation is
a write (send, claim, ack, retry), so each one goes to mgmt anyway; a local
Store DO would have added a second copy, dispatcher election across
replicas, cross-workload wakeups and unbounded ack tombstones, and bought
no availability. Producers send through their hostd; push consumers' hostd
claims batches with a visibility timeout and settles them; HTTP pull is the
same API for admins. Tenant code uses workerd's own `queue` binding and
`Fetcher.queue()` (so `MessageBatch`, `ack`/`retry`, `v8` all come from
workerd). The deploy is the grant: mgmt serves a host only queues the
workload's latest deployment declares, fenced by the workload's lease.

## Built

- **`internal/queuecfg`**: parse/validate wrangler `queues` (Cloudflare's
  names and limits; `max_concurrency` ≤ 20 on twd). Shared by hostd's
  bundle parser and mgmt's deploy check (`400 bad_bundle`).
- **API** (`api/openapi.yaml`, tag `queues`; `make api`):
  `GET /v1/tenants/{id}/queues`, `GET|PUT|DELETE …/queues/{q}`,
  `POST …/{q}/purge`, `GET|POST …/{q}/messages` (peek / send),
  `POST …/{q}/messages/pull`, `POST …/{q}/messages/ack` (acks, retries
  with delays, extend, release). Errors `unknown_queue`, `queue_limit`;
  audit `queue_create`, `queue_delete`, `queue_purge`.
- **mgmt** (`mgmt/queues.go`, `mgmt/sqlstore_queues.go`): `queues` and
  `queue_messages` tables (SQLite and Postgres; `SKIP LOCKED` claims on
  Postgres), bodies AES-256-GCM sealed under a per-queue custody key (AAD =
  incarnation + message ID), crypto-shred on delete. Claims count attempts
  and set a lease; long poll (in-process wakeup + 1 s re-check for other
  replicas and expiring delays); dead-lettering on a failed retry or on an
  expired lease past `max_retries` (re-sealed under the DLQ's key; DLQ
  created on first use). Host calls fenced like store journals; consumer
  settings read from the workload's latest deployment (cached per
  deployment).
- **hostd** (`hostd/queues.go`): `/v1/queue/send|metrics` on the local
  host API (checks the deployment's producer binding, applies
  `delivery_delay`); a reconcile loop that runs `max_concurrency`
  consumer loops per push consumer of each active workload while the
  heartbeat is fresh (cron's rule); claim → fill to `max_batch_size` or
  `max_batch_timeout` → run over private ingress (`cf.twd.kind "queue"`)
  → settle. Leases extended while `queue()` runs; a batch that never
  reached `queue()` (no instance, connection refused) is **released**
  uncounted. `hostd.Config.QueueVisibility` / `twd-hostd
  -queue-visibility` (default 60 s).
- **gen/JS** (`hostd/gen/queues.go`, `hostd/gen/js/queue.js`): tenant
  `queue` bindings to the platform's `QueueProducer` (props `{queue,
  binding}`), a platform-side twin per binding for facet proxies
  (`QueueFacetProxy`), `deliverQueue` (decodes bodies; `v8` as
  `serializedBody`) calling `env.TENANT.queue()`;
  `service_binding_extra_handlers` when the app has a push consumer.
  Producer errors use workerd's `CF-Queues-Error-Code/-Cause` headers, so
  `send()` throws `Error: [queue_limit] … (400)` instead of "Unknown
  Internal Error".
- **Runtime** (`js/packages/tailnet/src/runtime.ts` → `twd-rt.js`): the
  batch's first message carries the delivery's create ticket; the wrapper
  strips it (object and class default exports), so `queue()` can create
  Durable Objects through its guarded env like `scheduled()`.
- **fakemgmt** (`hostd/fakemgmt/queues.go`): the same semantics in memory
  (consumer settings from the deployment's bundle), so `twd dev` runs
  producers and consumers.
- **CLI** (`cmd/twd/queues.go`): `twd queues create|ls|info|rm|purge|
  messages|send`.
- **hostd fix** (`hostd/workload.go` `onExit`): a workerd that crashed left
  its unix ingress socket behind, and every restart failed with
  `bind(): Address already in use` until hostd gave up (the workload went
  `failed`). The stale socket is removed before the restart.

## Tests (2026-11-15, macOS arm64)

- `go test ./internal/queuecfg/`: ok (parse, defaults, every limit).
- `go test ./mgmt/ -run TestQueues`: ok.
  - `TestQueuesSendPullAck`: send by the producer's host, claim (attempts,
    in flight hidden), ack/retry/extend by lease, stale leases, sealed at
    rest (no plaintext in mgmt's dir, logs, audit), the deploy as the grant
    (403 for undeclared queues, an http_pull consumer can't be claimed by
    a host, 400 for a host without a workload), size limits.
  - `TestQueuesDelayRetriesDLQ`: delaySeconds (a waiting claim gets it when
    due), retry delays, `max_retries` → DLQ (body and send time intact),
    expired lease past `max_retries` → DLQ at the next claim (crash),
    drop without a DLQ, admin pull settings.
  - `TestQueuesLongPoll`, `TestQueuesFence` (other host / old epoch
    refused; after `MoveLease` the new holder gets the old holder's
    in-flight messages once their leases expire), `TestQueuesLifecycle`
    (create/list/paging/peek/purge/delete + custody key gone, audit, bad
    config fails the deploy).
- `go test ./hostd/ -run 'TestQueues$'` (real workerd, fake mgmt): ok
  (~30 s). Bindings (no platform binding visible to tenant code), batch by
  size (3) and by timeout (1 s), `retry({delaySeconds: 2})`, `delaySeconds`
  and `delivery_delay`, `max_retries` → `jobs-dlq`, a throw retries the
  batch, an explicit ack survives a throw, `text`/`bytes`/`v8` bodies, a
  send from a facet, `metrics()`, a 130 KB message refused with
  `queue_limit`, workerd SIGKILLed mid-batch (redelivered, attempt 2), a
  fenced host consumes nothing and the operator's message arrives after
  the heartbeat is back. `queue()` creates the `Log` DO, so the ticket path
  is covered.
- `go test ./hostd/bundle/ -run TestParseQueues`, `./hostd/gen/ -run
  TestGenerateQueues`: ok.
- `go test ./cmd/twd/ -run 'TestCLIQueues|TestDevQueues|TestHelp'`: ok
  (`TestDevQueues` runs the real `twd dev` binary with the queues fixture:
  a send on the dev port reaches the app's own `queue()`).
- `make e2e-queues` (`e2e/queues`, real twd-mgmt + two twd-hostd, ~22 s):
  ok. Producer on h1, consumer on h2 (25 delivered); 25 sent while h2's
  hostd was stopped, delivered after restart; h2's hostd and workerd
  SIGKILLed mid-batch, batch redelivered (attempts [1 2]); consumer moved
  h2 → h1 with `replace` while 60 messages flowed, all delivered; queue
  drained. Redeliveries: 0 / 0 / 10 of 60 around the move (the old
  holder's settle is refused after the move: at-least-once, as designed).
- `go test -race ./hostd/`: ok (127 s) after the change.
- After merging origin/master (observability, wrangler CLI): `make lint`
  ok; `make test` ok (every Go package, `-race`, and `npm test` 25/25);
  `make e2e-queues` ok again.
- Second master merge (KV #37, R2 #35, D1 #33, agent DX #32): `twd queues`
  joined the `--json` contract (`schema/cli.schema.json` defs,
  `cliSurface`), `llms.txt` and the skill's ask-first list; the ADR is
  numbered ADR-039. `make lint` ok, `make test` ok (all Go packages with
  `-race`, npm tests), `make e2e-queues` ok.

## Findings

- workerd's `queue` binding: `send()` defaults to `contentType: "json"` at
  twd's compatibility dates (not `v8`); the service must answer JSON with
  `{metadata: {metrics: {backlogCount, backlogBytes,
  oldestMessageTimestamp(number)}}}`; errors are surfaced only through
  `CF-Queues-Error-Code` / `CF-Queues-Error-Cause` headers.
- `Fetcher.queue()` needs `--experimental` (hostd already passes it) and
  accepts `serializedBody`, so `v8` works without a userland decoder
  (workerd's `node:v8` `serialize` is not implemented).
- `MessageBatch.messages` is a plain array: filtering out the ticket
  message keeps the remaining messages' native `ack()`/`retry()`.

## Gaps and next steps

- Postgres: the SQL is portable (`COUNT(CASE …)`, `SKIP LOCKED`), but
  `make test-pg` was not run on this branch.
- microVM tier: not run (same ingress path as crons).
- Exactly one push consumer per queue (Cloudflare's rule) needs a
  registration that follows undeploys; today consumers compete.
- A per-queue tailnet grant for non-admin HTTP pull clients.
- Queue-level settings (`message_retention_period`, a queue-level
  `delivery_delay`); messages are kept until consumed.
- `max_concurrency` doesn't autoscale.
- Coordination with KV/R2/D1: queues don't use the platform-store
  substrate (see the ADR); they share its custody/AEAD helpers
  (`storeAEAD`) and fencing (`fenceStore`).
