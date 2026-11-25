# Alternatives to tailworks

Where tailworks fits next to other ways to run Workers-style code and to
give agents a network identity, and when to pick something else.

**Facts about other projects are as of 2026-11-24**, each taken from the
source linked in its row. They go stale quickly; the linked source wins
over this page. "Not stated" means the linked source doesn't say, not that
the answer is no. If a row is wrong or out of date, please
[open an issue](https://github.com/rosskukulinski/tailworks/issues).

tailworks is a community project, not affiliated with or endorsed by
Tailscale Inc. or Cloudflare. The descriptions of their products below come
from their public pages only.

## What tailworks adds to stock workerd

tailworks runs your code on unmodified
[workerd](https://github.com/cloudflare/workerd), so the Workers and
Durable Objects APIs are Cloudflare's own implementation
([COMPAT.md](COMPAT.md) lists what works). What it adds is identity on a
network you control. Every workload and every Durable Object instance is
its own node on your tailnet, with its own name and tags
([concepts](concepts.md#object)). From that follow the rest:

- **Callers are identified by the network.** `getCaller()` returns the
  WireGuard peer's user or tags; there are no bearer tokens between
  workloads ([security model](security-model.md#identity-wireguard-not-tokens)).
- **Grants authorize at the level of a tool.** Your tailnet policy decides
  who may reach which object, what each object may reach, and which MCP
  tools a caller may run ([COMPAT.md](COMPAT.md#mcp)).
- **Sub-agents are narrowed.** An object created with `ctx.tailnet.spawn()`
  gets at most its parent's grants ([concepts](concepts.md#sub-agent)).
- **Kill is per node.** `tw kill` takes an object and its sub-agents off
  the tailnet ([concepts](concepts.md#kill)).
- **Approvals are decided by a tailnet identity**, not by whichever code
  holds a binding ([approvals.md](approvals.md)).
- **The audit trail is hash-chained**, so truncation and gaps show
  ([security model](security-model.md#secrets-and-state)).

That is the whole claim. tailworks is not a faster or more scalable way to
run Workers, and several of the projects below are ahead of it on storage
durability and scale.

## Comparison

"Per-workload network identity" means: does each Worker, or each Durable
Object or actor, appear to the services it calls as its own principal on
the network, so that a network policy can tell one from another?

| | Self-hosted | Workers API | Per-workload network identity | Durability model | Licence | Source |
|---|---|---|---|---|---|---|
| **tailworks** | Yes: macOS or Linux hosts on a Tailscale or Headscale tailnet | Stock workerd; surface by surface in [COMPAT.md](COMPAT.md) | Yes: a tailnet node per workload and per Durable Object | Durable Object storage is on one host's disk with no replication. D1 and KV writes and the R2 index are journaled to the control plane and survive a host loss; failover is manual ([COMPAT.md](COMPAT.md#do-storage)) | BSD-3-Clause | this repo |
| **celld** | Yes: nodes that share one object storage bucket form a fleet | Its own runtime: "Every node embeds V8 and executes Wrangler bundles". Deploys from a supported subset of the wrangler config. Lists Workers, Durable Objects, KV, Queues, D1, R2, Workflows, Cron Triggers, static assets, Dynamic Workers, and Containers (experimental) | Not stated. Nodes authenticate to each other with a fleet HMAC, and the README says to put node addresses on "a trusted private network or an encrypted overlay such as WireGuard or Tailscale" | A SQLite database per object. With two or more nodes a write is durable once followers hold it on disk, and is uploaded to the bucket afterwards; a single node uploads to the bucket before acknowledging. Ownership moves to another node by a conditional bucket write | Apache-2.0 | [README](https://github.com/denoland/celld), [guarantees](https://github.com/denoland/celld/blob/main/docs/guarantees.md) |
| **workerd on its own** | Yes: one binary, configured in Cap'n Proto; the README suggests systemd | The reference implementation. You write the config and supply the backends for bindings yourself | No | Durable Objects on the instance's local disk (`localDisk`, marked experimental in the config schema). The main branch also has a cluster mode behind `--experimental`: instances share a bucket, and each object has one owner at a time (see [Upstream](#upstream-workerd-and-multi-node-durable-objects)). How writes are acknowledged in cluster mode: not verified | Apache-2.0 | [README](https://github.com/cloudflare/workerd), [`workerd.capnp`](https://github.com/cloudflare/workerd/blob/main/src/workerd/server/workerd.capnp) |
| **OpenOrange** | Yes: you run the Rivet control plane and worker-node containers (the README also describes a mode that Rivet operates in your cloud account) | A reimplementation: it "implements the Workers API on Node.js" and "does not run workerd". Reads your wrangler config. KV, D1, Queues, Cron Triggers, Hyperdrive and Rate Limiting are listed as drop-in; Durable Objects, Workflows and Cache as partial | Not stated | Each Durable Object is a Rivet Actor with its own SQLite database; "S3 stores each object's SQLite data" | Apache-2.0 | [README](https://github.com/rivet-dev/open-orange) |
| **Rivet** | Yes ("S3 is the only dependency"); also a managed cloud, and a control plane in your own VPC | No: its own actor API (RivetKit) on Node.js and Bun. The README calls it "an open-source alternative to Cloudflare Durable Objects" | Not stated. Clients get JWTs scoped to the actors they may reach | SQLite per actor, backed by S3 or another object store; "If a node goes down, its Actors move elsewhere with their state intact" | Apache-2.0 | [README](https://github.com/rivet-dev/rivet) |
| **Cloudflare Mesh + Workers VPC** | No: a Cloudflare service. You run connectors on your machines, and "All Mesh traffic routes through Cloudflare's global network" | Yes: Workers on Cloudflare reach your private network through a Workers VPC binding (Workers VPC is in beta) | Not today. "the target service sees a Worker making a request. It doesn't know which agent is calling". Identity-aware routing, where "eventually each agent gets a distinct identity", is described as being built | Not applicable (a network product; storage is Cloudflare's) | Service; open source not stated | [Mesh announcement, 2026-04-14](https://blog.cloudflare.com/mesh/), [Workers VPC docs](https://developers.cloudflare.com/workers-vpc/) |
| **Cloudflare Agents SDK on Cloudflare** | Not stated. The SDK is built for Cloudflare's network ("No infrastructure to manage"). Its core also runs on tailworks ([COMPAT.md](COMPAT.md#agents-sdk)) | Yes: "Each agent is a Durable Object" | As Workers on Cloudflare (the row above) | Cloudflare's Durable Object storage. Cloudflare's 2024 description: each change is forwarded to five followers and confirmed once three have it, and responses are held until then | SDK: MIT. Platform: service | [Agents docs](https://developers.cloudflare.com/agents/), [SDK repo](https://github.com/cloudflare/agents), [storage post, 2024-09-26](https://blog.cloudflare.com/sqlite-in-durable-objects/) |
| **Coolify, Dokploy** (run it in a container) | Yes | No. They deploy containers, databases and Compose stacks, not Workers. You could run workerd in a container yourself | Not stated | Whatever the container and its volumes give you; not verified | Coolify: Apache-2.0. Dokploy: Apache-2.0, with a separately licensed `/proprietary` directory | [Coolify](https://github.com/coollabsio/coolify), [Dokploy](https://github.com/Dokploy/dokploy), [Dokploy licence](https://github.com/Dokploy/dokploy/blob/canary/LICENSE.MD) |
| **Tailscale tsnet, tsidp, Aperture** | tsnet is a Go library you embed; tsidp is a server you run on your tailnet; Aperture: you sign up for it, and whether you can run it yourself is not stated | No. These are identity and network building blocks, not a Workers runtime | Yes, for a program you write: tsnet "lets you embed Tailscale inside a Go program", which then joins the tailnet as its own device | Not applicable | tsnet (in `tailscale/tailscale`) and tsidp: BSD-3-Clause. Aperture: not stated | [tsnet](https://tailscale.com/kb/1244/tsnet), [tsidp](https://github.com/tailscale/tsidp), [Aperture](https://tailscale.com/docs/aperture), [agent connectivity](https://tailscale.com/use-cases/secure-ai-agent-connectivity) |

### What each is better at

- **celld** has multi-node Durable Objects with replicated storage today,
  which tailworks does not. If you need an object to survive the loss of a
  host without manual steps, look there first. Its authors have joined
  Cloudflare and the two teams say they are "merging workerd and celld"
  ([announcement, 2026-10-09](https://blog.cloudflare.com/deno-joins-cloudflare/)),
  so check that post for where the project is heading.
- **workerd on its own** is the smallest thing that runs a Worker. If one
  machine, no per-object identity and your own process supervision are
  enough, you don't need anything on top. Its README warns that "workerd
  is not a hardened sandbox" and must run inside one; that applies to
  tailworks' process tier too ([Isolation tiers](security-model.md#isolation-tiers)).
- **OpenOrange** targets AWS, Google Cloud, Azure or your own servers with
  storage in S3, and lists bindings tailworks doesn't have (Hyperdrive,
  pluggable Workers AI). It runs on Node.js rather than
  workerd, so check its compatibility pages against the APIs you use.
- **Rivet** is the choice when you want actors and don't need the Workers
  API: a general actor platform with S3-backed storage and a managed
  option.
- **Cloudflare Mesh with Workers VPC** keeps your code on Cloudflare's
  network, with Cloudflare's scale, storage durability and products, and
  still reaches private services. If you are happy on Cloudflare and need
  private connectivity more than per-agent identity, it is less to run.
- **The Agents SDK on Cloudflare** is where the SDK is developed and where
  all of it works, including the parts tailworks lacks (`subAgent()`,
  `env.AI`, email, browser).
- **Coolify and Dokploy** run any container, in any language, with
  databases and one-click services. If your app isn't written for Workers,
  they fit better than any row above.
- **tsnet, tsidp and Aperture** are the pieces to use when you are writing
  your own Go service or agent and want it on your tailnet directly:
  tsnet for the node, tsidp (which its README calls "an experimental, work
  in progress, community project") as an OIDC/OAuth provider backed by
  tailnet identity, Aperture as "a centralized AI gateway". tailworks
  itself is built on tsnet ([concepts](concepts.md#node)); it is the
  Workers and Durable Objects runtime on top, so you don't embed tsnet in
  each agent yourself.

## Upstream: workerd and multi-node Durable Objects

tailworks runs stock workerd, the published release pinned in
[COMPAT.md](COMPAT.md#sdk-conformance-process-tier), and intends to keep doing so. It
does not aim to build a competing distributed Durable Objects layer.

Cloudflare has said it will make "workerd self-hosting a first-class
supported way to build and run apps using the Workers programming model",
merging code and ideas from celld into workerd
([2026-10-09](https://blog.cloudflare.com/deno-joins-cloudflare/)). That
post describes today's workerd Durable Objects as "only in a
single-instance way". As of 2026-11-24, workerd's main branch already has
a cluster mode behind `--experimental`, in which instances "work together
in a cluster, to distribute Durable Objects among them" using a shared
bucket
([`ClusterConfig` in `workerd.capnp`](https://github.com/cloudflare/workerd/blob/main/src/workerd/server/workerd.capnp)).
tailworks does not use it yet.

The intent: when multi-node Durable Objects are a supported part of
workerd, tailworks adopts them for storage and placement, and keeps doing
the part upstream doesn't: a tailnet identity per workload and per object,
grants, kill, approvals and audit. This is an intent, not a schedule.

Until then, the storage story is single-host
([COMPAT.md](COMPAT.md#do-storage)):

- A Durable Object's storage is a directory on the host that runs it.
  Nothing replicates it, and it doesn't move with the workload.
- D1 and the other platform stores journal each write to the control
  plane, so they survive a host loss. Whether that is the right design is
  open in
  [#38](https://github.com/rosskukulinski/tailworks/issues/38).
- [#297](https://github.com/rosskukulinski/tailworks/issues/297) is a
  spike on replicating Durable Object SQLite storage with the workerd
  tailworks runs today.

## When not to use tailworks

- **You need edge scale.** tailworks runs on machines you own, in the
  places you put them. There is no global network, no automatic placement
  near users, and `request.cf` has no geolocation
  ([COMPAT.md](COMPAT.md#fetch)). Use Cloudflare.
- **You need Durable Objects to survive a host loss today.** Object
  storage is on one host with no replication, and failover is manual
  ([COMPAT.md](COMPAT.md#do-storage), [locks](COMPAT.md#locks)). celld,
  Rivet and Cloudflare all replicate object storage or keep it in object
  storage.
- **You write Python Workers that need Cloudflare-only pieces.** Python
  Workers run ([python.md](python.md)), with Durable Objects, KV, R2 and D1
  cron triggers, Queues, static assets and packages vendored by
  pywrangler, on both isolation tiers. Python Workflows don't yet, and
  Python Durable Objects have no tailnet node of their own.
- **You are building a public SaaS.** tailworks identifies callers by
  tailnet identity, so its model fits internal tools and agents. Public
  routes exist, as opt-in paths over Tailscale Funnel
  ([public-ingress.md](public-ingress.md)), but a public request has no
  caller identity, and the default process tier is not a boundary for code
  you don't trust ([security model](security-model.md#isolation-tiers)).
- **You depend on Workers AI**, or on Vectorize, Browser Rendering, Email
  or Hyperdrive. tailworks has none of these bindings
  ([COMPAT.md](COMPAT.md#workers-ai)). For models, it expects an LLM
  gateway on your tailnet.
- **You don't want a tailnet.** Everything above depends on Tailscale or
  [Headscale](headscale.md). Without one, tailworks adds nothing over
  workerd on its own.
