# ADR-054: A host can send all internet egress through a tailnet exit node; `tailnet.egress.via: "exit-node"` lets a workload require it

**Date:** 2026-11-23
**Status:** Accepted (2026-11-23; built on branch
`require-exit-node-egress-hosts-workloads`). Drafted as
`ADR-exit-node-egress`. Extends ADR-049 (Workers' internet
egress from the host's network) and ADR-032 D4 (containers'
`enableInternet`). Revisits the alternative ADR-049 rejected, "an exit node
per workload", with a different carrier: the host's own node.



- Internet egress (ADR-049) leaves from the **host's own network**:
  `hostd.DirectInternet()` is the host dialer and the host resolver, for
  Workers through `netd/internet.go` and for containers through
  `netd/sbxnet`. The only knobs are on/off and an allowlist:
  `tailnet.egress.internet` per workload, and `-worker-internet=direct|off`
  and `-container-internet=direct|off` per host.
- Nothing sets an exit node on any tsnet node. The one `EditPrefs` call in
  netd rewrites a hostname. The host machine's own `tailscale set
  --exit-node` doesn't apply either: tw-hostd's nodes are tsnet, and the
  direct dialer bypasses the machine's tailscaled anyway.
- ADR-049 rejected an exit node *per workload* for the on-by-default
  path: the flow would carry the object's identity, every tailnet would
  need an exit node and an `autogroup:internet` grant before `fetch()`
  worked, and netd "would lose sight of the resolved address", moving the
  SSRF guard to an exit node tailworks doesn't run. `DirectInternet()`'s
  comment and docs/containers.md both still say "no tenant exit-node or
  gateway path yet", so the path was deferred, not refused.
- Operators ask for it for the usual reasons: a **fixed source address**
  (ADR-049 says "which IP will my Worker call from?" has no stable answer
  across hosts), **one place to see and filter** every internet flow a
  fleet makes, hosts that **sit on networks with no direct internet**
  (a homelab VLAN, a locked-down VPC), and **name privacy** (ADR-049
  decision 4 accepts that the host's resolver sees every name a Worker
  looks up).
- The gateway recipe (docs/recipes.md "Call an internet API") covers one
  API at a time, and needs a grant per workload. It is not "everything
  this host sends to the internet goes through there".
- tsnet documents exit nodes as a supported path: set `ExitNodeID` with
  `LocalClient.EditPrefs`, and "dials of addresses outside the tailnet
  then go through the exit node; dials within the tailnet are
  unaffected". Under the hood, `tsnet.Server.Dial` of a public IP asks
  `PeerForIP`, which falls through to the node's route table, where the
  selected exit node owns the default routes, so the dial goes through
  netstack and WireGuard to the exit node (`ipn/ipnlocal/resolve.go`,
  `tsnet.go` `UseNetstackForIP`). This is the path tailscaled's own
  SOCKS5 and HTTP proxies use.
- The host already has a node of its own: `tw-host-<hostID>`, tagged
  `tag:tw-host`, which mgmt pushes to (`hostd.Config.PushListener`). It
  carries no tenant identity.

**Decision.**

1. **The carrier is the host's own node, not the object's.** When a host
   is configured for it, every internet flow from every workload on that
   host (Workers, Node apps, Durable Objects, and containers started with
   `enableInternet`) is dialled by the host node, which has an exit node
   selected. On the tailnet the flow is the **host's** (`tag:tw-host`);
   at the exit node and beyond it is the exit node's public address.

   This answers ADR-049's three objections. It stays opt-in per host, so
   the default is unchanged and no tailnet needs an exit node to run
   "hello world". One grant covers a fleet: `tag:tw-host` →
   `autogroup:internet`, written by the tailnet admin, never by tailworks
   (`internal/policy/autoapprovers.go` doesn't touch routes or exit nodes,
   and that stays true). And the SSRF guard stays where it is, because
   netd still resolves the name, checks every answer, and dials the
   checked IP literal; the exit node forwards packets to an address netd
   chose and never resolves anything (point 4).

   A per-object exit node (the object's own tsnet node, the object's
   identity at the exit node) is still **not** built. It would reopen the
   per-tenant grant problem and multiply exit-node prefs by the number of
   objects on a host. Nothing here precludes it later as another `via`
   value.

2. **The host flag: `-worker-internet=exit-node=<node>`.** The host's
   internet path becomes one of three:

   | value | Workers and containers reach the internet |
   |---|---|
   | `direct` (default) | from this host's own network, as today |
   | `exit-node=<node>` | through `<node>`, via this host's own tailnet node |
   | `off` | not at all (`internet_egress_unavailable`) |

   `<node>` is a MagicDNS name (`exit-fra`), a full name, a tailnet IP or a
   stable node ID; hostd resolves it in the host node's netmap at start.
   `tw up --worker-internet=exit-node=<node>` saves it in tw.json like
   `direct` and `off` do today (`cmd/tw/up_workerinternet.go`), so a
   restart and the `tw up --yes` that `tw upgrade` runs keep it. A host
   added with `tw host add` passes `tw-hostd -worker-internet=exit-node=<node>`.

   `-container-internet` accepts `exit-node` (bare) and `exit-node=<node>`
   too. A host node has one exit node, so naming two different ones is a
   startup error; bare `exit-node` means "the one `-worker-internet`
   names". A host is one internet path: there is no mixing direct and
   exit-node traffic on one host. An operator who wants both runs two
   hosts and lets `via` (point 3) place workloads.

   At start, and whenever the netmap changes, hostd checks that the node
   exists in the host node's netmap, is an exit node (`ExitNodeOption`)
   and is one this host may use; it then sets `ExitNodeID` with
   `EditPrefs` and keeps it set. If the check fails at start, hostd
   starts anyway with internet egress **refused**, never falls back to
   `direct`: `tw status` shows `internet: exit-node (unavailable: <why>)`,
   `tw doctor` fails the `worker_internet` check with the policy snippet
   the admin needs, and workloads get `exit_node_unavailable` (point 6).
   Failing open to the host's network would be the one thing an operator
   who chose this setting didn't want.

3. **The workload setting: `tailnet.egress.via: "exit-node"`.** In
   tailworks.jsonc:

   ```jsonc
   {
     "tailnet": {
       "tags": ["tag:tw-wl-billing"],
       "egress": {
         "internet": ["api.stripe.com"],
         "via": "exit-node"
       }
     }
   }
   ```

   `via` is a **requirement on the host**, not a routing choice the
   workload makes: `"exit-node"` means "run my internet egress only on a
   host whose internet path is an exit node". It is enforced at
   placement: a bundle with `via: "exit-node"` needs the host feature
   `internet-exit-node` (point 5), which a host advertises only when its
   path is `exit-node=…`. mgmt places it only on such hosts and otherwise
   fails the deploy with `feature_unavailable`, naming the feature.

   Absent (the default) means "whatever the host offers", which is
   today's behaviour. The setting is independent of `internet`: `via`
   says *how*, `internet` says *whether and where to*. `via` with
   `"internet": false` is allowed and does nothing until `internet` is
   turned back on. `via` applies to internet egress only; tailnet egress
   always leaves from the workload's or object's own node (ADR-005,
   ADR-006), with or without an exit node on the host.

   `"exit-node"` is the only value for now. A `"host"` value ("require the
   host's own network, refuse exit-node hosts") is easy to add if a
   workload turns out to need the host's source address; nothing asks for
   it yet.

   `tw dev` has no exit node (its control is local, ADR-014). It runs a
   `via: "exit-node"` workload on the direct path and prints one line
   saying so at start. Local dev is the developer's machine; the
   requirement is about production hosts.

4. **The SSRF guard and DNS move with the flow, not off the host.**
   - netd's internet path is unchanged in shape: `InternetConfig.Resolve`
     looks the name up, `netd/publicip` checks **every** answer and
     refuses the whole name if any is forbidden, and `InternetConfig.Dial`
     dials the checked IP literal. `hostd.ExitNodeInternet(srv, node)`
     (a new file, `hostd/exitnode.go`) is a `*ContainerInternet` whose
     `Dial` is the host node's `tsnet.Server.Dial` and whose `Resolve` is
     described below. It is passed where `DirectInternet()` is today, so
     Workers (`hostd.internetConfig`) and containers (`netd/sbxnet`) share
     it, as ADR-049 decision 3 requires. The guard is never relaxed: a
     private address stays `forbidden_destination` even though, through
     an exit node, "private" would now mean the exit node's LAN, which is
     exactly what must not be reachable.
   - **Names resolve through the exit node**, not the host's resolver.
     Tailscale exit nodes serve DNS to their clients over the peer API
     (`/dns-query`, DNS over HTTPS inside the tunnel), which is how
     tailscaled resolves for its own proxies when an exit node is set.
     `Resolve` sends the query to the exit node's peer API URL (from the
     host node's `Status()`) over `srv.Dial`. The host's resolver never
     sees the name, which closes the leak ADR-049 decision 4 accepted,
     for hosts on this path. Tailnet-shaped names still resolve from the
     object's netmap only, before any of this.
   - **Not every exit node answers a tagged peer.** In the pinned
     tailscale.com, an exit node's peer API answers DNS for a tagged
     client only through an extension hook that stock exit nodes don't
     register (`ipn/ipnlocal/peerapi.go` `isPeerAPIDNSAllowed`), and the
     host node is tagged. So the setting takes a resolver:
     `exit-node=<node>,dns=<ip[:port]>` sends plain DNS over TCP to that
     resolver *through the exit node* (port 53 by default). The host's
     resolver still never sees the name; the named resolver does, from
     the exit node's address. Without `dns=`, a refusal from the exit
     node fails closed with `exit_node_unavailable` whose detail says to
     add `dns=`; IP literals are never resolved and keep working. Falling
     back to the host resolver would leak the names the operator chose
     this path to hide, so there is no such fallback.
   - An exit node that is offline blackholes WireGuard traffic rather
     than refusing it. `Dial` keeps the direct path's 10 s timeout and
     reports `exit_node_unavailable` with the exit node's name, instead
     of a bare dial failure. The host node's status is the source of
     truth: hostd polls it every 2 s, and `Dial` and `Resolve` act on a
     snapshot at most 1 s old (refreshing first when it is older), so a
     change in the netmap is seen within a second and the fail-open
     window of `tsnet.Server.Dial` (which would use the host's network
     once no exit node owns the default route) is that one second at
     most. netd does not probe.

5. **Feature, versions and placement.** `internal/features` gains
   `InternetExitNode = "internet-exit-node"`. `features.Host()` is already
   build-static plus config (`FeatureSelfUpgrade`); hostd advertises
   `internet-exit-node` only when `-worker-internet` is `exit-node=…`.
   `features.Required` adds it when the bundle has `via: "exit-node"`.
   An older hostd ignores the unknown `via` key, which is why the gate is
   in placement and not in the host: mgmt never places such a bundle on a
   host that can't honour it. mgmt upgrades first, as always (ADR-041).
   `tw-hostd` also refuses to start with `exit-node=…` on a build older
   than this ADR (unknown flag value), which is the existing behaviour.

6. **Errors, status and audit.**
   - New code `exit_node_unavailable` (docs/errors.md "When": the host's
     internet path is an exit node that is missing from the netmap, not
     permitted, offline or not serving DNS; "Fix": the grant, the node,
     or `tw doctor`). It is distinct from `internet_egress_unavailable`,
     which keeps its meaning: the setting or the operator turned egress
     off.
   - `tw status` and `GET /v1/status` show the path as `exit-node`, the
     node's name and whether it is usable, where they show `direct`/`off`
     today (`StatusView.WorkerInternet`). `tw doctor`'s `worker_internet`
     check verifies the netmap, the permission and DNS.
   - The `egress` audit record's note becomes "internet via exit node
     <name> (<addr>)" instead of "internet via this host's network
     (<addr>)". The first flow per object and destination is recorded as
     today; refusals are `deny` records with their code. The flow spans
     (ADR-045) carry the path as an attribute.
   - The destination sees the exit node's address. The exit node's
     operator sees flows from `tw-host-<id>`, never which tenant or
     object sent them; that attribution stays in the host's audit chain.

7. **Rejected alternatives.**
   - *Per-object exit node* (point 1): per-tenant grants, N prefs per
     host, and no way to make it a host-level requirement.
   - *A subnet router or site-to-site instead of an exit node*: it
     routes specific prefixes, not "the internet", and tailworks would
     have to know which; an exit node is the Tailscale primitive for
     "default route via a peer".
   - *Running the host's traffic through the machine's tailscaled exit
     node* (i.e. tell operators to `tailscale set --exit-node` on the
     box): `DirectInternet()` dials from the host's network stack, so it
     would work on Linux hosts where the exit node is the OS default
     route, and silently not on macOS, in a Lima VM, or wherever tsnet is
     the only Tailscale present. It also can't be checked, reported or
     placed on. The explicit flag can.
   - *`egress.internet.via`* (nesting under `internet`): `internet` is a
     boolean or a list and widening it to an object would break the list
     form every existing bundle uses. A sibling key keeps the schema
     additive.

**Consequences.**

- Operators get a fixed, inspectable egress point per host, and hosts on
  networks with no direct internet can run internet-calling workloads.
  The gateway recipe remains the answer for per-API logging, caching or
  credentials; it composes with this (the gateway is a tailnet peer, so a
  Worker reaches it from its own node regardless of the host's path).
- The host node gains a job. Until now it only served mgmt's pushes. With
  an exit node set it carries every internet byte the host's workloads
  send, so its throughput and the exit node's matter for Worker latency
  to internet APIs. The tests below measure it.
- A new trust party for hosts on this path: the exit node's operator sees
  every internet flow's destination and bytes (TLS is still end to end
  from netd, so not the content). docs/security-model.md "Who you trust"
  lists them, and "Egress" gets a paragraph for the path.
- `tag:tw-host` needs `autogroup:internet` on tailnets that use this.
  docs/INSTALL.md and `tw doctor` say so. tailworks never writes it.
- Containers' `enableInternet` table (ADR-049 decision 6) is unchanged;
  only the path differs.

**Tests.** `hostd:TestExitNodeInternet` with the local control server:
two tsnet nodes, one advertising `0.0.0.0/0, ::/0` and serving a peer API
DoH stub, the host node selecting it; a Worker `fetch()` to a name whose
DoH answer is a public documentation address (`203.0.113.0/24`) arrives at
the exit node's side and not at the host's dialer (the hermetic `Dial`
stand-in fails the test on any host-network attempt, as in ADR-049); a
DoH answer in a forbidden range is `forbidden_destination`; a name with
the exit node's DNS absent is `exit_node_unavailable`; the host resolver
stand-in fails the test if it is ever called. `netd/sbxnet` gets the same
through a container start with `enableInternet`. `internal/features`:
`Required` with `via`, `Host()` with and without the flag; `mgmt`
placement: a `via` bundle is placed only on an advertising host and
otherwise `feature_unavailable`. `cmd/tw`: `--worker-internet=exit-node=x`
round-trips through tw.json and `--separate-host` args; `tw doctor` with
the node missing. `e2e-real` (needs a real tailnet with an exit node and
the `tag:tw-host` grant; skipped in CI): end-to-end `fetch()` to a public
echo service shows the exit node's address, plus p50/p95 against the
direct path, recorded in `status/<branch>.md`.

**Open questions for review.**

1. Should `via` also be settable per Durable Object class
   (`tailnet.objects.<Class>.egress` already picks `object`/`workload`)?
   Proposed: no; it is a property of where internet traffic leaves the
   host, which is per host, so per workload is already finer than the
   mechanism.
2. Settled while building: exit nodes refuse peer-API DNS for tagged
   clients in the pinned tailscale.com, so `dns=<resolver>` (point 4) is
   the answer for them and for Headscale; there is no host-resolver
   fallback.
3. Whether `tw up` should offer to pick an exit node when the netmap
   shows exactly one. Proposed: no; a one-line hint in `tw doctor` is
   enough.
