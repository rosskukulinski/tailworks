# Security model

How twd decides who is calling, what each workload can reach, and what
stands between tenants. Read this first; the red team's detailed view is
in [THREAT-MODEL.md](../security/THREAT-MODEL.md) (a dated assessment),
[RED-TEAM.md](../security/RED-TEAM.md) (findings and fixes) and
[LEDGER.md](../security/LEDGER.md) (what is still open). To report a
vulnerability, see [SECURITY.md](../SECURITY.md).

## Identity: WireGuard, not tokens

- **Every workload and every Durable Object instance is its own tailnet
  node**, with its own MagicDNS name and the tags from `twd.jsonc`. Your
  tailnet policy (grants) decides who may reach which node, as for any
  other device.
- **Callers are identified by the network.** netd asks the tailnet who
  owns the WireGuard peer (WhoIs) and puts the answer in `request.cf`.
  `getCaller(req)` returns that node, its user (for a person's device),
  its tags and the app capabilities its grants carry (`caps()`). Client
  copies of identity headers (`Tailscale-*`, `x-twd-*`, …) are stripped on
  the way in ([COMPAT.md](COMPAT.md#identity)). There are no bearer tokens
  between workloads.
- **twd's own API works the same way.** twd-mgmt serves `/v1` on its
  tailnet node and authorizes each request by WhoIs: admins hold
  `twd.dev/cap/admin`, approvers `twd.dev/cap/approve`, MCP tool users
  `twd.dev/cap/mcp` ([agents.md](agents.md), [approvals.md](approvals.md)).
  `twd link` stores only mgmt's address; no key or token.
- **One credential mints node keys:** an OAuth client, a workload identity
  federation trust credential, or an API key ([INSTALL.md](INSTALL.md)).
  Each object's key is single use, pre-authorized, tagged and expires
  within 5 minutes (`KeyExpiry` in `mgmt/types.go`). Warm keys, minted
  ahead of time so new objects start faster, wait in mgmt's custody for up
  to 30 minutes ([INSTALL.md](INSTALL.md#tuning-how-fast-a-new-objects-own-node-comes-up)). Revoking the credential
  stops all future minting.
- **Kill is a network cut.** `twd kill` tombstones the object, its node
  stops answering (410), and with the Devices scope the device is deleted.

## Isolation tiers

| Tier | Where | What separates a workload from the host and other tenants |
|---|---|---|
| **process** (default) | macOS, Linux | V8 isolates (workerd) or a Node.js process, inside a per-workload OS sandbox: Seatbelt on macOS, Landlock + seccomp on Linux (`twd-hostd -sandbox auto`). The workload can write only its own directories, can't read your home directory or twd's state, can't use twd's or the system's sockets, and on Linux (Landlock ABI 4+) may open TCP only to its own port and egress proxy. |
| **microVM** (`"tier": "microvm"`) | Linux with KVM (Firecracker), macOS on Apple silicon (Virtualization.framework) | A VM per workload. On Linux, Firecracker runs under its jailer (own chroot, uid, network namespace, cgroup) and Durable Object storage sits on a per-workload encrypted volume whose key mgmt releases only to the host that holds the workload's lease. Node keys stay on the host and never enter the guest ([containers.md](containers.md), [INSTALL.md](INSTALL.md#linux-the-microvm-tier-firecracker-kvm)). |

The process tier is not a VM boundary. Don't run code you don't trust on
it. If the OS sandbox isn't available (Linux without Landlock), or your
user can reach a container engine or the system tailscaled socket,
`twd doctor` warns and twd serves **one tenant only** unless you start it
with `twd up --insecure-shared-host`.

## Egress: deny by default

- A Worker's outbound `fetch()` reaches **tailnet peers only**. A name that
  isn't in the node's netmap is refused (`peer_not_visible`), as are
  loopback, link-local, metadata and the host's own addresses. There is no
  internet egress for Workers; put what they need on the tailnet
  ([recipes.md](recipes.md)). Containers may reach an `egress.internet`
  allowlist ([containers.md](containers.md)).
- Traffic leaves from the workload's node, or from the object's own node
  in facet mode ([egress-modes.md](egress-modes.md)), so your tailnet
  policy decides what each object can reach, and the other side's
  `getCaller` sees which object called.

## Trust boundaries

| Boundary | Who is trusted | What holds it |
|---|---|---|
| Tenant code ↔ twd | twd, never the tenant's code | the tier's sandbox or VM; the platform wrapper sets `request.cf`; egress goes through netd's guard |
| Tenant ↔ tenant on one host | neither | per-tenant netd registries (one tailnet's netmap each), per-workload sandboxes and 0700 state directories; one tenant only without a sandbox |
| Object ↔ object in a workload | the workload's code | per-object nodes and egress attribution; a sub-agent's capabilities are narrowed to what its spawner allowed (`lim`) |
| Host ↔ mgmt | mgmt | WhoIs `tag:twd-host`; every host write is fenced by its lease epoch |
| twd ↔ Tailscale control | the tailnet admin | one revocable mint credential; tags you own in `tagOwners` |
| Internet ↔ workload | nobody | tailnet-only by default; public routes are opt-in paths over Funnel on `tag:twd-public`, and `getCaller` throws on a public request ([public-ingress.md](public-ingress.md)) |

## Secrets and state

- Worker secrets are sealed in mgmt's custody (AES-256-GCM per entry) and
  reach only the workload's own process; mgmt's database never holds a
  value ([secrets.md](secrets.md)).
- The state directory (`~/.twd`) holds node keys and the custody key.
  Back it up like a secret ([INSTALL.md](INSTALL.md#back-up-and-restore)).
- The audit trail is hash-chained, so truncation and gaps show
  (`twd audit`).

## Known gaps

The red team's open items are tracked in
[LEDGER.md](../security/LEDGER.md), with a test for each. Among them:
on-behalf-of assertions (`actingFor`) aren't implemented, and tenant code
can still forge `request.cf` inside its own process when it forwards a
request in-process.
