# Tailscale Services

tailworks uses [Tailscale Services](https://tailscale.com/kb/1552/tailscale-services)
in two places:

- **workloads:** a workload's entry node (its default export) can host a
  Service. Callers then use a stable name and VIP that don't change when the
  workload node is re-minted, redeployed or moved to another host;
- **mgmt replicas:** several `tw-mgmt` replicas on one Postgres can host one
  Service (e.g. `svc:tw-mgmt`), so hosts reach whichever replica is healthy
  (H12 in `e2e/multimgmt/LEDGER.md`).

Durable Object instances never host Services. A DO is single-instance, and
its node *is* its identity: per-object egress attribution, `tw kill`, and
spawn lineage all hang off that node.

## How it works (`internal/tssvc`)

`tsnet.Server.ListenService` forwards Service traffic to a host loopback
socket. The backend then sees `127.0.0.1` as the caller, or has to trust a
PROXY header that any local process could forge. Tailworks authenticates every
request with WhoIs on the connection's source address, so it can't use that.
Instead, tailworks:

1. advertises the Service in the node's prefs (`AdvertiseServices`);
2. adds an **empty** TCP handler for the Service port to the serve config.
   Netstack then intercepts flows to the VIP, but serve has nothing to do
   with them (no `TCPForward`, no HTTP), so they fall through to tsnet's
   fallback TCP handlers;
3. registers a fallback handler that hands VIP flows (any destination that
   isn't the node's own address, on an advertised port) to an in-process
   listener.

The `net.Conn` keeps the caller's real tailnet address, so netd's attributed
ingress (WhoIs, `request.cf.tailscale.caller`, cap narrowing) works exactly
as it does on the node's own address. Kill RSTs Service flows like any
other flow.

**App capabilities follow the address the caller dialed.** netd and mgmt
resolve the caller with `tssvc.WhoIs`, which asks for the caps that apply to
the connection's local address (`WhoIsForIP`):

- on a flow to the Service VIP, the caller has the caps of grants whose
  `dst` is the `svc:` name (or covers the VIP), and *not* those of grants to
  the host node's tags or address;
- on a flow to the node's own address, it is the other way round.

Plain `WhoIs` would compute caps for the node's own address on every flow:
on a VIP flow it misses the `svc:` grant's caps and shows the node grant's
caps the caller didn't use (H22, measured below).

## Workloads

```jsonc
// tailworks.jsonc
{
  "name": "support",
  "tailnet": {
    "tags": ["tag:tw-wl-support"],   // required: Service hosts are tagged nodes
    "service": true                    // svc:support; or "svc:helpdesk" / "helpdesk"
  }
}
```

**What `tw deploy` does:**

- **Preflight** (dry run): checks that the policy's
  `autoApprovers.services` lets the workload tags host the Service. If it
  doesn't, control would never route the Service to the node, so the deploy
  is refused with the entry to add:
  ```jsonc
  "autoApprovers": { "services": { "svc:support": ["tag:tw-wl-support"] } }
  ```
  `tw policy --apply` (or `tw deploy --apply-policy`) writes this entry
  for you: only the workload's own `svc:` name, approved only for the tags
  the workload node is deployed with. On Headscale, or when those tags
  aren't defined, it stays a line for you to add.
- **Service definition:** mgmt creates it (port `tcp:80`, the attributed
  HTTP ingress) with the comment `managed by tw: tenant=<id> workload=<name>`.
  - A redeploy adopts its own definition.
  - A Service with any other comment is refused with `409 service_conflict`.
    One tenant or workload can never take over another's name on a shared
    tailnet. To hand an existing Service to a workload, set its comment to
    that exact string.
  - A name that would share its MagicDNS name with an existing device is
    also refused, because MagicDNS answers the device first. This check is
    best effort: it's skipped if the credential can't list devices.
- **Credential:** the OAuth client needs **Services › Write**; without it
  the deploy fails with `502 service_api_failed` and says so.
- **Output:** the deploy prints
  `✓ service svc:support · http://support.<tailnet>/`, and
  `GET /v1/deployments/{id}` has `endpoint.service`.

**Callers need a grant to the `svc:` name**, for example
`{"src": ["tag:agent"], "dst": ["svc:support"], "ip": ["tcp:80"]}`.
App capabilities go in that same grant
(`"app": {"example.com/cap/support": [{"role": "agent"}]}`): a call through
the Service sees exactly those in `caps()`. A grant to the workload's tags
(`"dst": ["tag:tw-wl-support"]`) applies only to calls to the workload
node's own name or address, so a caller that uses both needs its caps in
both grants.

**When the host advertises the Service (`hostd/service.go`).** Tailscale
doesn't health-check Service hosts, so each host gates its own
advertisement. It advertises only while all of these hold:

- the deployment is active;
- its instance (workerd, or the Node app) answers health probes, every 2 s
  with a 1.5 s timeout;
- the workload node is ready.

It withdraws the Service:

- on undeploy, and so on self-fencing and when mgmt moves the workload to
  another host;
- after 2 failed probes in a row (one good probe brings it back);
- on a redeploy without `tailnet.service`;
- with the node, on `tw kill` or a close.

Withdrawing removes the serve-config entry, so the node stops intercepting
the VIP at once. It also turns off keep-alives on Service connections: idle
ones close, and active ones answer `Connection: close`. Callers' pooled
connections therefore re-resolve instead of sticking to a host that no
longer serves. Requests already in flight finish; a kill RSTs them.
`tw-hostd`'s status shows `Service {name, advertised, healthy, reason}` per
workload.

**The definition outlives the deployment.** `tw undeploy` and `tw down`
withdraw the advertisement but keep the definition, so the VIP stays stable
for the next deploy. Delete it in the admin console (or via the API) when
the workload is gone for good.

**Egress to Services.** An object's `fetch("http://support.<tailnet>/")`
(or the bare `http://support/`, or the VIP itself) resolves from that
object's own netmap: the Service's MagicDNS record, routed by a peer that
has the VIP in its AllowedIPs. Host DNS is never used. If every host of
the Service is an object on the same host, netd delivers the call in
process to that object's Service ingress (RFC §4.8), after the same checks:
the caller sees the Service in its netmap, and the host's packet filter
accepts it.

### Replicas: one stateless workload on several hosts

```jsonc
{
  "name": "web",
  "replicas": 3,                       // 1..16, default 1
  "tailnet": { "tags": ["tag:tw-wl-web"], "service": true }
}
```

This is Cloudflare's split. A plain Worker is stateless, so it can run in
many places. A Durable Object must run in exactly one place, so **only
stateless bundles may have replicas**: no DO classes, or every class marked
`"fence": "never"`. Anything else is refused with `400 bad_bundle`, both at
the preflight and at the deploy.

- **Model:** replica *i* is deployed as its own workload,
  `<workload>--r<i>` (`web`, `web--r2`, `web--r3`). Each replica keeps its
  own lease and epoch, deployment record, assignment, workload node and
  identity, so all the single-host machinery (fencing, kill, ready,
  credentials) applies unchanged. `"replicas": 1` is exactly the old
  behavior. The `_workload` namespace records the group: `replicaOf` on
  replicas 2..N, and `replicas` on the workload itself.
- **Placement:** each replica goes on a distinct live host that offers the
  tier, sticking to its current host on redeploy. If there aren't enough
  hosts, the deploy fails with `503 no_host` ("replicas: 4 needs 4 distinct
  live hosts") before anything is written. `--host` can't be combined with
  replicas.
- **Service:** all replicas host the same Service, which belongs to the
  workload. Each host gates its own advertisement on its replica's health
  (above), so a stalled or failing replica drops out on its own.
- **Lifecycle:**
  - a redeploy with a smaller count undeploys the extra replicas, and
    undeploying the workload undeploys them all;
  - a replica can't be deployed by its own name (`409 replica_conflict`);
  - a name ending in `--r<n>` (n ≥ 2) belongs to replicas: a workload
    that is not a replica can't be deployed under one, nor can a preview
    (`--preview-alias pr7--r2`), with the same
    [`409 replica_conflict`](errors.md#replica_conflict). tailworks tells
    a replica by its name where it has nothing else: a replica's R2
    objects are stored with its workload's. A workload that an earlier
    release deployed under such a name keeps deploying until it is
    undeployed; rename it when you can;
  - a replica name that such an older, separate workload still uses is
    refused to the replica, also with `409 replica_conflict`;
  - `tw deploy` waits for every replica and prints
    `✓ replicas 3 · web@h1, web--r2@h2, web--r3@h3`.
- **Routing:** in the measured runs, Tailscale sent every fresh connection
  from one client to the same Service host (20 of 20). Replicas give
  failover and spread load *across clients*, not round-robin per
  connection.

For one-leader-at-a-time work among replicas (leader election), see
[locks.md](locks.md#leader-election-among-replicas).

Not done: the Cloudflare-style split for DO apps, where the Worker is
replicated and its DOs stay single-homed, with stubs forwarded to the
owning host.

## mgmt replicas (`svc:tw-mgmt`)

Replicas run `tw-mgmt` directly, not `tw up`. Moving an existing install's
store to Postgres first, and what each replica needs, is in
[postgres.md](postgres.md#several-mgmt-replicas-ha).

```sh
tw-mgmt -db-file pg.dsn -custody setec -setec-url … \
  -tsnet-hostname tw-mgmt-1 -tsnet-control-url https://controlplane.tailscale.com \
  -tsnet-service svc:tw-mgmt
# every host
tw-hostd -mgmt http://tw-mgmt.<tailnet> …
```

- **Naming:** each replica keeps its own node (`-tsnet-hostname`, e.g.
  `tw-mgmt-1`). mgmt refuses to start if the hostname equals the Service
  label, because MagicDNS would answer the node first.
- **What's served:** Service flows on `:80` reach the same
  WhoIs-authenticated handler as the node's own address. Verified:
  `/v1/whoami` through the VIP returns the caller's node ID. Not yet
  exercised end to end: a real `tw-hostd` registering and heartbeating
  through the VIP, and `tw link`. Both use the same WhoIs path.
- **Health gate:** `mgmt.ServiceGate` advertises while `Status` (the store)
  answers. It withdraws after 3 failed checks in a row (every 2 s, 2 s
  timeout), and one success restores it. On SIGTERM it withdraws before the
  listeners close. Withdrawing turns off keep-alives on the Service
  listener, so hosts' pooled connections move to another replica.
- **Policy:**
  ```jsonc
  "autoApprovers": { "services": { "svc:tw-mgmt": ["tag:tw-mgmt"] } },
  "grants": [
    {"src": ["tag:tw-host"], "dst": ["svc:tw-mgmt"], "ip": ["tcp:80"]},
    {"src": ["autogroup:admin"], "dst": ["svc:tw-mgmt"], "ip": ["tcp:80"], "app": {"github.com/rosskukulinski/tailworks/cap/admin": [{}]}},
  ]
  ```
  Create the Service definition (`svc:tw-mgmt`, `tcp:80`) in the admin
  console. `tw up --apply-policy` doesn't write `autoApprovers`.
  The admin cap in the `svc:tw-mgmt` grant counts on calls through the
  Service; a grant to `tag:tw-mgmt` counts only on calls to a replica's own
  node name.
- **Limitation:** a replica that is stalled outright (SIGSTOP, a wedged
  process) can't withdraw itself. Callers time out and retry, and a later
  connection may land on it again until control sees the node go offline.
  This is the S3 case in the multimgmt ledger.

## Measured (tailnet 1, production control, 2026-11-13)

| Check | Result |
|---|---|
| `internal/tssvc` real (`TestRealService`) | VIP answered 3.5 s after advertise; WhoIs saw the caller's node ID through the VIP; after withdraw the next fresh request failed (within one 3 s timeout); re-advertise answered in 14 ms |
| E2E-SVC (`make e2e-svc`, tailworks CLI) | `tw deploy` created the definition (owner comment, VIPs). First call through the VIP 11.1 s after deploy, caller = the client node. Redeploy kept the same VIPs and was served again at once. `tw kill` of the workload node: the VIP stopped answering on the next request (≤ 3 s). `tw down`: 0 leftover devices/keys; definition kept |
| E2E-REPLICAS (`TestE2EReplicas` in `make e2e-svc`; host A from `tw up`, host B a second `tw-hostd` joined with `tw host add`) | `replicas: 2` placed on both hosts. The Service answered 5.6 s after deploy, caller = the client node, 20/20 fresh connections to one replica. Stalling that replica's workerd (SIGSTOP): its host withdrew, and 5 consecutive answers came from the other replica 11.1 s later; re-advertised 0.2 s after SIGCONT. Scale to 1: the second replica was undeployed and the Service kept answering. 0 leftovers |
| H22 caps (`make e2e-svc-caps`, `TestRealServiceCaps`; tailnet 2, 2026-11-21) | Caller `tag:ttest`, host `tag:ttest-rt-svch`; one cap granted to `svc:ttest-rt-caps`, another to the host's tag. Through the VIP (IPv6 via MagicDNS, and the IPv4 VIP): `WhoIsForIP(VIP)`, `WhoIsForService` and `tssvc.WhoIs` = the `svc:` cap only; plain `WhoIs` = the tag grant's cap only. To the node's address: the tag grant's cap only, every way. With the tag grant removed (no tag-to-tag grant at all), plain `WhoIs` on the VIP flow had no caps and `tssvc.WhoIs` the `svc:` cap |
| H12 real (`make e2e-mm-svc`, 2 replicas on Postgres; needs `TW_MM_PG_DSN` pointing at a database of its own) | `svc:ttest-mgmt` answered 5.6 s after start; `/v1/whoami` through the VIP = the client node, admin. Serving replica's store reset → 5 consecutive answers from the other replica 8.2 s later; it re-advertised 0.5 s after its store came back. SIGTERM of the serving replica → steady on the other 7.7 s later |
