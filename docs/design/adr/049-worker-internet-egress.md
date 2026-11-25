# ADR-049: Workers have internet egress by default, from the host's network through netd's SSRF guard; `tailnet.egress.internet` turns it off or limits it

**Date:** 2026-11-21
**Status:** Accepted (2026-11-21, decided by the maintainer on issue #161;
built on branch `feat-161-worker-internet-egress`). Drafted as
`ADR-worker-internet-egress`. Revises RFC §4.8 ("denies the public internet
by default") and ADR-019 rule 1 ("internet egress only via an exit node or
Aperture"). Extends ADR-032 D4 (containers' `enableInternet`).

**Context.**

- Workers have had no internet egress. netd's egress guard (ADR-019)
  resolves names from the object's netmap only and dials tailnet addresses
  only. Since #117, a Worker's `fetch()` to an internet name is refused
  with `internet_egress_unavailable`, whose detail links the gateway recipe
  (docs/recipes.md#call-an-internet-api).
- The RFC described internet egress as opt-in per class
  (`egress.internet: ["api.openai.com"]`, "routed through an exit node or
  a gateway such as Aperture"). That was never built for Workers.
  `tailnet.egress.internet` exists only as the containers' allowlist
  (ADR-032 D4): a container started with `enableInternet: true` reaches the
  listed hosts from the host's own network, through `netd/sbxnet`, which
  refuses private, link-local, loopback, tailnet and host addresses after
  DNS.
- On Cloudflare, a Worker's `fetch()` reaches the internet with no
  configuration. Code ported from Cloudflare (an OAuth callback, a webhook
  sender, an LLM API call) fails on tailworks on its first line of I/O.
  The gateway recipe works, but it is a machine to run and a grant to
  write before "hello world" calls anything.
- The maintainer decided the direction on #161: **Workers have internet
  egress on by default, with a setting to turn it off.** This ADR records
  that decision and settles the questions #161 left open: where the traffic
  leaves, how names resolve, attribution and audit, the schema, and
  upgrade behaviour.

**Decision.**

1. **On by default.** A Worker's (and a Node app's, and a Durable
   Object's) outbound `fetch()` to a destination that is not on the tailnet
   is allowed unless the workload turns it off. Tailnet egress is
   unchanged: it leaves from the workload's node, or the object's own node
   in facet mode, and the tailnet policy decides what it reaches.

2. **Where it leaves: the host's own network, through netd.** netd's
   egress handler keeps one entry point for every `fetch()`. A destination
   is routed to the internet path only when it is not a tailnet
   destination:
   - an IP literal outside the tailnet ranges (`100.64.0.0/10`,
     `fd7a:115c:a1e0::/48`), or
   - a name that is not in the object's netmap, is not a Tailscale Service
     name, and is not tailnet-shaped (a bare short name, a name under a
     tailnet's MagicDNS suffix, or any `*.ts.net` name; #117). A
     tailnet-shaped name that misses stays `peer_not_visible`.

   The internet path dials from the host's network stack
   (`hostd.Config.WorkerInternet`, `hostd.DirectInternet()`, the same path
   containers use). **The flow never leaves as the object's tailnet
   identity**: it does not touch the object's tsnet node, and netd strips
   the platform's identity headers (`x-tw-*`, `tw-*`, `cf-*`,
   `Tailscale-*`, `Proxy-Authorization`, …) as it does on every egress.
   **The source address is the host's**: whatever address that host's
   routing and NAT give it. It is per host: two hosts give two source
   addresses, and a workload that placement moves gets a new one. An API
   that allowlists source IPs needs every host's address, or a gateway on
   the tailnet with a fixed address (the recipe still applies).

   Rejected alternatives:
   - *An exit node per workload.* The object's tsnet node would send the
     flow to an exit node, so it would leave under the object's tailnet
     identity, and every tailnet would need an exit node and an
     `autogroup:internet` grant before a Worker could call anything. That
     can't be on by default. netd would also lose sight of the resolved
     address, so the SSRF guard would move to the exit node, which
     tailworks doesn't run.
   - *A platform gateway.* The same setup cost, plus a component tailworks
     would have to operate. Tenants who want one (a fixed source IP,
     logging, caching, rate limits) can already run one on their tailnet;
     the recipe says how.

3. **The SSRF guard is mandatory and checks the address that is dialled.**
   `netd/publicip` is shared by both internet paths (Workers in netd,
   containers in `netd/sbxnet`), so they can't disagree. It refuses
   loopback and unspecified addresses; RFC 1918 and ULA (`fc00::/7`);
   link-local, including `169.254.169.254` and other metadata services;
   multicast and broadcast; CGNAT `100.64.0.0/10` (the tailnet range,
   including `100.100.100.100`) and the tailnet's IPv6 range; reserved,
   benchmarking and discard ranges; and the forms that embed or translate
   an IPv4 address (IPv4-mapped, IPv4-compatible, NAT64, 6to4, Teredo).
   netd also refuses the host's own addresses and the platform's
   endpoints. A refusal is `forbidden_destination`.
   - **No DNS rebinding.** netd resolves the name when it dials, checks
     **every** answer, refuses the whole name if any answer is forbidden,
     and dials the checked address, never the name. Each new connection
     resolves and checks again, so an answer that changes between
     connections is checked again too. A redirect is a new request that
     workerd sends through the same handler.
   - **TLS** for `https://` is originated by netd with the system roots
     and the URL's host as the server name, so a DNS answer can't swap
     the certificate that is checked.
   - **Special names are never looked up.** A name with a special-use or
     private suffix (`localhost`, `local`, `internal`, `arpa`, `test`,
     `invalid`, `onion`, `alt`, `lan`, `home`, `corp`, `intranet`,
     `localdomain`) or a numeric last label (`127.1`) is refused before
     DNS. `metadata.google.internal` never reaches the host's resolver.

4. **Names: what leaks and what doesn't.**
   - Tailnet-shaped names are resolved from the object's netmap only, as
     before. They never reach host DNS.
   - A name that the workload's setting refuses (off, or not on its
     allowlist) is refused before DNS, so it is never looked up.
   - Any other internet name is resolved by the **host's resolver**
     (`net.DefaultResolver`). That resolver, and whatever upstream it
     forwards to, sees the names that Workers on that host look up, when
     they look them up, from the host's address. It doesn't learn which
     tenant or object asked. Tenants on one host share the resolver and its
     cache, so one tenant could time a lookup to learn whether another
     looked up the same name recently. We accept this for now: tenants who
     need private lookups turn internet egress off and use a gateway. A
     per-tenant resolver (for example DNS over HTTPS to a resolver the
     tenant chooses) can be added behind `InternetConfig.Resolve` later.

5. **Attribution and audit.**
   - The first internet flow per object and destination (`host:port`) on
     a host is an audit record of kind `egress` on the host chain: the
     object ID, `dst` (the name and port the code asked for), and a note
     naming the address dialled ("internet via this host's network
     (203.0.113.7:443)"). Each tenant's registry on a host remembers
     4096 destinations; past that every flow is recorded (more records,
     never fewer). Containers' internet flows are already audited the same way
     (`sandbox-of <object>`).
   - Every refusal is already a `deny` record with its code, from hostd's
     egress handler and the Node tier's CONNECT path.
   - Every request is a span in the egress telemetry (ADR-045), like any
     other egress. hostd logs the first flow per destination, and netd
     logs every internet flow at debug level.
   - The destination doesn't see a tailnet identity. It sees the host's
     source address and whatever the code sends.

6. **One schema for Workers and containers: `tailnet.egress.internet`.**

   | value | Workers and Node apps | containers (`enableInternet: true`) |
   |---|---|---|
   | absent | any public host | none (unchanged) |
   | `true` | any public host | any public host |
   | `false` or `[]` | none (`internet_egress_unavailable`) | none |
   | `["api.example.com", "*.github.com", "203.0.113.0/24"]` | only those | only those (unchanged) |

   The list form is the containers' existing allowlist syntax (exact host
   names, `*.suffix`, `*`, IPs and CIDRs). An IP literal matches only IP and
   CIDR entries. The guard in point 3 applies in every case: a list can't
   allow a private address.

   Containers keep "absent means none". A container is a whole OS image
   that the workload author may not have written (a Sandbox SDK
   interpreter running model-written code), and `enableInternet` is
   already its per-start switch. Widening it on upgrade would be a silent
   change to a stronger sandbox. `true` is the explicit way to give it the
   whole internet.

7. **The opt-out.** In tailworks.jsonc:

   ```jsonc
   { "tailnet": { "egress": { "internet": false } } }
   ```

   `internet_egress_unavailable` now means exactly this: internet egress is
   turned off for the workload (`false` or `[]`), the host is not on its
   allowlist, or the host's operator turned it off for the whole host
   (`tw-hostd -worker-internet=off`; `tw up` and `tw dev` use `direct`).
   Its detail names the setting and links the gateway recipe. A host with
   no internet path keeps today's codes exactly: an internet name is
   `internet_egress_unavailable`, a public IP literal is
   `forbidden_destination`.

   The setting is the active deployment's. It takes effect when a deploy
   that changes it becomes active, and for every request after that,
   including requests from objects that were created earlier.

8. **Upgrade behaviour.**
   - **An app without `tailnet.egress.internet` gains internet egress as
     soon as its host runs this release**, without a redeploy (hostd
     reads the setting from the deployed bundle). This is the deliberate
     reversal; the release note says so under "Upgrade notes", and tells
     operators how to keep the old behaviour: add `"internet": false` (or
     `[]`) and redeploy, or run hosts with `-worker-internet=off`.
   - An app with a list keeps its containers' behaviour; its Workers can
     now reach the listed hosts too (before, they reached none).
   - Nothing that was allowed is refused, and the tailnet path is
     unchanged.
   - Mixed versions: an older hostd can't parse `true` or `false` there.
     A bundle that uses the boolean form needs the host feature
     `worker-internet` (internal/features), so mgmt places it only on
     upgraded hosts and otherwise fails the deploy with
     `feature_unavailable`. `[]` means "off" on every version, so a mixed
     fleet can use it until all hosts are upgraded.

**Consequences.**

- Code ported from Cloudflare that calls internet APIs works on tailworks
  without a gateway.
- The default trust boundary moves: a Worker can now send data to the
  internet. Tailnet policy still decides what it can read on the tailnet,
  but anything it reads it can now send out. A workload that holds
  sensitive tailnet data and doesn't need the internet should set
  `"internet": false`. docs/security-model.md and security/THREAT-MODEL.md
  say so.
- On the process tier, the guard is netd's, on the path every `fetch()`
  takes. Code that escapes workerd's V8 sandbox, or a Node app that skips
  the proxy, is held to it only where the OS sandbox confines TCP
  (docs/security-model.md); on the microVM tier the guest's only network
  is the platform's. That was already true for tailnet egress; what's new
  is that the host network is now a reachable destination class for
  well-behaved code, so the guard on it matters.
- Source addresses are per host. "Which IP will my Worker call from?" has
  no stable answer across hosts; the gateway recipe is the answer when one
  is needed.

**Tests.** `netd:TestInternetEgress` (through real workerd and netd: an
allowed internet fetch, a public IP literal, the opt-out, the allowlist,
every blocked range both as a DNS answer and as a literal, special names
never looked up, DNS rebinding with mixed answers and with an answer that
flips between lookups, the tailnet path unchanged, and the Node tier's
CONNECT path). `hostd:TestWorkerInternetEgress` (the hello template's
`fetch()` end to end: on by default and audited, `false`, an allowlist,
the guard). `netd/publicip` unit tests, `hostd/bundle:TestInternetEgress`,
`internal/features:TestRequiredWorkerInternet`. The tests are hermetic:
`InternetConfig.Resolve` answers from a table with public documentation
addresses (`203.0.113.0/24`), and `Dial` sends those to a local server and
fails the test if any other address reaches it. The guard itself is never
relaxed for tests.
