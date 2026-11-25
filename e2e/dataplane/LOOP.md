# Loop: the data plane under faults (chaos + invariants)

Issue #415. The second loop in the `e2e/multimgmt` style, for the data
plane: Queues, Durable Object and lock fencing with a real `tw-hostd`, and
D1 across moves. State lives in `e2e/dataplane/LEDGER.md`. Read it first,
every iteration.

Run one scenario by hand:

```sh
make deps
TW_DP_SCENARIO=fencing TW_DP_SEED=7 TW_DP_DURATION=4m \
  go test -tags e2edataplane -count=1 -v -timeout 25m -run TestDataplane ./e2e/dataplane/
```

`make e2e-dataplane` runs the `mixed` scenario for 4 minutes with
today's date as the seed. `nightly.yml` runs it every night. To replay a
night's run, take the seed from its log (`scenario mixed, seed 20261124`).
`TW_DP_FAULTS=pause-takeover,move` narrows a run to some faults.
`TW_DP_REPLAY=<results dir> go test -tags e2edataplane -run TestReplay
./e2e/dataplane/` re-runs the checkers on a recorded history, so a checker
change can be tried without a new run.

## Hard rules

1. **Hermetic only.** testcontrol, a local DERP, the fake Tailscale API.
   No real tailnet, no real keys.
2. **Only kill what you started.** Every process the harness starts or
   signals (hostd, its workerds, mgmt) is recorded in
   `results/<run>/pids`. Faults signal those PIDs and their descendants,
   never a name pattern.
3. **Budget:** one experiment per iteration, ≤ 15 min wall clock, ≤ 3
   reruns of a seed.
4. **Findings become issues** labelled `dataplane-chaos`, each with the
   seed, the scenario, the violated invariant and the evidence from
   `report.json`. A finding's regression test goes in with its fix.

## The cluster

- `tw-mgmt` (SQLite) on a unix socket. Each of `h1`, `h2` and `h3` reaches
  it through its own proxy, which the nemesis can cut. A cut host stays on
  the tailnet and keeps answering clients: the split-brain case of
  `TestLocksFailover`.
- Three `tw-hostd` processes with the default timings (heartbeat 3 s,
  `FenceAfter` 30 s, lease 45 s), one host per process, so SIGSTOP stalls a
  real host. `-queue-visibility 5s`.
- One workload, `testdata/dp`, holds every primitive, so one lease and one
  fault cover them all: a queue producer and consumer (with a dead-letter
  queue), the lock template, `Reg` (a DO counter), `Pulse` (a DO whose alarm
  makes an egress call every 250 ms), a D1 database and a KV namespace.
- Tailnet nodes: `dp-cli` (the client and the prober), `dp-lka` and
  `dp-lkb` (two lock clients), `dp-store` (the fenced store of
  docs/locks.md), `dp-rec` (the recorder). The consumer and Pulse report
  to the recorder, so the record survives whatever happens to the hosts.

## The nemesis (seeded: `TW_DP_SEED`)

| Fault | What it does |
|---|---|
| `kill-host` | SIGKILL hostd and its workerds; restart it after 1–5 s |
| `crash-hostd` | SIGKILL hostd alone, as an OOM kill or a panic would (its workerds, in process groups of their own, survive it); restart it. The new hostd must reap them (D7, #432) |
| `kill-workerd` | SIGKILL the host's workerds; hostd restarts them |
| `pause-short` | SIGSTOP hostd and its workerds for 3–20 s (under `FenceAfter`), then SIGCONT |
| `pause-takeover` | SIGSTOP the host, `--force-takeover` to another host once mgmt allows it, SIGCONT 2–8 s later |
| `pause-hostd-takeover` | the same, stopping only hostd (workerd keeps running) |
| `cut-short` | cut the host off from mgmt for 3–20 s |
| `cut-takeover` | cut it off, take over elsewhere, reconnect it |
| `skew` | step the host's wall clock (hostd and its workerds) 1–6 min forward or back, within ±8 min, and leave it there for the faults that follow (H3). Linux only: `fencing` and `mixed` get it where the preload library builds |
| `move` | `deploy --replace --force-takeover` of the live workload to another host |
| `restart-mgmt` | SIGKILL or SIGTERM mgmt, restart it after 0.5–5 s |

Scenarios are fault mixes: `queues`, `fencing`, `d1`, `mixed`
(`nemesis_test.go`). The nemesis always targets the current lease holder.

Clock skew: the hosts are a test build (`-tags twtestclock`,
`internal/wallclock`). Each hostd follows the offset in `<work>/<host>/clock`
for its wall-clock reads (tickets, caller seals, MAC timestamps, cron), and
its workerds run with `internal/wallclock/testdata/skewclock.c` preloaded,
which shifts `CLOCK_REALTIME`, `gettimeofday` and `time` by the same
offset. Monotonic clocks aren't shifted. Before the nemesis starts, the
harness checks that the workload's `Date.now()` follows a +10 s step, in
a Worker and in a Durable Object (`/clock`). The library is Linux only, so
on macOS the skew fault is left out and a seed runs a different fault
sequence than on Linux. mgmt (SQLite) keeps the true clock.

Workflows and approvals (#418) have their own test, workload and checkers
(W1–W5): see [ledger/workflows.md](ledger/workflows.md).

**mgmt replicas.** `TW_DP_MGMT=pg3` (or `pg2`) runs mgmt as replicas on a
throwaway Postgres container (docker context `PG_CTX`, default `orbstack`),
each behind its own fault proxy, with a shared fake setec: the pieces of
`e2e/multimgmt`, from `e2e/internal/mgmtpg` and `e2e/chaoskit` (`mgmtpg_test.go`). The
cluster's mgmt socket spreads connections over the running replicas, so
hosts fail over when one dies; `restart-mgmt` then restarts one replica,
and `pg-fault` blackholes, slows or resets one replica's path to Postgres.
The deploys scenarios use `pg3` unless `TW_DP_MGMT` says otherwise.

## Workloads and the history

Every client op is tagged with a unique ID and timed on the client
(`Sent` before the request leaves, `Recv` when the answer is in). Events go
to `history.jsonl` as they happen. The ops are queue sends (every 7th one
is retried once, every 23rd is poison), Reg increments and reads, probes of
every endpoint ever seen (to catch a copy that should be fenced), D1
inserts, KV puts and gets (`kv_test.go`), and lock acquire/renew/release
with a fenced-store write after each grant and renew.

## Invariants (`check_test.go`)

| ID | Invariant |
|---|---|
| D1 | One live serving copy: once a copy at a newer epoch has answered, no older epoch answers a request sent after that. No older epoch's Pulse egress arrives more than 1 s after the newer copy's first |
| D1-reg | The DO counter, per epoch: increments are unique and increase in real-time order, and a read sees every increment acknowledged before it was sent. Writes survive a restart on the same host. Across a move the copy starts empty (documented) |
| D2 | Fencing: two lock holders never overlap within an epoch, or across a takeover of a stalled or cut-off host. Tokens only go up. The store never accepts a token lower than one it has accepted. An overlap within an epoch right after a forward clock step on the lock's host is a note, not a failure, until #569 is decided |
| D3 | Queues: every acknowledged send is delivered. No two deliveries of a message carry the same `attempts`. Outside a fault window, no attempt is counted for a delivery that never reached `queue()`. Retried messages come back. Poison stops at `max_retries + 1` and reaches the dead-letter queue |
| D4 | Every acknowledged D1 insert is there at the end |
| KV | Only what docs/kv.md documents: a read sees the workload's own last acknowledged write (or a later one) and nothing never written, across restarts, moves and takeovers, and after healing. A key put with `expirationTtl` 60 s is there until it expires and gone after, in real time shifted by the writer's and reader's clock offsets |
| D5 | Convergence: after healing, the deployment is active and serving within 60 s, both queues drain within 120 s, and the lock answers |
| D6 | Availability: reported. Gated only outside fault windows (+60 s): ≥ 90% of ops succeed |
| D7 | No workerd outlives its hostd: 10 s after a `crash-hostd` restart, none of the crashed hostd's workerds is still running (#432) |

## Cron triggers and alarms (issue #419)

`TW_DP_SCENARIO=cron` or `alarms` (`make e2e-dataplane-cron` runs both)
replaces `dp` with two workloads of its own in `testdata/cron`, on the
same cluster and nemesis (`cron_test.go`, checkers in
`check_cron_test.go`, ledger in `ledger/cron-alarms.md`):

- `cron`, stateless: three cron triggers. Nothing fences a stateless
  workload, so only hostd's heartbeat-freshness rule keeps a cut-off host
  from running them.
- `cronobj`, stateful: the same triggers, plus alarm chains on a plain DO
  (`Alm`), a facet (`FAlm`, whose alarm tailworks delegates to the Host DO)
  and an Agents SDK agent (`Sch`, `scheduleEvery(2)`). The client re-arms
  them on every new epoch, as storage doesn't move.

Every run reports `start`, a `tick` every 5 s while held, and `end` to
the recorder. `*/1 * * * *` holds every run 75 s, and `0-59 * * * *` holds
70 s in `ctx.waitUntil()` on minutes divisible by 3, so the next minute
comes due while a run is in flight. A move or takeover moves both
workloads. The stateless one goes without `--replace` on a takeover, so
mgmt waits out the old host's lease. Four extra faults:

| Fault | What it does |
|---|---|
| `clock-back` | step every host's wall clock back 70–150 s, hold it 70–100 s (half the time restarting hostd half way), then step it forward again. The binaries are a `twtestclock` test build, and hostd follows `TW_TEST_WALL_CLOCK_FILE` (#564). workerd keeps the true clock: cron is due by hostd's |
| `clock-forward` | the same, stepped forward 70–150 s |
| `pause-boundary` | SIGSTOP the host 2–8 s before a minute boundary, for 10–20 s (under `FenceAfter`) or 32–40 s (past it, under the lease) |
| `reschedule` | redeploy both workloads in place with `* * * * *` swapped for `*/2 * * * *`, or back |

| ID | Invariant |
|---|---|
| C1 | At most one run per (workload, trigger, scheduled minute), across all hosts. Note, not failure: a rerun on the host a workload moved to, with a clock step between (#600). A rerun after hostd restarted under a clock step fails (#594, fixed) |
| C2 | No two runs of one trigger overlap: no run reports anything more than 1 s after a later run of the trigger started |
| C3 | No run starts on a host cut off from mgmt for more than `FenceAfter` (by `scheduledTime`), or on a host after the workload was seen active elsewhere (5 s for a live move). Every due minute runs, unless the trigger's previous run is in flight or the minute is in a fault window (+30 s) |
| C4 | After a schedule change is active, a trigger only the old schedule has never fires, and a new one fires at its first matching minute |
| A1 | No alarm fires on a host after the workload was seen active elsewhere, and no alarm (chain, seq) fires on two hosts. A set alarm fires unless the object moved or was re-armed before it was due (extended over fault windows). `scheduleEvery` has no gap over 10 s outside fault windows |
| C5 | Convergence: after healing, both workloads are active, every trigger runs and every alarm class fires |

`TestCronCheckers` plants one violation of each in a synthetic history.
`TW_DP_REPLAY=<dir> go test -tags e2edataplane -run TestReplayCron
./e2e/dataplane/` re-checks a recorded run.

Not yet: hosts whose clocks *differ* (each host its own offset). The
harness gives every process the same environment, so the clock faults step
every host together. #600 already shows the cross-host rerun.

## Deploys (issue #421)

`TW_DP_SCENARIO=deploys-hosts|deploys-mgmt|deploys-mixed` (`make
e2e-dataplane-deploys`, nightly with the date as the seed) runs another
workload, `testdata/deploys` (`dpd`): one Durable Object class, so the
lease fences. Every answer to `GET /info` carries the epoch, the bundle's
`BUILD` var, a random ID of the Durable Object copy, and the first 16 hex
digits of the SHA-256 of each secret value, as both the Worker and the
Durable Object see them. Values never leave the Worker; the history holds
digests. State: `ledger/deploys.md`.

- **Clients**, each one op at a time, mostly through its own replica:
  three deployers (sticky deploys, rollbacks naming an earlier deployment
  that ran, preview deploys with aliases `pa` and `pb`, promotions with and
  without `keepPreview`), and two secret writers (SEC_A and SEC_B one writer
  each, SEC_C both; puts and deletes; one writer also puts SEC_P on a
  preview's own name, which no deployment may get). Every deploy uploads a
  bundle of its own (`BUILD` = `b2.17`), so an answer names its deploy.
- **Probes** ask every endpoint ever seen every 400 ms; a watcher lists
  every workload's history every second.
- **Faults** (`deploys_test.go`): `deploy-kill-host`, `deploy-pause-host`
  and `deploy-cut` start a deploy and break its host 0–1.5 s later;
  `deploy-pause-takeover` and `deploy-cut-takeover` also take over on
  another host with a deploy or rollback `--force-takeover`; `deploy-move`
  is a live `--replace --force-takeover` move with a new bundle;
  `deploy-race` fires a deploy and a rollback through two replicas at
  once; `deploy-kill-mgmt` SIGKILLs the replica serving a deploy 0–300 ms
  after sending it; plus `kill-workerd`, `restart-mgmt` and `pg-fault`.
  The `skew` fault (Linux) is not in the deploys mix yet (ledger H2).

| ID | Invariant (`check_deploys_test.go`) |
|---|---|
| V1 | One active deployment per workload: once an epoch has answered, no older epoch answers a request sent after that, on any host (a live move is allowed 5 s) |
| V2 | Epochs are unique and grow with creation per workload; each acknowledged deploy has its own; an epoch answers only from the host its deployment is on |
| V3 | Every answer comes from a copy of a deployment created before that copy answered, running that deployment's bundle; once a newer copy answered on a host, no older copy there answers a later request. The previous copy answering at the new epoch while the new one starts is a reported handover |
| V4 | Secrets: an answer holds no value overwritten or deleted (and acknowledged) before its deployment was created; Worker and Durable Object agree; SEC_P never appears; every `secret_release` audit went to the host holding that workload's epoch (previews: with `previewOf`); a redeploy after the last write sees the last acknowledged values; no value is in any log, audit record, or mgmt's store (`pg_dump`) |
| V5 | History: records never vanish or change; a rollback's record names the deployment the client named and has its bundle; every acknowledged op is in the history and every record comes from an op; every record has its `deploy`/`rollback`/`promote` audit |
| V6 | Convergence: after healing, each workload's newest deployment is active and serves its bundle at its epoch within 60 s |

`TW_DP_REPLAY=<results dir> go test -tags e2edataplane -run
TestReplayDeploys ./e2e/dataplane/` re-runs V1–V5 on a recorded run.

## R2 (`TestR2`, issue #420)

The same cluster runs `testdata/r2` with the `fs` or an `s3` backend (a
Garage container, so Docker is needed), in the scenarios `r2-fs`, `r2-s3`
and `r2-replicas`, with three more faults (`cut-s3`, `slow-s3`,
`kill-mid-put`) and invariants R1–R6. `make e2e-dataplane-r2` runs all
three (nightly too); `TW_DP_SCENARIO=r2-s3` and the other `TW_DP_*`
variables pick one, as above, with `-run TestR2`. The rig, the invariants
and the results are in [ledger/r2.md](ledger/r2.md).

## Routing (issue #422)

`TestRouting` (`routing_test.go`, `check_routing_test.go`) runs the same
cluster with three stateful workloads (`testdata/routing`): `rt-a` (public
routes, and a service binding to B), `rt-b` (the binding's target, fetch
and RPC) and `rt-ws` (a Durable Object echoing on hibernatable
WebSockets). Every answer carries the answering copy's (host, epoch, ver).
Its ledger is [ledger/routing.md](ledger/routing.md).

```sh
make e2e-dataplane-routing                       # routing, 4 min, today's seed
TW_DP_SCENARIO=bindings TW_DP_SEED=3 make e2e-dataplane-routing
TW_DP_REPLAY=<results dir> go test -tags e2edataplane -run TestRoutingReplay ./e2e/dataplane/
```

- **Clients:** a public client that drives netd's public listener directly
  (`tw-hostd -test-public-listen-dir`, `-local-dev` only: each public node
  listens on loopback and its conns are Funnel conns; Funnel itself is out
  of scope), a tailnet caller `rt-cli` (A, B, the binding paths, the
  WebSockets, probes of every endpoint and listener ever seen), and
  `rt-rec`, which A tells about every `/gate` request before answering.
- **Faults:** the table above's host faults, run against the host of one
  of the scenario's workloads, plus `move` (of A, B or the DO workload),
  `paths` (redeploy A with `/gate/*` added to or removed from its public
  routes), `revoke-binding` (undeploy B, redeploy it 8–12 s later) and
  `hibernate` (the WebSocket clients go quiet past the object's idle
  eviction).
- **Scenarios:** `public`, `bindings`, `websockets`, `routing` (all of
  them). `TW_DP_WORKLOADS=rt-b` narrows which workloads the faults hit.
- **Testcontrol names:** a background loop keeps MagicDNS names unique in
  testcontrol, as real control does (mgmt's device deletes don't reach it,
  and a node rejoining from saved state can come back as a new node).
  Without it a binding call after a move back could reach a dead node.

| ID | Invariant |
|---|---|
| N1 | No request (public, tailnet, binding, WebSocket) is answered by a copy past its fence: once a newer epoch of a workload has answered, an older one doesn't answer a request sent after that (live moves: 5 s). After B moves, binding calls reach its new copy within the 5 s cache TTL + 15 s of it answering directly |
| N2 | A revoked binding (B undeployed) stops answering within the cache TTL (5 s) + 1 s |
| N3 | B sees the calling A copy's node on every binding call (fetch and RPC) and `rt-cli` on direct calls; A sees `rt-cli` on tailnet requests and a public requester on public ones |
| N4 | An unlisted public path never reaches workerd; a tailnet-only path over the public listener is always `404 not_public`; a route change applies within 10 s |
| N5 | Per WebSocket: replies in order, no duplicates, none skipped (hibernation included); a message left unanswered has its connection closed within 45 s, or 15 s after its host's stall or cut ends |
| N6 | Availability per path, reported; ≥ 90% outside fault windows (+60 s). Recovery time per path after each fault is in the notes |
| N0 | Convergence: after healing, every workload active and every path answering within 60 s |

## Each iteration

1. Read `LEDGER.md` and pick one item: a harness fix, an open finding to
   re-check, or the next seed of a scenario that has fewer than 3
   consecutive clean seeds.
2. Run it. Artifacts go to `results/<stamp>-<scenario>-<seed>/`:
   `history.jsonl`, `report.json`, `faults.json`, `store.json`, the
   process logs and `pids`.
3. Classify the result: `CONFIRMED` (with the invariant and evidence),
   `REFUTED`, `FLAKY` (rerun the seed up to 3 times), or `HARNESS-BROKEN`.
4. For CONFIRMED, find the root cause (`file:line`), file a
   `dataplane-chaos` issue, and add it to the ledger.
5. Update the ledger's status table and run log.

**Done** (issue #415): every scenario passes 3 consecutive seeds, plus 2
clean 10-minute `mixed` soaks; every confirmed finding has a deterministic
regression test and a ledger entry.

## Containers, pooled nodes and snapshot restore (#423)

The same cluster with a microVM backend on every host, so it needs a Mac's
virtualization (vz, no sudo) or Linux + KVM (Firecracker + jailer, as
root). Not in the nightly. Ledger: `ledger/containers.md`.

```sh
make e2e-dataplane-vm CTR_IMAGES=<dir with images.json> TW_DPK_SCENARIO=containers TW_DPK_SEED=7
```

Files: `containers_test.go` (workload, observers, faults) and
`check_containers_test.go`, build tags `e2edataplane e2edataplanevm`.
The workload (`testdata/containers/ctr`) has one class, `Box`: each object
owns a container and its own node (taken from the pre-joined pool). The
client creates objects in bursts of `TW_DPK_BURST` (3), touches a random one
every second (`up`: start the container unless it runs, read its `boot_id`
and 16 bytes of urandom, report them to the recorder from the object's
own node), and retires the oldest past `TW_DPK_MAX_OBJECTS` (6) with
`destroy()` and a kill. `testdata/containers/vm` is a microVM-tier workload
deployed `TW_DPK_VMS` (3) times at once, so its VMs come from the warm pool
and the base snapshot together (Firecracker; vz cold-boots). The test
polls every host's status socket each second and mgmt's object list every
3 s.

Faults, besides the shared `kill-host`, `move`, `pause-takeover`,
`cut-takeover`, `cut-short` and `restart-mgmt`:

| Fault | What it does |
|---|---|
| `api-stall` | the fake Tailscale API answers 503 or 4 s late for 5–20 s, during a burst |
| `burst-crash` | a burst of new objects, then SIGKILL the whole host 0–1.5 s in |
| `crash-hostd-vmm` | SIGKILL hostd alone; 15 s after the restart, none of its old VMMs may run |
| `kill-vmm` | SIGKILL one VMM (`tw-vz`, `firecracker`) under the holder's hostd |
| `redeploy` | a blue/green redeploy on the same host (containers must not roll) |
| `vm-burst` | deploy `TW_DPK_VMS` microVM workloads at once, read their first random draws, undeploy |

Scenarios: `containers`, `pool`, `restore`, `mixed` (`kscenarios`).

| ID | Invariant |
|---|---|
| K1 | One container per Durable Object: no host but the lease holder runs an object's container more than 45 s (FenceAfter + a heartbeat + slack) after the lease or the last fault left it. A copy at an older epoch never answers after a newer one did. A container changes only across a fault, a move or the client's own `destroy()`, never across a redeploy. `destroy()` returns within the SIGTERM grace cap (3 s) + 5 s, unless the host was under a fault (stopped, or the container still booting after a restart). An `up` that answers 200 without the object's report is an error, and K1 notes it |
| K2 | Single adoption: no two live objects share a stable node ID or a MagicDNS name; a pool node goes to at most one object; every report reaches the recorder from the object's own node under the name it read back; nothing is `pending`, `minted` or `assigned` at quiescence |
| K3 | Uniqueness: no two containers share a `boot_id`, no urandom draw repeats, no two microVM copies share workerd's first `randomUUID`, `getRandomValues` or `Math.random`, and each copy's egress leaves from its own node |
| K4 | No leftovers at quiescence: no VMM outlives a hostd crash or runs outside every hostd, no host runs more VMMs than its containers, microVMs and pool account for, no host runs a node for an object mgmt has ended, every ready pool node runs on a host, and the strict keyscan of each running container finds no parent key |
