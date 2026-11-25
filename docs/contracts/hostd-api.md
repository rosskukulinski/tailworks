# Contract: the tw-hostd API

tw-hostd, the host agent, serves a small hand-written HTTP API. It is not
part of `api/openapi.yaml`: that spec is mgmt's API
([mgmt-api.md](mgmt-api.md)), which hosts and operators call. This file is
the other direction: mgmt → hostd pushes and calls, and the operator's
calls to the host agent on their own machine.

hostd's routes share mgmt's `/v1` prefix but are a separate API. They stay
where they are: moving them would break mgmt ↔ hostd compatibility during a
rolling upgrade ([ADR-041](../design/adr/041-upgrades-and-releases.md)).

## Route table

hostd serves the routes on three surfaces:

- **socket**: the local unix socket `<run-dir>/hostd.sock` (mode 0600,
  `procsb.Guard`: no sandboxed workload may connect). The operator on this
  machine uses it, and so does a local-dev mgmt (`pushURL: unix://…`).
- **tailnet**: the host's own tailnet node, port `-push-port` (7474). Every
  request must come from mgmt's node (see
  [Push API authentication](#push-api-authentication-m12-h1)).
- **inproc**: `hostd.Host.PushHandler()`, which mgmt calls directly when it
  runs in the same process as the host (`tw up`'s default single-process
  mode; see [Embedded host](#embedded-host-single-process-mode-m13)).

| Method | Path | Surfaces | Caller | Purpose |
|---|---|---|---|---|
| `GET` | `/metrics` | socket | the operator (a local scrape) | Prometheus metrics. The tailnet scrape is a separate listener (`-metrics-port`, 9464) |
| `GET` | `/v1/approvals` | socket, tailnet, inproc | mgmt, for `tw approvals` and the admin UI | List pending approval requests on this host |
| `POST` | `/v1/approvals/decide` | socket, tailnet, inproc | mgmt, for `tw approve` (and `--reject`) and the admin UI | Decide one approval request |
| `GET` | `/v1/logs` | socket, tailnet, inproc | mgmt, for `tw logs` from a linked machine | One workload's app log (`?tenant=&workload=&n=&follow=1`), from the lease holder |
| `POST` | `/v1/purge` | socket | `tw down --purge` | Forget killed objects (their tombstones), so a redeploy on this state dir starts clean |
| `POST` | `/v1/push/assignments` | socket, tailnet, inproc | mgmt, after a deploy, move or public change | Sync assignments now instead of at the next heartbeat |
| `POST` | `/v1/push/kill` | socket, tailnet, inproc | mgmt, for `tw kill` (tier 1) | Kill one object now (`{objectID, reason}`) |
| `POST` | `/v1/push/probe` | socket, tailnet, inproc | mgmt, at deploy (smart placement, [ADR-033](../design/adr/033-smart-placement.md)) | Measure a tenant's targets from this host and answer with the report |
| `POST` | `/v1/push/upgrade` | socket, tailnet, inproc | mgmt's upgrade runs (`tw upgrade`) | Install a release ([Self-upgrade push](#self-upgrade-push-adr-041)) |
| `POST` | `/v1/store-admin` | socket, tailnet, inproc | mgmt, for `tw d1 execute`, `tw d1 migrations apply`, `tw d1 export` | Run an operator op on a platform store in the workload's workerd |
| `GET` | `/v1/status` | socket | `tw status`, `tw up`, `tw doctor`, `tw logs` | The host's status dump: tenants, node IDs and IPs, PIDs, dirs, ports |
| `POST` | `/v1/teardown` | socket | `tw down --purge` | Close the create gate (no pool refill, no new object) and wait for creates in flight, so the list `down` takes next is complete |
| `POST` | `/v1/vm/check` | socket | `tw host vmcheck` | Check a workload's microVM: guest stats, DO volume fsync latency, the no-keys-in-guest scan |

The routes on all three surfaces are `pushHandlers` in `hostd/http.go`; the
socket-only ones are added in `pushAPI`. Errors are typed
`{code, message, detail, doc_url}` with a `tw-error` header, as in mgmt's
API.

## Push API authentication (M12, H1)
The tailnet surface serves only the routes the [route table](#route-table) lists for it.

The tailnet surface authenticates every request by WhoIs on the host's node (`hostd.Config.WhoIs`, set from the tsnet `LocalClient`); hostd refuses to start a tailnet push listener without it. A push is accepted only when:
1. WhoIs resolves the peer (no identity → refused), and
2. the peer's node carries `tag:tw-mgmt` (`hostd.MgmtTag`), and
3. the peer's stable node ID is the one hostd pinned as mgmt's: at `Start` hostd resolves the `-mgmt` address (a tailnet IP → WhoIs; a MagicDNS name → the netmap via `Config.TailnetStatus`) and pins that node if it carries the tag. If the address is not resolvable yet, the first `tag:tw-mgmt` pusher pins it. A later push from another `tag:tw-mgmt` node is accepted only if the `-mgmt` address now resolves to it (mgmt's node was recreated; re-resolved at most every 2 s), and re-pins.

Refusals are `403 {"code":"push_unauthorized","detail":…}` with `tw-error: push_unauthorized`, and one log line per refusal carrying the peer address and its tags (never key material). The socket-only routes (`GET /v1/status`, `POST /v1/purge`, `POST /v1/teardown`, `POST /v1/vm/check`, `GET /metrics`) are not routed on the tailnet surface at all (404 even for mgmt): the status dump (tenant IDs, node IDs/IPs, PIDs, dirs, ports) is the operator's.

## Self-upgrade push (ADR-041)

`POST /v1/push/upgrade {runID, version, name, url, sha256}` is on every push
surface. mgmt's upgrade runs send it, and only to a host that advertises the
`self-upgrade` feature: one started with `tw-hostd -self-upgrade`, under a
service manager.

1. The host answers `202 {"ok": true, "installing": "<version>"}` at once.
   A repeated push while an install runs gets the same answer.
2. It downloads `name` from mgmt (`GET /v1/releases/{version}/artifacts/{name}`,
   host role).
3. It checks the SHA-256, extracts the tarball (refusing entries that escape
   it), and runs its `install.sh --prefix <install prefix> --force`. The
   prefix keeps the previous release as `.prev`.
4. It exits with status 75 for its service manager to start the new binary.

Any failure is logged. The host keeps running the old release, and the run's
wait gate times out and pauses it. A host without `-self-upgrade` answers
`400 bad_request`.

## Embedded host (single-process mode, M13)
The default `tw up` runs mgmt and the host agent in **one process** (`tw __supervise`; `tw.json` `"mode": "single"`): mgmt's tsnet node is the only infrastructure device, the host has no node of its own, no join key and no registration secret. `tw up --separate-host` keeps the two-process layout above; `tw host add` machines are unchanged.

**Transport** (`internal/inproc`). hostd's `mgmtapi.Client` gets an `http.RoundTripper` that serves each request through mgmt's `/v1` handler (`mgmt.Server.Handler()`, i.e. *below* `TailnetHandler` / `LocalHandler`) in a goroutine with a piped body, so long polls (`POST /v1/objects/{id}/credentials?wait=`) and bundle downloads stream and honour the request context. The request context carries:
- `mgmt.WithPrincipal(ctx, Principal{Kind: "host", HostID: <hostID>})`: the host role with its ID, so the role gate, the `/v1/hosts/{id}` ID check, `requireLease` and `hostScope` apply exactly as for a host on its own tailnet node (an admin route is `403 forbidden`, another host's routes `403`); and
- `mgmt.WithEmbeddedHost(ctx, <hostID>)`: the embedded-host marker. It is a context value, so no listener can supply it: `TailnetHandler` and `LocalHandler` attach only a `Principal`.

`RemoteAddr` is the sentinel `inproc` (no `host:port`, so `RegisterHost` derives no node address).

**Registration.** The embedded host registers with `push: {url: "inproc://"}` (`mgmt.EmbeddedPushURL`). `RegisterHost` accepts it only when the context's embedded-host marker equals the host ID and the caller has no node ID; otherwise `403 host_identity_mismatch` with a `deny` audit record, whether the request came over the tailnet (a `tag:tw-host` node) or the local-dev socket. A record already bound to a tailnet node is never taken over (`403`; `tw host rm` first). The stored `Host` has `nodeID: ""`, `pushPort: 0`; the `host_register` audit detail carries `"transport": "embedded"`. `GET /v1/hosts/{id}` shows the host `live` by heartbeat as usual, with an empty `push` object (no port, no unix URL).

**Push.** `mgmt.Mgmt.SetInProcessPush(hostID, h)` registers the embedded host's push handler (`hostd.Host.PushHandler()`: the routes the [route table](#route-table) marks inproc, no socket-only route). A push to a host whose target is `inproc://` is served through that handler with a `PushTimeout`-bounded context and counted as delivered on a 2xx; nothing is dialled, and no `tag:tw-mgmt → tag:tw-host` grant is involved. hostd's local socket `<run-dir>/hostd.sock` is still served for the operator (`tw status`, `tw down --purge`).

**Lifecycle.** `tw __supervise` starts mgmt (`internal/mgmtd`, the same runtime as the `tw-mgmt` binary), builds the host (`internal/hostdrun`, the same as `tw-hostd`) with the in-process client, registers the push handler, then starts the host. SIGTERM stops the host first (nodes close in parallel), then mgmt (flushes pending device deletes). Each component logs to its own file (`<state>/logs/mgmt.log`, `hostd.log`).

## hostd ⇄ platform wrapper (local, per workerd instance; not a network API)
`hostapi.sock`, which only the trusted platform worker can reach (as the `HOSTD` binding).

**Ops.** `POST /v1/{ensure,spawn,ready,kill,audit,self}` with JSON `{as?:{class,name}, …}`. `as` is the calling object, attested by the platform's `TailnetApi` props.

**Errors.** Typed `{code, message, detail, doc_url}` with a `tw-error` header.

**Egress sockets.**
- `egress-obj.sock` (facets): `x-tw-class`, `x-tw-name`, `x-tw-ts`, `x-tw-nonce` and `x-tw-mac`.
  - The MAC is HMAC-SHA256 under a per-process key over `class\nname\nmethod\nurl\nts\nnonce`, with ±2 min skew allowed.
- `egress-wl.sock`: the Worker and plain-DO objects. This socket is the workload node's identity.
