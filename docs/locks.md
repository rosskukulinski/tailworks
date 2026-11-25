# Mutual exclusion and leader election with Durable Objects on tailworks

Hosted platforms sell "only one worker handles this task at a time, with a
lease that renews while the work runs" as a separate product. On tailworks it
comes from the Durable Object model. A named object runs single-threaded,
in one place. Tailworks keeps it in one place by giving the host a lease on the
workload ([ADR-011](design/adr/011-leases-conditional-writes-durable-store.md),
[ADR-028](design/adr/028-database-lease-clock-hosts-fence-themselves.md)).
Every object also has its own tailnet identity
([ADR-006](design/adr/006-every-object-gets-own-tailnet-identity.md)).

This page covers:

- what tailworks guarantees when a host dies or loses its lease, as measured;
- three patterns: a named DO as a mutex, leases with renewal and fencing
  tokens, and leader election;
- the `lock` template (`tw init my-locks --template lock`), which ships the
  second and third patterns as a small service and client;
- the failure modes.

## What tailworks guarantees

| Question | Answer | Measured by |
|---|---|---|
| Can two copies of one named object run at once on the same host? | No. workerd routes every request for a name to one single-threaded instance. 20 concurrent `acquire`s from 4 tailnet nodes produced exactly 1 winner | `hostd:TestLockTemplate/ContendedAcquireOneWinner` |
| A host is cut off from mgmt but still on the tailnet. Can a second copy start elsewhere while the first still serves? | No. The cut-off host stops serving its stateful workloads (it *fences* itself) 30 s after its last successful heartbeat. mgmt won't let another host take the workload until the lease runs out, 45 s after the last renewal by the database's clock. Measured with default timings: h1 answered its last lock request at t0+32.8 s (t0 is the moment it was cut off). The takeover was accepted at t0+45.3 s, and h2 granted the lock at t0+47.9 s. The chaos loop repeats this with seeded faults: SIGSTOP of the whole host or of hostd alone, and cut-offs past the lease, each followed by `--force-takeover`. In 37 takeovers on macOS and 38 on Linux (since #501), some of them of a host whose clock had been stepped minutes forward or back, no stale copy answered after the new one | `e2e/locks:TestLocksFailover`, `e2e/dataplane:TestDataplane` (D1, D2) |
| Side effects outside storage (egress calls)? | The fence stops the old copy, alarms included. In the test, a Durable Object made an egress call every 250 ms. h1's last call arrived at t0+32.7 s and h2's first at t0+48.1 s, so the two never overlapped. When h1 reconnected, mgmt revoked the workload and h1 stayed silent. **What the fence can't do** is recall a request that left before it: a packet still in flight, or a retry inside the system you called, can arrive after the new copy has started. Use [fencing tokens](#fencing-tokens) for that. A host that was *stalled* (a paused VM, a frozen process) past the lease is covered too: on resume its stateful workloads refuse ingress and egress (`upstream_unavailable`) until a heartbeat sent after the stall succeeds, and that heartbeat's revoke is applied first (#501) | `e2e/locks:TestLocksFailover`, `e2e/dataplane:TestDataplane` (D1: Pulse), `hostd:TestStallFence` |
| Storage writes? | A Durable Object's storage lives on its host's disk and **doesn't move**. After a takeover, the object starts **empty** on a host that never ran it. The old copy's writes stay on the old disk, so nothing is corrupted, but they are lost to the new copy. If the workload later moves back to that host, they are served again: the object resumes the state it had when the lease left, and Workflow instances resume ([#565](https://github.com/rosskukulinski/tailworks/issues/565)). A lock held at that moment is forgotten (see [failure modes](#failure-modes)) | `e2e/locks:TestLocksFailover` (B's acquire on h2 sees no holder), `e2e/dataplane:TestDataplaneWorkflows` (W4: instances resume after a move back) |
| How long is the takeover gap? | About **45 s** from the old host's last successful heartbeat (measured: 15.1 s between h1's last answer and h2's first grant). Tailworks has **no automatic failover**: someone has to redeploy with `--force-takeover` (or remove the host). Until then the lock is unavailable, so it fails closed | `e2e/locks:TestLocksFailover` |
| Are fencing tokens monotonic across a host failover? | Yes, with the template. Tokens include the workload's lease epoch, which mgmt raises on every deploy and every move. They went from 1000000001 (epoch 1, h1) to 2000000001 (epoch 2, h2), and the example store refused the old holder's late write | `e2e/locks:TestLocksFailover`, `hostd:TestLockTemplate/RedeployKeepsHolderTokenCarriesEpoch` |

The safety argument is two timeouts, both measured on one clock each:

- The host fences after `FenceAfter` (30 s) on its **monotonic** clock.
  A host that was stopped and never ran that check fences anyway: its
  stateful workloads serve and send only while the last heartbeat that
  succeeded was *sent* within `FenceAfter` (`hostd:TestStallFence`).
- mgmt hands the lease to another host only after `NamespaceLeaseTTL`
  (45 s) by the **database's** clock.

hostd refuses to start with a fence above 40 s, which leaves a 5 s margin
for heartbeat round trips and clock-rate drift
(`hostd:TestFenceBelowLease`). Wall-clock skew between machines doesn't
matter (ADR-028 §1). The chaos loop checks this by stepping a host's
clock 1–6 minutes forward or back (hostd and its workerds) and then taking
the workload over or moving it. No stale copy answered or acted
(`e2e/dataplane:TestDataplane`, D1 and D2 with the `skew` fault). The lock
template's own lease is another matter: see "Clocks" under
[failure modes](#failure-modes).

## Pattern 1: a named DO as a mutex

Inside one workload, the simplest lock is the object itself. Requests for
`getByName("job:nightly-report")` all reach one single-threaded instance, so
code that runs inside it runs alone:

```ts
export class Job extends DurableObject {
  async run() {
    // Only one run() of this job executes at a time, anywhere.
    const last = (await this.ctx.storage.get("lastRun")) ?? 0;
    if (Date.now() - last < 23 * 3600_000) return "skipped";
    await this.ctx.storage.put("lastRun", Date.now());
    await buildReport(); // see "Fencing tokens" if this writes elsewhere
    return "done";
  }
}
// Worker, cron trigger or another object:
await env.JOB.getByName("job:nightly-report").run();
```

Caveats:

- **Single-threaded doesn't mean one request at a time.** While the object
  `await`s a `fetch()` or an RPC, workerd may deliver the next request.
  Storage reads and writes don't open that gate, but other I/O does. For
  "only one at a time, across awaits", wrap the work in
  `this.ctx.blockConcurrencyWhile(...)`, or keep a "running" flag in
  storage, which is what a lease is.
- It only works for callers **in the same workload**: `env.JOB` is a binding
  of this app. Code elsewhere (another workload, a replica, a Node service, a
  laptop, CI) needs Pattern 2.

## Pattern 2: leases with renewal (the `lock` template)

`tw init my-locks --template lock` creates a small lock service:

- one Durable Object per lock name;
- each lock object has its own tailnet node, so `tw ls` lists the locks;
- its expiry runs on an alarm (plain-DO mode);
- it records the holder by tailnet identity.

Clients call it over the tailnet:

```
POST /locks/<name>/acquire  {"ttlMs": 10000}   → 200 {token, ttlMs, expiresAt, holder, epoch, …}
                                                  409 lock_held (Retry-After, retryAfterMs, holder)
POST /locks/<name>/renew    {"token": <n>}     → 200 {expiresAt, …} | 409 lock_lost | 403 not_holder
POST /locks/<name>/release  {"token": <n>}     → 200 | 409 lock_lost | 403 not_holder
GET  /locks/<name>                             → {held, holder, expiresAt, lastToken, epoch, why}
```

- **Acquire** grants the lock only when nobody holds it. It returns a lease
  `{token, expiresAt}` and records the caller's verified identity (`getCaller`:
  node ID, MagicDNS name, tags, user) as the holder. The lease length is
  capped at `MAX_TTL_MS` (10 s, [why](#failure-modes)).
- **Renew** extends the lease. The token has to match **and** the call has
  to come from the holder's node. A token that leaked to another node gets
  `403 not_holder`.
- **Expiry:** an alarm at `expiresAt` frees a lock whose holder stopped
  renewing, without waiting for the next request. In the test, a 1 s lease
  was freed about 1.0–1.2 s after acquire
  (`hostd:TestLockTemplate/ExpiresWithoutRenewal`).
- **Release** frees it at once.

The client (`src/client.ts`, which only needs `fetch`) is a file you copy
into the app that takes the lock:

```ts
import { lockClient } from "./client";
const locks = lockClient({ url: "http://my-locks" }); // the lock workload's MagicDNS name

await locks.withLock("job:nightly-report", async (l) => {
  // l.token: the fencing token. l.signal: aborted if the lock is lost.
  for (const chunk of work) {
    if (l.signal.aborted) throw l.signal.reason;
    await store.write(chunk, { fencingToken: l.token });
  }
}, { waitMs: 30_000 }); // wait up to 30 s for the lock
```

`withLock` acquires the lock and renews it every third of the lease. It
releases the lock at the end. If a renewal says the lock is gone, or the
lease's **deadline** passes without a successful renewal, it aborts `signal`
and throws once your function returns. It also checks the deadline when
your function returns, so a busy event loop that delays the expiry timer
can't hide a lapsed lease: `withLock` throws `lease_expired` then too. The
deadline is counted from when the granting request was **sent**, on the
client's own clock, so the client always stops trusting a lease no later
than the service lets it go
(`hostd:TestLockTemplate/ClientWithLockRenewsAndFences`: a 3 s lease held
for 4 s by renewing, with a contender refused meanwhile;
`create-tailworks/test/lockclient.test.mjs`: the deadline check). The lower-level
calls are `acquire`, `tryAcquire`, `renew`, `release` and `status`.

**Where to run it.**

- Put the `Lock` class in the same app (Pattern 1's binding) when only that
  app takes the lock.
- Deploy it as its own workload when other workloads, replicas or machines
  take it. A client workload on the **same host** reaches it as one on
  another host does: netd delivers the call in process after the same
  netmap and packet-filter checks (RFC §4.8), so it still needs the grant.

## Fencing tokens

A lease can't stop a holder that *believes* it still holds the lock:

- a GC pause or a stalled process outlives the lease;
- a request was sent just before the fence and arrives late;
- the lock moved host and forgot its holder.

The fix is on the side of the system you write to: it remembers the highest
token it has accepted and refuses lower ones.

Every acquire returns a token **strictly greater** than every token issued
before it for that name:

```
token = max(lastToken + 1, epoch × 1 000 000 000 + 1)
```

`epoch` is the workload's lease epoch on the lock's host
(`ctx.tailnet.self().epoch`). mgmt raises it on every deploy and on every
move, and never reuses one. So even when a takeover starts the lock with
empty storage on another host, its first token beats anything the old host
handed out. That holds across a restore of mgmt's store from a backup too:
the restore fence (`tw-mgmt restore-fence`, run by `tw restore` and
`tw upgrade`'s rollback; [upgrading.md](upgrading.md)) raises every lease epoch by 2²⁰, far
past anything handed out after the backup.

**Worked example.** A report table in Postgres (or any store with a
conditional write) keeps the token of the last writer:

```sql
-- once
ALTER TABLE reports ADD COLUMN fence bigint NOT NULL DEFAULT 0;
-- every write by the lock holder
UPDATE reports SET body = $1, fence = $2
 WHERE id = $3 AND fence <= $2;  -- 0 rows updated: a newer holder exists; stop
```

The same check as a small HTTP service. This is the example store the tests
run (`hostd/hostdtest/fencedstore.go`):

```ts
const maxSeen = new Map<string, number>();
export default {
  async fetch(req: Request) {
    const resource = new URL(req.url).searchParams.get("resource")!;
    const token = Number(req.headers.get("Fencing-Token"));
    if (token < (maxSeen.get(resource) ?? 0)) {
      return Response.json({ error: "stale_token" }, { status: 409 });
    }
    maxSeen.set(resource, token); // keep this durable in a real system
    return Response.json({ ok: true });
  },
};
```

What the tests check, on one host and across a failover:

1. A holds the lock with token *t₁* and writes: accepted.
2. A stalls. Its lease expires, or its host is cut off and the lock moves.
3. B acquires with *t₂ > t₁* and writes: accepted.
4. A wakes up and writes with *t₁*: **refused** (`409 stale_token`). A's
   renewal fails with `lock_lost`.

(`hostd:TestLockTemplate/ClientWithLockRenewsAndFences`, `e2e/locks:TestLocksFailover`.)

## Leader election among replicas

A workload with `"replicas": N` ([services.md](services.md#replicas-one-stateless-workload-on-several-hosts))
runs N copies on N hosts. Each copy has its own workload node, so its own
tailnet identity (`web`, `web--r2`, …). Replicas must be stateless (no
Durable Objects), so the election lives in a lock workload they all call:

```ts
import { lockClient, type Lease } from "./client"; // from the lock template
const locks = lockClient({ url: "http://my-locks" });
let lease: Lease | null = null;

async function campaign() {
  try {
    lease = lease ? await locks.renew(lease) : await locks.tryAcquire("leader:web");
  } catch (e) {
    lease = null; // lock_lost (someone else leads now) or the service is unreachable
  }
  if (lease && Date.now() > lease.deadline) lease = null; // renewals failing: step down
  setTimeout(campaign, lease ? lease.ttlMs / 3 : 2_000);
}
const isLeader = () => !!lease && Date.now() < lease.deadline;
```

- Exactly one replica's `tryAcquire` wins. The others get `null` and try
  again.
- If the leader's host dies, its renewals stop. Within one lease, another
  replica wins.
- `GET /locks/leader:web` shows the current leader by node name, which is
  verified (WireGuard + WhoIs), not self-reported.
- Pass `lease.token` along with anything only the leader may do.

## What tailworks adds

- **The holder is a tailnet identity.** It's the caller's verified node ID,
  MagicDNS name and tags, not a string the client sends. A token that leaks
  to another node can't renew or release the lock (`403 not_holder`,
  `hostd:TestLockTemplate/RenewAndReleaseOnlyByHolder`). The Worker's caller
  is its workload node, so two requests in one Worker share an identity;
  the token still tells them apart.
- **Auditable.** The lock writes `lock_acquired`, `lock_released` and
  `lock_expired` notes with the token and the holder. `tw audit <lock
  object>` lists who held it and when
  (`hostd:TestLockTemplate/ContendedAcquireOneWinner`).
- **Emergency release: `tw kill` the holder.** Its node is cut off the
  tailnet, so it can't renew, and the lock is free when the lease runs out.
  Measured: 3.0 s after killing a holder of a 3 s lease
  (`hostd:TestLockTemplate/KillHolderReleases`).
- **Kill switch: `tw kill` the lock object.** This is **not** a release.
  A killed object keeps a killed marker in its storage, so that lock name
  answers `410 killed` from then on. The holder's next renewal fails, and
  nobody can take that name again. Other names are unaffected
  (`hostd:TestLockTemplate/KillLockObjectStopsIt`). Use it to stop a job for
  good. Resume under a new name.

## Failure modes

- **No automatic failover.** When the lock's host dies, the lock is
  unavailable until someone runs `tw deploy --force-takeover` (or removes
  the host) and the 45 s lease has run out. Holders can't renew, and their
  leases run out. The lock fails closed.
- **A move forgets every holder.** Storage doesn't move, so after a
  takeover each lock starts free on the new host. This is safe only because
  a holder's lease runs out before the takeover can happen:
  - the old host stops serving at most `FenceAfter` (30 s) after its last
    good heartbeat (the heartbeat runs every 3 s, so about 33 s in the
    test);
  - the new host can't start before the 45 s lease runs out;
  - so a holder whose last renewal the old host answered just before
    fencing is covered if `ttl ≤ 45 − 30 − 3 − margin ≈ 10 s`.

  The template caps leases at **10 s** (`MAX_TTL_MS`) for this reason. In
  the test, A's lease ended at t0+41.0 s and B was granted at t0+47.9 s.
  Don't raise the cap, and don't lower `tw-hostd -fence-after` and the
  lease margin at the same time without redoing this sum. Fencing tokens
  stay correct either way.
- **`tw deploy --replace --force-takeover` on a live host** moves the lease
  at once. The old host stops at its next heartbeat (≤ 3 s), but the new
  copy can start in that window with empty storage. Locks held at that
  moment are forgotten, and the new host may grant them while the old
  holders' leases are still running. Tokens still order them correctly.
  Don't force-move the lock workload while its locks are in use. (This is
  from the code, `mgmt.Deploy` with `Replace`; `e2e/locks` doesn't run it.)
- **`tw host rm --force` of a host that is actually still running**
  releases its leases at once (by reading the code: `mgmt.DeleteHost`; not
  exercised in `e2e/locks`). If that host is only cut off, it keeps serving
  until it fences, up to 30 s, while the new host starts. The lease refusal
  message suggests `tw host rm --force` only for a host that is gone for
  good. For a host that may still be running,
  prefer `--force-takeover`: it waits for the lease to run out.
- **The fence can't recall requests already sent.** A request that left the
  old host before it fenced can arrive later. Anything outside tailworks that the
  lock protects must check the fencing token.
- **Clocks.** Lease expiry is measured on the lock host's clock, and the
  client's deadline on the client's own clock, counted from when its request
  was sent. Neither compares two machines' wall clocks. A client whose
  process pauses for longer than the lease (GC, VM suspend) can act late:
  that's what the token is for. A *step* of the lock host's own clock does
  matter: stepped forward, it ends a live holder's lease early and a second
  client can be granted the lock; stepped back, leases last longer (#569,
  measured by `e2e/dataplane`'s skew fault). The token still protects the
  resource.
- **Same-host callers.** A workload can't call the lock workload over the
  tailnet when both run on the same host (see
  [Where to run it](#pattern-2-leases-with-renewal-the-lock-template)).
- **Off tailworks** (no `ctx.tailnet`, e.g. under another runtime) the epoch is 0,
  so tokens are monotonic only while the object's storage lasts.

## Tests

| Test | What it shows |
|---|---|
| `hostd:TestLockTemplate` (`make test`) | the template on tw-hostd + stock workerd, with 4 tailnet client nodes, a Worker using the client, and the example store: one winner under contention, holder-bound renew and release, alarm expiry, strictly increasing tokens, `withLock` renewal and fencing, killing the holder vs killing the lock, tokens carrying the epoch across a redeploy |
| `e2e/dataplane:TestDataplane`, `deploys-*` scenarios (`make e2e-dataplane-deploys`) | deploys, rollbacks and previews racing each other through three mgmt replicas while hosts are killed, stopped or cut off mid-activation: per workload one active deployment, epochs that only go up (`ctx.tailnet.self().epoch`, the base of fencing tokens: `hostd:TestDeployNeverLowersEpoch`, #584), and a live `--replace --force-takeover` move whose old copy stops within 5 s |
| `e2e/locks:TestLocksFailover` (`make e2e-locks`) | real tw-mgmt and two tw-hostd processes, default timings. h1 is cut off from mgmt while still on the tailnet: it fences before h2 can take over; side effects don't overlap; tokens increase across the move; the store refuses the old holder; h1 is revoked when it reconnects |
