# ADR-039: Queues are a sealed, fenced broker in mgmt; hostd runs push consumers

**Date:** 2026-11-15
**Status:** Accepted (2026-11-16, built and shipped in v0.2.0: `queues`,
`tw queues`, [docs/queues.md](../../queues.md); results in
`status/implement-queues-issue-18.md`).

**Context.**

- Cloudflare Queues (issue #18) has producers (`env.Q.send()` /
  `sendBatch()`, with `contentType` and `delaySeconds`) and consumers: a
  Worker's `queue(batch, env, ctx)` handler with `msg.ack()` / `retry()`,
  `batch.ackAll()` / `retryAll()`, and per-consumer `max_batch_size`,
  `max_batch_timeout`, `max_retries`, `dead_letter_queue`, `retry_delay`.
  A queue belongs to the account, not to a Worker: producer and consumer
  are usually different Workers. There are also HTTP pull consumers.
- **Upstream already has both ends.** Measured on the pinned workerd
  (1.20261108.1): the `queue` binding is a frontend that POSTs to a
  service you name (`/message` with `X-Msg-Fmt` and `X-Msg-Delay-Secs`;
  `/batch` with a JSON body of base64 bodies; `/metrics`) and needs a
  JSON answer carrying metrics. `Fetcher.queue(name, messages)` (behind
  `service_binding_extra_handlers`, which hostd already runs with
  `--experimental` for crons) runs a Worker's `queue()` handler with
  workerd's own `MessageBatch`, accepts `serializedBody` (V8 structured
  clone) as well as `body`, and returns `{outcome, ackAll, retryBatch,
  explicitAcks, retryMessages}`. So tailworks needs a broker and a dispatcher,
  not an API. The default `contentType` at tailworks' compatibility dates is
  `json`; `v8` is opt-in. Userland can't decode `v8` (`node:v8`
  `serialize` is not implemented), but `Fetcher.queue` can.
- **ADR-044** gives a platform SQLite DO per store with a
  fenced, sealed journal in mgmt, scoped to one workload. Every write is
  already a round trip to mgmt; reads are local.

**Options considered.**

1. *A queue as a platform store kind* (a `Store` DO per queue in the
   owning workload, its messages, acks and retries as journal entries,
   alarms for delays and batch timeouts). Considered first. But for a
   queue nearly every operation is a write (send, claim, ack, retry), so
   each one goes to mgmt anyway, and a local copy buys no availability: if
   mgmt is down, sends and acks fail either way. What it adds: a second
   copy that can be stale; picking one dispatcher when the workload has
   replicas (each replica has its own copy); a way for a producer in
   *another* workload, whose journal it can't read, to wake the consumer;
   and tombstones (every ack stays in the journal: the substrate only
   compacts an entry when a later one has the same `ckey`, and nothing
   compacts the ack).
2. **A broker in mgmt.** (Chosen.) Messages are rows in mgmt's database
   (SQLite or Postgres, so it survives failover with mgmt itself), sealed
   with a per-queue key like store entries. Producers send to mgmt;
   consumers claim batches from mgmt with a visibility timeout and settle
   them. A queue is a tenant resource, so producer and consumer can be
   different workloads on different hosts with nothing between them but
   mgmt. A pull consumer is the same API.
3. *Messages delivered host to host over the tailnet* (the producer's
   platform calls the consumer workload's node). Needs the consumer to be
   up to accept a send, and still needs durable storage somewhere.

**Decision.**

1. **Storage and ownership.** A queue is a tenant-scoped resource in mgmt
   (`queues`, `queue_messages`), named like Cloudflare's (`[a-z0-9-]`,
   ≤ 63). Each queue has a random `incarnation` and a 32-byte data key in
   custody (`tw/<tenant>/queues/<incarnation>/key`). Bodies are sealed
   with AES-256-GCM, bound to the incarnation and message ID; deleting a
   queue deletes its rows and key (crypto-shred). The first send or pull
   creates the queue (`tw queues create` exists for operators, and the
   dead-letter queue is created on first use, as on Cloudflare).

2. **Delivery semantics.** At least once.
   - A claim (`pull`) takes up to N visible messages, oldest first, sets
     each one's `visibleAt` to now + the visibility timeout, gives it a
     fresh lease ID and increments `attempts`. A claim that finds nothing
     waits up to the long-poll time for a send (woken in process, and it
     re-checks the database at least every second for sends through other
     mgmt replicas and for delays that expire).
   - Settling (`ack`) names lease IDs: an ack deletes the message; a retry
     makes it visible again after its delay (the call's, else the
     consumer's `retry_delay`). A lease ID that is no longer current (the
     visibility timeout expired and someone else claimed it) is ignored
     and counted `stale`. hostd extends the leases of a batch while its
     handler runs, so a long handler isn't redelivered under it.
   - A message whose `attempts` already exceed `max_retries` when it fails
     again, or when it's found expired at claim time (its consumer
     crashed), goes to the consumer's `dead_letter_queue` (re-sealed under
     that queue's key, attempts reset), or is dropped if there is none.
     `max_retries` 3 means up to 4 deliveries, as on Cloudflare.
   - Consumer crashes: if the handler throws, every message not acked
     explicitly is retried. If workerd dies mid-run, hostd gets no result
     and retries the whole batch. A batch that never reached the handler
     (workerd restarting: the connection is refused) is *released*: visible
     again at once, its attempt not counted. If hostd or its host dies, the
     leases expire and the messages come back. A lease move just changes
     which host's hostd claims next; a batch the old holder was running
     when the lease moved can't be settled (`lease_lost`) and is delivered
     again.
   - Ordering: best effort FIFO (by `visibleAt`, then send order). Not
     guaranteed: retries and delays reorder, and with concurrency > 1 or
     two consumers, batches interleave. Same as Cloudflare.
   - Batching: hostd claims, then keeps claiming until it has
     `max_batch_size` messages or `max_batch_timeout` has passed since the
     first claim, then invokes `queue()` once.

3. **Cross-workload delivery and authorization.** Nothing flows producer
   to consumer directly. A producer's platform sends through its hostd to
   mgmt, and a consumer's hostd claims from mgmt. Both are the host role,
   fenced by the lease of the workload they act for (`workload` and
   `epoch`, `409 lease_lost`, like store journals), so a host that lost a
   workload can't send or consume for it. **The deploy is the grant.**
   mgmt serves a host only queues that the workload's latest deployment
   declares: `queues.producers` to send, `queues.consumers` to claim and
   settle, and the consumer settings (`max_retries`, `dead_letter_queue`,
   `retry_delay`) come from that deployment, not from the request. Within
   a tenant (a tailnet), whoever may deploy may bind a queue, as anyone
   in a Cloudflare account may. hostd checks the same declaration before it
   forwards a send, so tenant code reaches only its bound queues.
   - The consumer runs on the host holding its workload's lease, and only
     while that host's heartbeat is fresh (cron's rule). Replicas
     (`<w>--r<n>`) each consume, as competing consumers.
   - Unlike Cloudflare, several workloads may declare a push consumer of
     the same queue. They compete; each message still goes to one batch.

4. **Pull consumers and content types.** `POST …/queues/{q}/messages/pull`
   and `…/ack` are Cloudflare's pull API shape (lease IDs, visibility
   timeout, per-message retries with delays) on the admin role: a tailnet
   identity with tailworks' admin capability, never a bearer token. A per-queue
   capability for non-admin pull clients is future work. Content types
   `json`, `text` and `bytes` are delivered as `body`; `v8` is stored as
   its serialized bytes and handed to `Fetcher.queue` as `serializedBody`,
   so workerd deserializes it. A pull consumer gets every type as base64
   bytes plus its `contentType`.

5. **Operator surface.** `tw queues create|ls|info|rm|purge` and
   `tw queues messages <queue>` (peek without claiming; how you read a
   dead-letter queue), `tw queues send` (JSON or text), and the API
   (`/v1/tenants/{id}/queues…`, tag `queues`). Audited: `queue_create`,
   `queue_delete`, `queue_purge`. Sends, claims and acks are not audited
   (volume).

6. **Delivery to the handler.** hostd posts the batch over the instance's
   private ingress as `cf.tw {kind: "queue"}` (only hostd can set it, as
   for crons). The platform decodes the bodies and calls
   `env.TENANT.queue(name, messages)`. The batch's first message carries
   the run's create ticket (so `queue()` can create objects through its
   guarded env, as `scheduled()` can); the tenant wrapper strips it before
   the handler sees the batch. Message IDs come from mgmt, so tenant data
   can't forge it.

7. **Limits.** As Cloudflare: 128 KB per message, 100 messages and 256 KB
   per `sendBatch`, `delaySeconds` and `retry_delay` ≤ 43200 (12 h),
   `max_batch_size` ≤ 100, `max_batch_timeout` ≤ 60 s, `max_retries` ≤
   100, `max_concurrency` ≤ 20 on tailworks. A queue keeps at most 1 GiB of
   bodies (`400 queue_limit`).

**Consequences.**

- Queues add no state to hosts: a consumer can move freely, and a hostd
  restart loses nothing.
- Every send, claim and settle is a transaction in mgmt's database. Fine
  for the rates a self-hosted tailnet runs; a high-rate stream belongs in
  a Durable Object.
- An mgmt outage stops sends (`send()` throws `queue_unavailable`) and
  delivery. Nothing is lost; claimed messages come back when their
  leases expire.
- `tw dev` runs producers and consumers against its in-memory fake
  mgmt, so dev queues last one `tw dev` run.

**Open.**

- A per-queue capability (tailnet grant) for non-admin pull consumers.
- Exactly one push consumer per queue, as on Cloudflare (needs a
  registration that follows undeploys).
- Queue-level settings set by the operator (Cloudflare's
  `delivery_delay` and `message_retention_period` per queue); today
  `delivery_delay` is per producer binding and retention is unlimited.
- The microVM tier: the consumer path is the same ingress as crons, so it
  should work unchanged, but it hasn't been run there.
