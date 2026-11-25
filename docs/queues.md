# Queues

tailworks runs Cloudflare-style Queues. A Worker sends messages with
`env.QUEUE.send()`, and a Worker's `queue()` handler consumes them in
batches, with retries and a dead-letter queue. The producer and the consumer
can be different workloads on different hosts. The design is
[ADR-039](design/adr/039-queues.md).

```jsonc
// wrangler.jsonc
{
  "queues": {
    "producers": [{ "binding": "JOBS", "queue": "jobs" }],
    "consumers": [
      {
        "queue": "jobs",
        "max_batch_size": 10,        // 1-100, default 10
        "max_batch_timeout": 5,      // seconds, 0-60, default 5
        "max_retries": 3,            // 0-100, default 3
        "dead_letter_queue": "jobs-dlq",
        "retry_delay": 0,            // seconds, default 0
        "max_concurrency": 1,        // 1-20, default 1
      },
    ],
  },
}
```

```js
export default {
  async fetch(req, env) {
    await env.JOBS.send({ userId: 42 });                       // JSON
    await env.JOBS.send("hello", { contentType: "text", delaySeconds: 30 });
    await env.JOBS.sendBatch([{ body: { a: 1 } }, { body: { a: 2 }, delaySeconds: 5 }]);
    return new Response("queued");
  },
  async queue(batch, env, ctx) {
    for (const msg of batch.messages) {
      try {
        await handle(msg.body); // msg.id, msg.timestamp, msg.attempts
        msg.ack();
      } catch {
        msg.retry({ delaySeconds: 60 });
      }
    }
  },
};
```

The API is workerd's own: `send(body, {contentType, delaySeconds})`,
`sendBatch(messages, {delaySeconds})`, `metrics()`, and in the consumer
`MessageBatch` with `msg.ack()`, `msg.retry({delaySeconds})`,
`batch.ackAll()` and `batch.retryAll()`. Producer bindings work from the
Worker and from Durable Objects (facet-mode objects get a proxy to the same
binding). A producer's
`delivery_delay` (seconds) delays messages that don't give their own.

Content types: `json` (the default), `text`, `bytes` (an `ArrayBuffer` in the
consumer) and `v8` (structured clone: `Date`, `Map`, typed arrays, …).

## Delivery

Delivery is at least once.

- **Batches.** The consumer's host claims messages and invokes `queue()`
  once it holds `max_batch_size` of them or `max_batch_timeout` has passed
  since the first one.
- **Acks and retries.** A message you `ack()` is deleted. One you `retry()`
  comes back after its delay (or the consumer's `retry_delay`). If
  `queue()` returns, every message you didn't retry is acked. If it throws,
  every message you didn't ack explicitly is retried.
- **Dead letters.** After `max_retries` retries (so `max_retries + 1`
  deliveries), a failing message goes to `dead_letter_queue`, which is
  created on first use. With no dead-letter queue it is dropped.
  `msg.attempts` counts deliveries, starting at 1.
- **Crashes and moves lose nothing.** Messages live in mgmt's database,
  not on a host. A claimed batch is hidden for a visibility timeout (60 s,
  `tw-hostd -queue-visibility`). hostd extends it while `queue()` runs. If
  workerd dies mid-batch, the batch is retried. If hostd or its host dies,
  the timeout expires and the batch is delivered again. A batch that never
  reached `queue()` (workerd restarting, or the workload moved away first)
  is handed back without counting an attempt.
- **Who consumes.** The host holding the consumer workload's lease runs
  the consumer, and only while its heartbeat to mgmt is fresh (the same
  rule as cron triggers). When the workload moves (`tw deploy --replace`,
  a failover), the new host takes over. A batch the old host was still
  running is delivered again if the old host can no longer settle it.
- **Order** is best effort: oldest first, but retries, delays and
  concurrency reorder messages, as on Cloudflare.
- **Time limit:** one `queue()` run gets 15 minutes of wall time.

Measured by `e2e/queues` and the data-plane chaos loop
(`e2e/dataplane:TestDataplane`, invariant D3), which kills hosts, hostd and
workerd, stalls and cuts off the consumer's host, moves the workload, takes
it over and restarts mgmt under a steady producer: every acknowledged send
was delivered, no two deliveries of a message had the same `attempts`,
retried messages came back, and poison stopped at `max_retries + 1` and
reached the dead-letter queue.

## Identity and security

- A queue belongs to the **tenant** (your tailnet), as a Cloudflare queue
  belongs to the account. Queues are created on first use, or with
  `tw queues create`.
- **The deploy is the grant.** mgmt serves a host only the queues the
  workload's latest deployment declares: `queues.producers` to send,
  `queues.consumers` to consume. The consumer's `max_retries`,
  `dead_letter_queue` and `retry_delay` come from that deployment. A host
  acts only for workloads whose lease it holds, so a host that lost a
  workload can't send or consume for it.
- Message bodies are encrypted at rest in mgmt's database with a per-queue
  key in custody. `tw queues rm` deletes the key too.
- A `queue()` run has no caller, like a cron run, and its outbound
  `fetch()` leaves from the workload's own node. It may create Durable Objects through its `env`, as a tailnet
  request may.
- Nobody on the tailnet can invoke `queue()`. hostd delivers batches over
  the instance's private ingress as `cf.tw.kind = "queue"`, which only
  hostd can set.

## Operating queues

```sh
tw queues ls                       # backlog, in flight, delayed, last consumer
tw queues info jobs
tw queues messages jobs-dlq        # read messages without claiming them
tw queues send jobs '{"retry": 7}' # or --text 'hello', or the body on stdin
tw queues purge jobs               # delete every message
tw queues rm jobs                  # delete the queue, its messages and its key
tw queues create jobs
```

HTTP pull consumers (`"type": "http_pull"`) use mgmt's API:
`POST /v1/tenants/{id}/queues/{queue}/messages/pull` and `…/messages/ack`
([contract](contracts/mgmt-api.md)). They have Cloudflare's pull shape:
lease IDs, a visibility timeout, and per-message retries with delays. The
caller needs tailworks' admin capability (a tailnet identity, never a token).

## Differences from Cloudflare

- Several workloads may declare a push consumer of the same queue. They
  compete, and each message still goes to one batch. Cloudflare allows one.
- `max_concurrency` is fixed: that many consumer loops per queue on the
  host. Cloudflare scales up to it.
- No per-queue settings: `delivery_delay` is per producer binding, and
  messages are kept until consumed (no `message_retention_period`).
- HTTP pull is admin-only, through tailworks' API (not Cloudflare's REST API).
- Errors from `send()` read `Error: [<code>] <message> (<HTTP status>)`,
  with tailworks' [error codes](errors.md) (`queue_limit`, `queue_unavailable`).

## Local development

`tw dev` runs producers and consumers against its in-memory mgmt, so a
send from your Worker reaches its own `queue()` handler. Dev queues last
one `tw dev` run.
