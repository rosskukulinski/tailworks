Last update: 2026-11-15 09:30 PT. Locks and leader election on DO leases (issue #26): guarantees measured, docs/locks.md, `lock` template, hermetic + multi-host tests.

# Locks and leader election on DO leases (issue #26)

## Step 1: the guarantees, verified

Measured with `make e2e-locks` (`e2e/locks:TestLocksFailover`): real
twd-mgmt (SQLite) and two real twd-hostd processes on testcontrol + local
DERP, default timings (heartbeat 3 s, `FenceAfter` 30 s, namespace lease
45 s). h1 reaches mgmt only through a unix-socket proxy. At t0 the test
cuts that proxy, so h1 is cut off from mgmt but still on the tailnet and
still answering clients. The workload is the lock template plus `Pulse`, a
plain-DO class whose alarm sends an egress POST every 250 ms to a recorder
node (which logs the caller via WhoIs, so it can tell h1's copy from h2's).

Run of 2026-11-15 (macOS arm64):

| Event | Time |
|---|---|
| h1 cut off from mgmt | t0 |
| takeover deploys to h2 (`forceTakeover`, pinned) refused `409 lease_held` | t0 … t0+45 s (90 tries) |
| h1's last answer to a lock request | t0+32.8 s |
| h1's last Pulse egress (side effect) | t0+32.7 s (epoch 1) |
| A's lease ran out (last renew h1 answered was sent at t0+31.0 s; TTL 10 s) | t0+41.0 s |
| takeover deploy accepted | t0+45.3 s |
| B granted the lock on h2 | t0+47.9 s |
| h2's first Pulse egress | t0+48.1 s (epoch 2) |
| h1 reconnected: revoked, no pulses for 12 s, not serving | t0+48.9 s |

Answers:

- **Split brain:** none measured. h1 fenced at about FenceAfter plus one
  heartbeat tick after its last good heartbeat. mgmt refused the move until
  the lease ran out by the store's clock. The two copies' side effects were
  15.4 s apart, and the lock was unavailable for 15.1 s.
- **Storage:** doesn't move. On h2 the lock object started empty (B was
  granted at once). The old copy's writes stay on h1's disk.
- **Egress:** the fence stops the old copy's alarms and requests, and h1
  stays silent after it reconnects (revoked). A request that left before the
  fence can't be recalled, so fencing tokens are still needed.
- **Takeover gap:** about 45 s from the last successful heartbeat. Failover
  is manual (`--force-takeover`, or `twd host rm`). Until then the lock
  fails closed.
- **Fencing tokens across the failover:** 1000000001 (epoch 1) → 2000000001
  (epoch 2). The example store refused A's late write with `409
  stale_token`. A's renewal on h2 gave `lock_lost`.

Findings beyond the issue's questions:

1. **A move forgets every holder** (storage is host-local). It's safe only
   if a holder's lease is shorter than `lease − fence − heartbeat − margin`
   ≈ 45 − 30 − 3 − 2 = 10 s. The template caps leases at 10 s
   (`MAX_TTL_MS`). The e2e asserts A's lease ended before B's grant
   (margin 6.9 s in this run).
2. **`twd kill <lock object>` is not a release.** The tenant-side wrapper
   stores `__twd_killed` in the object's own storage
   (`js/packages/tailnet/src/runtime.ts`), so the name answers `410 killed`
   from then on, even after the tombstone window. I documented it as a kill
   switch. Killing the **holder** is the emergency release
   (`KillHolderReleases`: lock free 0.8 s after the kill, 1 s lease). The
   issue's assumption that killing either one releases the lock was wrong
   for the lock object.
3. **The `lease_held` refusal message tells the operator to `twd host rm h1
   --drain`.** `mgmt.DeleteHost` releases the host's leases at once, so if
   that host is only cut off (not dead), it can serve for up to 30 s
   alongside the new host. This comes from reading the code. `e2e/locks`
   doesn't exercise it: in the local-dev harness mgmt's pushes reach the
   host over its unix push socket, which the proxy doesn't cut, so the
   harness can't produce a faithful partition for this case. docs/locks.md
   tells operators to prefer `--force-takeover`. **Follow-up worth an
   issue:** the message should suggest `--force-takeover` first, or `host
   rm` should wait for the lease.
4. **Same-host callers:** a workload can't reach another workload on the
   same host over the tailnet (netd refuses sibling hairpins, ADR-019). The
   lock service therefore has to run on a different host from its clients,
   or with `--allow-sibling-egress`. The hermetic test turns that on.
5. **A `null` JSON body crashed the first draft of the lock DO** (500).
   Fixed, and the tests send `null` bodies.
6. **Data race in hostd (fixed):** the platform ops `self`, `ensure`,
   `spawn` and `ready` answered with `objView(o)`, which read the object's
   fields without `h.mu` while `awaitNode` wrote them. `make test`'s
   `-race` caught it on the first `self()` from a lock object whose node was
   still joining. `objView` is now a `Host` method that takes `h.mu`.

## Built

- **`ctx.tailnet.self()` returns `epoch`**: the workload's lease epoch on
  this host (`hostd/http.go` `self` op, one field; type in
  `js/packages/tailnet/src/runtime.ts`). The fencing token needs it to stay
  monotonic when storage doesn't survive a move.
- **Template `lock`** (`js/packages/create-twd/templates/lock`;
  `twd init --template lock`):
  - `src/lock.ts`: the `Lock` DO (plain-DO, own node): acquire / renew /
    release / status, holder = `getCaller` identity, alarm expiry, token =
    `max(last+1, epoch·1e9+1)`, audit notes (`lock_acquired`,
    `lock_released`, `lock_expired`), TTL capped by `MAX_TTL_MS` (10 s).
  - `src/index.ts`: the Worker routes `/locks/<name>[/op]`.
  - `src/client.ts`: a copyable client: `acquire` (with wait), `tryAcquire`,
    `renew`, `release`, `status`, `withLock` (background renewal, `signal`
    aborted on loss or a missed deadline; the deadline is counted from send
    time).
- **docs/locks.md**: the guarantees table, the patterns (named-DO mutex,
  lease + renewal, fencing tokens with a worked SQL/HTTP example, leader
  election among replicas), what twd adds, and the failure modes. Linked
  from the README (docs list and the compatibility table) and
  docs/services.md.
- **COMPAT row** `locks` (⚠️), with the same row in the README table.
- **CLI / packaging**: `lock` added to the `twd init` template lists, the
  help test, create-twd's description, templates/README.md and
  INSTALL.md / README.md mentions.
- **Test helpers**: `hostd/hostdtest/fencedstore.go` (the example external
  store: the highest token per resource, `409 stale_token`) and the
  `hostd/testdata/apps/lockuser` fixture (a Worker using the template's
  client).
- **Makefile**: `e2e-locks` target and its vet line.

## Tests

- `go test -count=1 -run TestLockTemplate ./hostd/` (part of `make test`):
  PASS (15 s).
  - ContendedAcquireOneWinner: 4 nodes × 5 concurrent → 1 winner, 19
    `lock_held`; TTL capped to 10 s; token in the epoch's range; audit note
    names the winner.
  - RenewAndReleaseOnlyByHolder: the right token from another node gets
    `403 not_holder`; a wrong token gets `409 lock_lost`.
  - ExpiresWithoutRenewal: the alarm's `lock_expired` note was seen 1.0–1.2 s
    after a 1 s acquire, with no request in between.
  - TokensStrictlyIncrease.
  - ClientWithLockRenewsAndFences: `withLock` holds a 1 s lease for 2.5 s;
    the stale holder's late write is refused by the store.
  - KillHolderReleases (0.8 s).
  - KillLockObjectStopsIt: 410 for the name, other names fine.
  - RedeployKeepsHolderTokenCarriesEpoch: holder survives a same-host
    redeploy; next token = 2·1e9+1.
- `make e2e-locks`: PASS (77 s), numbers above.
- `make lint test`: PASS (2026-11-15, macOS arm64; `go test -race ./hostd/` 111 s).
- `make e2e-locks`: a second run gave the same numbers within 0.2 s (fence at t0+32.7 s, takeover t0+45.2 s, grant t0+48.1 s, A's lease ended t0+41.1 s).

## Gaps / next

- Not tested: a host that is SIGKILLed (a subset of the partition case);
  `twd host rm` of a live but cut-off host (finding 3);
  `--replace --force-takeover` of the lock workload (documented from the
  code); the microVM tier.
- No automatic failover exists. When one is built, re-run `e2e-locks`: the
  10 s TTL cap depends on the 45 s / 30 s timings.
- A platform-store-backed lock (ADR-platform-stores journal) would survive
  moves with its holders. Worth revisiting once tenant-facing stores land.
