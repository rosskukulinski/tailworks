# More than one machine

One `tw up` machine runs everything: mgmt, the host agent and your apps'
nodes. This page is for when one machine is no longer enough: adding
hosts with `tw host add`, how mgmt picks the host for a workload, and how
many objects a host holds. Installing the first machine is in
[INSTALL.md](INSTALL.md); more than one mgmt replica is in
[postgres.md](postgres.md).

## One process by default; scale out with `tw host add`

`tw up` runs **one process** by default: the supervisor hosts tw-mgmt
and the host agent together, so mgmt's node `tw-mgmt` is the only
infrastructure device tailworks adds to your tailnet (`tw.json`:
`"mode": "single"`). The embedded host talks to mgmt in-process as the
`host` principal (every per-host and per-lease check applies as for a
remote host) and mgmt delivers kills in-process; no host node, no join key,
no `tag:tw-host` grant is needed for this machine. `tw status` says
`host <id> (embedded in the mgmt process)` and `tw doctor`'s `transport`
row `ok tsnet: … host embedded in the mgmt process`. The policy snippet
`tw up` prints still includes `tag:tw-host` and the host ⇄ mgmt grants:
they are what **scale-out** needs. `tw up --separate-host` keeps the
two-process layout (tw-hostd as its own child on its own `tag:tw-host`
node); a state dir keeps the layout it was created with (state from before
this option stays two-process). Object nodes are the same in both layouts.

Add a second machine as a host with `tw host add <host-id>`: mgmt records
it (pending) and writes a join file (0600) with a single-use join key, the
one-time registration secret, and the host ID, mgmt's URL and the control
URL (plus key-tags-only on Headscale). Copy it to the new machine and run

```sh
tw-hostd -join-token-file <host-id>.join.env
```

No other flags are needed: tw-hostd joins with the key, deletes the file
and keeps the settings beside the node state (`<data-dir>/tsnet/join.env`),
so later starts need no flags either. A flag still wins over the file. To
run it as a service, use systemd with `Restart=always` and add
`-self-upgrade`. `tw-hostd join --token-file <file>` (join only, then
exit) reads the same file.
`tw host ls` / `tw host rm <id> [--force]` manage hosts; `tw tenant set`
(PATCH) and `tw tenant rm`, `tw undeploy <workload>` round out the CRUD.
More hosts still share one mgmt; for more than one mgmt replica, see
[When to switch to Postgres](INSTALL.md#when-to-switch-to-postgres).

## Where a workload runs (placement, multi-host)

With more than one host, mgmt picks the host at deploy
([ADR-033](design/adr/033-smart-placement.md)):

1. **Eligible hosts** are live (a heartbeat in the last 10 s), in the tenant's
   pool (`tw tenant set --pool`), offer the workload's tier (`"tier":
   "microvm"` in `tailworks.jsonc`, or the tenant's: only hosts with KVM and the
   helper, `tw host ls` TIERS) and have free capacity.
2. `tw deploy --host <id>` pins one (it must still be eligible).
3. Otherwise a workload that is **already running stays where it is**. A
   better host appearing never moves it.
4. Otherwise (first deploy, or its host died) **smart placement**: if the
   app declares `tailnet.connects` (or `placement.targets`), the host with
   the best measured path to those peers wins: direct WireGuard, then a peer
   relay, then DERP, then unmeasured; then the lowest RTT. Ties go to the
   host with more free capacity. Without targets it's the most free capacity.

```jsonc
// tailworks.jsonc
{
  "tailnet": { "tags": ["tag:api"], "connects": ["tag:db"] },
  "placement": { "mode": "smart" }   // the default when connects is set; "off" to disable
}
```

Advanced: `"placement": { "targets": ["pg-primary"] }` places close to other
peers than `tailnet.connects`, and with several targets `"aggregate"` picks
how their paths combine: `"worst"` (the default) or `"sum"`
([tailworks-jsonc.md](tailworks-jsonc.md#placement)).

```
$ tw deploy
✓ placed     placed on h2: tag:db direct 1.8 ms (h1: relayed via DERP nyc 24 ms, h3: unmeasured)
✓ deployed   api@h2 · …
```

**Where the measurements come from:** each host measures, every 60 s
(`tw-hostd -path-every`), the targets of the workloads it runs, from that
tenant's own nodes on the host. It resolves the tag or name in the node's
netmap and sends WireGuard disco pings; no keys are minted to measure. At
deploy, mgmt asks hosts without a fresh measurement to probe (≤ 2 s). A
host that runs nothing of the tenant has no vantage point and stays
"unmeasured", so in a new tenant the first workload goes by capacity. Pin
it (`--host`) next to the database once, and later deploys can measure.
Measurements expire after 5 min and live in mgmt's store, so every replica
places alike.

**Moving a running workload:** `tw deploy --replace` re-places it. It moves
only if another eligible host is clearly better: a better path class, or
≥ 2 ms and ≥ 20 % lower RTT. The lease moves at the next epoch in one
conditional write, the new host starts it, and the old host stops at its
next heartbeat. A stateful workload (Durable Objects without `"fence":
"never"`) also needs `--force-takeover`: its DO storage stays on the old
host's disk. `tw ls` shows each workload's placement reason.


## Sizing: how many objects a host holds

Each object with its own node costs the host about **2.5 MB idle and 4 MB
under request load** (measured on Linux and macOS at 100 and 500 nodes,
when objects can't see one another), on top of its workload's own memory.
A host advertises a node capacity to placement and takes no more objects
than that. By default the capacity comes from RAM: a quarter of it at 8 MiB
per node, between 50 and 1000. So an 8 GiB host takes 256 objects, a 16 GiB
host 512, and 32 GiB or more 1000. Set `tw-hostd -capacity-nodes N` to
override it. Lower it if your policy lets objects see one another: then
every node holds every other in its netmap, and at 100 nodes each costs
5–12 MB. Don't give hundreds of objects on one host that visibility. The
measurements, and the command that reproduces them, are in
[limits.md](limits.md#per-node-memory-measured).
