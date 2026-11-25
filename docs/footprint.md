# What tailworks runs on a host

Everything `tw up` puts on a machine and on your tailnet, on one page: the
processes, the ports, the devices, the files and, for the microVM tier,
the one root-owned part. The details are in the pages linked from each
section.

## Processes

- **One supervisor:** `tw __supervise --state-dir ~/.tailworks`. `tw up`
  starts it detached (or `tw up --service` installs it as a macOS
  LaunchAgent, `~/Library/LaunchAgents/dev.tailworks.up.plist`, or a
  systemd user unit, `~/.config/systemd/user/tw.service`). It runs as
  you, and its pid is in `~/.tailworks/supervisor.pid`.
- **mgmt and the host agent run inside it** by default (`"mode":
  "single"` in `tw.json`). With `tw up --separate-host` they are two
  children instead, `tw-mgmt` and `tw-hostd`, which the supervisor
  restarts with backoff. netd (the tailnet nodes, WhoIs, the egress
  guard) is part of the host agent, not a process of its own
  ([hosts.md](hosts.md#one-process-by-default-scale-out-with-tw-host-add)).
- **One runtime process per deployed workload:** workerd, or Node.js for
  a `"runtime": "node"` app, each in a process sandbox (Seatbelt on
  macOS, Landlock and seccomp on Linux; [Isolation tiers](security-model.md#isolation-tiers)).
- **On the microVM tier**, one microVM per workload or container instead:
  Firecracker under its jailer on Linux, `tw-vz` (Virtualization.framework)
  on macOS ([containers.md](containers.md#how-it-works)).

`tw down` stops all of it: the host agent first, then mgmt.

## Ports

Every listener below is on a tailnet node (tsnet, inside the tailworks
process), not on the machine's own interfaces, except the loopback proxy.

| Where | Port | What |
|---|---|---|
| mgmt's node `tw-mgmt` (`tag:tw-mgmt`) | `tcp:80`, and `tcp:443` when the tailnet has HTTPS certificates | the `/v1` API and the admin console (`/ui`), authorized by WhoIs |
| `tw-mgmt` | `tcp:9464` | Prometheus `/metrics` (mgmt's, and the embedded host's in the default layout); `tw-mgmt -metrics-port 0` turns it off ([observability.md](observability.md#metrics-get-metrics)) |
| a host node `tw-host-<id>` (`tag:tw-host`), only with `--separate-host` or a host added with `tw host add` | `tcp:7474` | mgmt pushes kills to the host |
| a host node | `tcp:9464` | the host's `/metrics` |
| each workload and object node | `tcp:80` | your app, behind netd's WhoIs and header stripping |
| a workload node with [public routes](public-ingress.md) | `tcp:443` over Funnel | the paths you opted in, and nothing else |
| `127.0.0.1` on the host (a random port) | | mgmt's loopback proxy: the `tw` CLI on this machine reaches mgmt through it as the operator. Its address and password are in `~/.tailworks/mgmt/loopback.json` (0600) |

The policy grants these need are the ones `tw up` prints: host ⇄ mgmt on
`tcp:80,443` and `tcp:7474`, admins to `tag:tw-mgmt`, and an optional
`tcp:9464` grant for your Prometheus ([observability.md](observability.md)).

## Devices on your tailnet

- **`tw-mgmt`**, one per install. In the default layout it is the only
  infrastructure device.
- **`tw-host-<id>`**, one per host that runs as its own process
  (`--separate-host`, or each machine added with `tw host add`).
- **One node per workload and one per Durable Object instance**, named
  from the hostname prefix, with the tags in `tailworks.jsonc`.
- **Pool nodes** (`_pool` in `tw ls`): joined ahead of time so a new
  object gets a node fast; idle ones are deleted after an hour
  (`tw up --pool-nodes`, [concepts.md](concepts.md#pool-node-_pool)).
- **`tw link --tsnet`** on a developer machine adds an ephemeral CLI node.

`tw kill` and `tw undeploy` remove their nodes (the device is deleted when
the OAuth client has Devices Core write). `tw down --purge` revokes the
[warm keys](concepts.md#warm-keys) still in custody.

## Files

Everything is under the state dir, `~/.tailworks` (or `--state-dir`), as
you. Nothing is written outside it except the service file above and, for
the microVM tier, the setup below.

| Path | What |
|---|---|
| `bin/`, `share/tailworks/`, `VERSION` | the release `install.sh` put there (`*.prev`: the one `tw upgrade` rolls back to). An npm install keeps its binaries in npm's directory instead |
| `tw.json` | `tw up`'s config: tailnet, layout, the flags it was started with |
| `mgmt/` | mgmt's database (`mgmt.db`, SQLite, unless you [moved to Postgres](postgres.md)), its key custody, its tsnet node state and `loopback.json` |
| `hostd/` | the host agent's state: each tenant's node keys and its workloads' storage (`hostd/tenants/<t>/workloads/<w>/`: Durable Object data, the microVM tier's encrypted `do.img`), the audit key, and R2 bytes with the default `fs` backend ([r2.md](r2.md)) |
| `run/` | sockets and per-instance runtime dirs |
| `logs/` | `supervisor.log`, `mgmt.log`, `hostd.log` (`tw logs --component`) |
| `backups/` | what `tw backup` and `tw restore` write |
| `link.json`, `cli/` | developer machine only: `tw link`'s endpoint and the `--tsnet` CLI node's state |

Workloads can't read any of it: the sandbox denies `mgmt/`, `hostd/`,
`logs/`, `run/`, `tw.json` and the OAuth client file to every tenant's
runtime. Disk use, quotas and retention: [limits.md](limits.md#tenant-storage).
Removing it all: [Uninstall](INSTALL.md#uninstall).

## The microVM tier's root-owned part (Linux)

The process tier needs no root. The Linux microVM tier needs one sudo
command, `sudo ~/.tailworks/bin/tw host setup --microvm`, which adds:

- root-owned copies of `tw-vmhelper`, `firecracker` and `jailer` in
  `/usr/local/libexec/tailworks`;
- `/etc/tailworks/vmhelper.json` and `/etc/tailworks/setup.json`;
- **`tw-vmhelper.service`**, a systemd system service started at boot. It
  runs as root with a bounded capability set that includes
  `CAP_SYS_ADMIN` and `CAP_NET_ADMIN`, listens on
  `/run/tailworks/vmhelper.sock`, and serves only the uids in
  `vmhelper.json`;
- per-VM jails under `/var/lib/tailworks-vm/`;
- a **huge-page reservation** for guest RAM in
  `/etc/sysctl.d/60-tw-microvm.conf` (enough for 4 VMs by default, at most
  a quarter of RAM; `--hugepages N`);
- your user in the `kvm` group.

`sudo ~/.tailworks/bin/tw host setup --undo` removes all of it. The full
list, with the capability set:
[One-time setup](INSTALL.md#one-time-setup-the-only-sudo). On macOS the
microVM tier needs no setup.
