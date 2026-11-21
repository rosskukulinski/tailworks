# Installing tailworks

tailworks runs on **macOS (Apple silicon)** and **Linux (arm64 and amd64, glibc
≥ 2.35)**. Each has a self-contained release tarball and the same
`install.sh`; nothing needs root.

The path through this page: install on [macOS](#macos) or [Linux](#linux),
then your first deploy (the [README quick start](../README.md#quick-start-deploy-your-first-durable-object),
and [troubleshooting](troubleshooting.md) if it doesn't answer), then
[upgrade](#upgrade) and [uninstall](#uninstall) when the time comes. Also
here: [trying an app without a tailnet](#try-it-without-a-tailnet), how
workloads are [isolated](#isolation), and [tuning](#tuning-how-fast-a-new-objects-own-node-comes-up).
The words (tenant, workload, tier, …) are explained in
[concepts.md](concepts.md). Where each platform is tested, and what was
measured there: [install-testing.md](install-testing.md).

## macOS

These are the exact steps for an Apple-silicon Mac with nothing installed:
no Go, no Node.js, no Homebrew packages.

### What you need

- macOS 14 or later on Apple silicon (arm64). For Linux, see [Linux](#linux).
- A tailnet you control, where you are an **admin** (`autogroup:admin`).
- An **OAuth client** (admin console › Settings › OAuth clients; scope
  Auth Keys write with tag `tag:tw`) saved in a 0600 file:
  [The OAuth client](#the-oauth-client). No browser login, no Funnel, no
  HTTPS needed.
- A policy snippet in your tailnet policy (tagOwners: `tag:tw` owns
  `tag:tw-mgmt` and `tag:tw-host`; [below](#the-oauth-client)).

tailworks **never uses your Mac's Tailscale app or tailscaled**, its login, its
state, or its LocalAPI. Every tailworks node (mgmt's own node and every object
node) is a separate in-process (tsnet) node of the tailnet your OAuth client belongs to.
If Tailscale.app is running on a different (personal) tailnet, it is left
alone; `tw doctor` checks this. The one place your machine's own Tailscale
*is* used is a **developer machine** running `tw link`: there the CLI is
not a node at all; it talks to mgmt over your existing Tailscale
connection, which is exactly the identity mgmt wants to see.

### 1. Get the release

The tarball is self-contained (~87 MB compressed, ~300 MB installed): the Go binaries `tw`, `tw-mgmt`,
`tw-hostd`, `tw-netd` (static, `CGO_ENABLED=0`; esbuild is built into
`tw`, so deploys need no Node.js), the pinned `workerd` (1.20261115.1, from
npm `@cloudflare/workerd-darwin-arm64`), the app templates and the
`node_modules` they need (vendored, so `tw init` and `tw deploy` work
offline).

The quickest way is [`scripts/get.sh`](../scripts/get.sh). It downloads
the latest [GitHub release](https://github.com/rosskukulinski/tailworks/releases)
for this machine, checks the release signature and `SHA256SUMS`, and runs
its `install.sh` (step 2): `curl -fsSLO https://raw.githubusercontent.com/rosskukulinski/tailworks/master/scripts/get.sh && sh get.sh`.
To do it by hand, or from a tarball you built (`make release`):

```sh
mkdir -p ~/Downloads/tailworks && cd ~/Downloads/tailworks
# download (or copy) tailworks-<version>-darwin-arm64.tar.gz, SHA256SUMS and SHA256SUMS.sig here
# (verify the signature first: "Verify a release" below)
shasum -a 256 -c SHA256SUMS --ignore-missing
tar -xzf tailworks-*-darwin-arm64.tar.gz
```

#### Verify a release

Every published release is signed. `get.sh`, `install.sh` and tw-hostd's
self-upgrade check the signature themselves. To check one by hand, take
the pinned keys from the repository, not from the release:

```sh
curl -fsSLO https://raw.githubusercontent.com/rosskukulinski/tailworks/master/internal/releasesig/allowed_signers
ssh-keygen -Y verify -f allowed_signers -I tw-release -n tw-release -s SHA256SUMS.sig <SHA256SUMS
```

`ssh-keygen` ships with OpenSSH on macOS and Linux. A good release prints
`Good "tw-release" signature`. `install.sh` refuses an unsigned or badly
signed release unless you pass `--allow-unsigned`, which is meant for
tarballs you built yourself. A local build (`make release`, version
`x.y.z-<commit>`) is unsigned, and `install.sh` installs it with a warning.

The binaries are **unsigned except for an ad-hoc signature** (`codesign -s -`,
required to run arm64 code). Files copied with `scp`, `curl` or `tar` are not
quarantined, so Gatekeeper doesn't get involved. If you downloaded the
tarball with a browser, `install.sh` removes the `com.apple.quarantine`
attribute from its own copy.

### 2. Install (no sudo)

```sh
cd tailworks-*-darwin-arm64
./install.sh                  # installs into ~/.tailworks; safe to re-run (repair); to upgrade, use `tw upgrade` (docs/upgrading.md)
export PATH="$HOME/.tailworks/bin:$PATH"
echo 'export PATH="$HOME/.tailworks/bin:$PATH"' >> ~/.zprofile   # the installer prints this; it doesn't edit your profile
```

It checks the release signature (`SHA256SUMS.sig`) and `SHA256SUMS`, checks the platform, copies `bin/` and `share/`
into `~/.tailworks`, runs `workerd --version` and `tw version`, and touches
nothing outside `~/.tailworks`.

### 3. Check the machine

```sh
tw doctor
```

It checks that workerd runs, that the built-in bundler can build every
template offline, that `~/.tailworks` (and the state dir) are writable, that the
process sandbox (Seatbelt on macOS, Landlock + seccomp on Linux) works and
enforces, and that tailworks is not using the system tailscaled. Every line has
a `fix:` if it fails. Once tailworks is up, the `mint_mode` row shows the OAuth
client tailworks mints with (it warns if the client file is readable by anyone
but you, and fails if the state is in a mint mode an older tailworks used:
[Upgrading from API-key or WIF mode](#upgrading-from-api-key-or-wif-mode)).
`api_key` is `skip` except on the Headscale profile.

Optional: tab completion for every command, subcommand and flag.

```sh
echo 'eval "$(tw completion bash)"' >> ~/.bashrc                  # bash
tw completion zsh > "${fpath[1]}/_tw"                            # zsh (then restart it)
tw completion fish > ~/.config/fish/completions/tw.fish          # fish
```

`tw version --check` says whether a newer release is out and whether the
running `tw up` needs a restart. An interactive tailworks also prints a one-line
notice when a newer release is out, at most once a day (never after `tw init`
or `tw dev`). `CI=1`, `--json` or `TW_NO_UPDATE_CHECK=1` turns it off. What
it sends: [security-model.md](security-model.md#telemetry-and-the-update-check).

tailworks mints a Tailscale auth key for every node it runs (mgmt's own node,
this host's node, every object), so it needs **one** credential: an OAuth
client of your tailnet. `tw up` without one prints how to create it and
exits.

Two machines appear below: the **host** (runs `tw up`: mgmt, the host
agent, your apps' nodes) and a **developer machine** (your laptop: `tw
link`, `tw deploy`). They can be the same machine; then skip `tw link`.

### The OAuth client

An OAuth client scoped to auth keys for `tag:tw`: the same mechanism the
Tailscale Kubernetes operator and GitHub Action use. Its secret never
expires and stays in a 0600 file on the host. **No browser login, no
Funnel, no HTTPS requirement.**

1. **Policy first** (admin console › Access controls). `tag:tw` must own
   mgmt's and the host's tags, so the client can mint their keys:
   ```jsonc
   "tagOwners": {
     "tag:tw":      ["autogroup:admin"],
     "tag:tw-mgmt": ["autogroup:admin", "tag:tw"],
     "tag:tw-host": ["autogroup:admin", "tag:tw"],
   },
   "grants": [
     // hosts call mgmt's API; mgmt pushes kills to the hosts
     {"src": ["tag:tw-host"], "dst": ["tag:tw-mgmt"], "ip": ["tcp:443", "tcp:80"]},
     {"src": ["tag:tw-mgmt"], "dst": ["tag:tw-host"], "ip": ["tcp:7474"]},
     // admins may use tw over the tailnet (tw link)
     {"src": ["autogroup:admin"], "dst": ["tag:tw-mgmt"], "ip": ["tcp:443", "tcp:80"], "app": {"github.com/rosskukulinski/tailworks/cap/admin": [{}]}},
   ],
   // no nodeAttrs: mgmt never uses Funnel
   ```
   `tag:tw` must exist before the console lets you tag a client with it.
2. **Generate the client** (admin console › Settings › OAuth clients ›
   Generate OAuth client, <https://login.tailscale.com/admin/settings/oauth>):
   - **Auth Keys › Write**, tag **`tag:tw`** (required);
   - optionally **Devices › Core › Write**, tag **`tag:tw`**: `tw kill`
     then also deletes the device (without it the kill still holds, the
     node is unreachable and tombstoned, but the device stays listed;
     the kill result says `device_delete_unavailable` with this fix);
   - optionally **Policy File › Read**: `tw deploy` preflights your
     app's grants and `spawn()` checks sub-agent tags/caps against the
     policy (without it, deploy says `preflight skipped` and `spawn()`
     answers `policy_unavailable`);
   - optionally **Services › Write**: workloads with `tailnet.service` get
     a Tailscale Service, i.e. a stable name and VIP
     ([services.md](services.md)). Without it, such a deploy fails with
     `service_api_failed`.
3. **Save it** where only you can read it (tailworks refuses a looser mode):
   ```sh
   mkdir -p ~/.config/tailworks
   printf 'TS_OAUTH_CLIENT_ID=%s\nTS_OAUTH_CLIENT_SECRET=%s\n' '<client id>' '<tskey-client-…>' > ~/.config/tailworks/oauth.env
   chmod 600 ~/.config/tailworks/oauth.env
   ```
4. **Start tailworks:**
   ```sh
   tw up --oauth-client-file ~/.config/tailworks/oauth.env --service --yes
   ```
   Before minting anything, `tw up` exchanges the client for an access
   token (a wrong or revoked secret stops here: `oauth_exchange_failed`),
   reads the tailnet name from the client's devices (or from mgmt's node
   once it joins; `--tailnet` overrides), and, if the client can read the
   policy, checks that `tag:tw` owns `tag:tw-mgmt` and `tag:tw-host`.
   If not, or if the control plane refuses the first key with
   `requested tags … are invalid or not permitted`, it stops with
   **`policy_missing_tag_owners`** and the exact tagOwners snippet;
   nothing is minted. Then the client mints mgmt's node key
   (`tag:tw-mgmt`) and, through mgmt, the host's (`tag:tw-host`); both
   join with no login. Tailworks stores the file's **path** and the client's
   public ID only; `tw-mgmt` re-reads the secret at each token exchange
   (cached until shortly before it expires; a 401 triggers one fresh
   exchange) and never logs it.

mgmt serves its API on its node over plain HTTP on `:80` (WireGuard
encrypts it; WhoIs authenticates every request) and, when the tailnet has
HTTPS certificates, on `:443` too (mgmt fetches the certificate in the
background at start). `tw link` tries https first, then http, and says
which it used.

To rotate or upgrade the client (e.g. add Devices Core), generate a new
one, overwrite the file (0600), run `tw up` again (it updates the
tenant), then revoke the old client in the console. Revoking the client is
also your kill switch for all future minting.

#### Upgrading from API-key or WIF mode

tailworks used to mint with a Tailscale API key (`tw up --api-key-file`) or
through workload identity federation (`tw up --wif`). Both modes are gone
(ADR-042). A state dir set up in one of them still starts
and its running nodes keep serving, but nothing new can be minted: `tw up` stops with **`mint_mode_removed`**
and `tw doctor` fails its `mint_mode` row. To switch, create the OAuth
client above (policy, client, 0600 file), then:

```sh
tw up --oauth-client-file ~/.config/tailworks/oauth.env --yes
```

This keeps the tenant, its deployments and mgmt's node; it only changes
how keys are minted. A second tenant added with `tw tenant add` switches
with `tw tenant set <tenant> --oauth-client-file <its client file>`.
Afterwards:

- **API key:** revoke the key in the admin console (Settings › Keys);
  tailworks no longer reads it.
- **WIF:** delete the trust credentials (Settings › Trust credentials),
  remove the `tw-issuer` machine, and remove `tag:tw-issuer` and its
  `funnel` nodeAttrs from your policy.

### Running tailworks

`tw up … --service` on macOS installs the LaunchAgent
`~/Library/LaunchAgents/dev.tailworks.up.plist` (per user, no sudo) so the
supervisor starts at login; `tw down --service` removes it. **Sleep:** a
sleeping Mac serves nothing. `tw up` prints a hint if sleep is enabled:
`sudo pmset -a sleep 0 displaysleep 10`, or run `caffeinate -s` in a
terminal, or use a Mac mini that doesn't sleep.

#### On a developer machine: `tw link`

Your laptop is on the same tailnet with the Tailscale app, as yourself (an
`autogroup:admin` member, or a user/group/tag granted `github.com/rosskukulinski/tailworks/cap/admin`
on `tag:tw-mgmt`). Install tailworks there too (steps 1–2), then:

```sh
tw link tw-mgmt.<tailnet>.ts.net       # https first, then http on :80; or just `tw link`: finds tw-mgmt through MagicDNS
tw init support --template agent        # or hello, mcp, ai-agent, lock; node-workflow needs Node.js on this machine
cd support && tw dev                    # optional: run it locally against an embedded control plane
tw deploy                               # bundles here (built-in esbuild), uploads to mgmt over the tailnet, waits until the node is ready
tw ls · tw audit <object> · tw kill <object> --cascade
```

`tw link` calls mgmt's `GET /v1/whoami` over your own Tailscale
connection. mgmt identifies the caller by **WhoIs** (your login, or your
node's tags) and admits admins by the `github.com/rosskukulinski/tailworks/cap/admin` capability; it
stores only the endpoint in `~/.tailworks/link.json`. No key, no token, nothing
to rotate. If you are not an admin, `tw link` fails with `not_admin` and
prints the grant to add. A team member granted `github.com/rosskukulinski/tailworks/cap/tenant-admin`
links too, limited to the tenants that grant names ([teams.md](teams.md)). `tw ls` shows `by <login>` on each deployment
and `tw audit` carries your login on the deploy record. `tw logs
<workload> [-n N] [-f]` works from here too: mgmt reads the workerd (or
node) log from the host that holds the workload, over the same mgmt → host
channel it uses for kills. A workload with `replicas` prints every
replica's lines prefixed `[<host>]`; `--host <id>` picks one. The daemons'
own logs (`tw logs --component mgmt|hostd|supervisor`) stay on the host.
`tw link --unlink` forgets the endpoint.

#### Transport: tsnet only

Normal operation never uses a unix socket or a token. mgmt serves the
`/v1` API (OpenAPI: `api/openapi.yaml`, rendered in
`docs/contracts/mgmt-api.md`) on its own tailnet node `tw-mgmt`
(`tag:tw-mgmt`), authenticated by WhoIs. tw-hostd runs its **own** node
`tw-host-<hostID>` (`tag:tw-host`), joined once with a single-use key
mgmt mints through the tenant's OAuth client, and talks to mgmt
over the tailnet; mgmt pushes kills to that node (port 7474). The `tw` CLI
on the host machine reaches mgmt through the node's loopback proxy
(`~/.tailworks/mgmt/loopback.json`, 0600), so it is the operator by WhoIs; a
developer machine uses `tw link` (its own Tailscale) or `tw link --tsnet`
(no Tailscale installed: an ephemeral CLI node with an interactive login,
state under `~/.tailworks/cli`). `tw doctor` has a `transport` row; `ok tsnet:`
is the expected answer. The policy additions `tw up` prints include the
host ⇄ mgmt grants (`tag:tw-host → tag:tw-mgmt tcp:80,443`,
`tag:tw-mgmt → tag:tw-host tcp:7474`).

#### One process by default; scale out with `tw host add`

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
it (pending) and writes a single-use join key file; on the new machine run
`tw-hostd join --host-id <id> --token-file <file> --control-url …`, then
start `tw-hostd -host-id <id> -mgmt http://tw-mgmt.<tailnet> -tsnet-control-url …`.
`tw host ls` / `tw host rm <id> [--drain]` manage hosts; `tw tenant set`
(PATCH) and `tw tenant rm`, `tw undeploy <workload>` round out the CRUD.

#### Where a workload runs (placement, multi-host)

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
  // "placement": { "targets": ["pg-primary"], "aggregate": "worst" }  // or "sum" over several targets
}
```

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

`tw up --local-dev` keeps the old unix socket + per-host token for
`tw dev` and hermetic tests only; `tw doctor` flags it.

### Self-hosted control: Headscale

tailworks also runs on a [Headscale](https://github.com/juanfont/headscale)
server (v0.29 or later) instead of Tailscale: `tw up --control=headscale
--control-url <server_url> --tailnet <base_domain> --api-key-file <file with
HEADSCALE_API_KEY=…> --policy-apply`. The profile has no Tailscale Services,
no Funnel and no posture. See [headscale.md](headscale.md).

### 4. Day 2, stop and uninstall

`tw up` starts `tw-mgmt` (SQLite, local key custody) and `tw-hostd`
(workerd supervisor) under a detached supervisor; state and logs are in
`~/.tailworks` (`--state-dir` to use another directory). `tw deploy` preflights
the grants your app declares (`tailnet.connects`), bundles it, and waits
until the workload node is on your tailnet, then prints its address:
`http://tw-support-<id>.<tailnet>.ts.net/`. To call it, a grant must let
you reach the workload's tag on port 80 (the
[quick start](../README.md#quick-start-deploy-your-first-durable-object)
shows it, and `tw deploy` prints it with the tagOwners for new tags). If
the first deploy doesn't answer, see [troubleshooting.md](troubleshooting.md).
Day 2: `tw ls`, `tw logs support`, `tw kill <object> --cascade`,
`tw audit <object>`, `tw status`.

#### A second tenant (another tailnet) on the same Mac

```sh
tw tenant add <other-tailnet>.ts.net --oauth-client-file ~/.config/tailworks/other-oauth.env   # that tailnet's own OAuth client
tw deploy --tenant <other-tailnet>.ts.net      # also: ls, kill, audit, logs --tenant
tw tenant ls
```

Each tenant gets its own netd registry (its tailnet's control plane and
netmap only), its own workerd/Node processes, each in its own sandbox, 0700
state dirs, throttles and audit chain. Objects of one tenant can't resolve
or reach the other's (`peer_not_visible`). On a host without a process
sandbox, `tw tenant add` refuses (`sandbox_unavailable`) unless tailworks was
started with `tw up --insecure-shared-host`. Each tenant needs its own
OAuth client file (created in that tailnet's admin console); `tw tenant
add` refuses one that already serves another tenant.

```sh
tw down --purge           # kill every object and delete its devices, then stop
~/Downloads/tailworks/tailworks-*-darwin-arm64/install.sh --uninstall --purge   # remove ~/.tailworks
```

`--uninstall` without `--purge` keeps your state (`tw.json`, logs, data).
Installed with `get.sh`? It doesn't keep the release directory; see
[Uninstall](#uninstall) for the full list.

### Troubleshooting

- `workerd does not run on this machine`: re-sign it (`codesign -f -s -
  ~/.tailworks/bin/workerd`) and clear quarantine (`xattr -d com.apple.quarantine
  ~/.tailworks/bin/workerd`), then `tw doctor`.
- `mint_mode_required` from `tw up`: pass `--oauth-client-file`; the
  error prints how to create the client.
- `mint_mode_removed`: this state dir (or tenant) is in the API-key or WIF
  mode an older tailworks used; switch it to an OAuth client
  ([Upgrading from API-key or WIF mode](#upgrading-from-api-key-or-wif-mode)).
- `policy_missing_tag_owners`: `tag:tw` must own `tag:tw-mgmt` and
  `tag:tw-host`; add the printed tagOwners and re-run. Nothing was minted.
- `credential_file_insecure`: `chmod 600` the OAuth client file.
- `oauth_exchange_failed`: the client secret is wrong or the client was
  revoked; generate a new one (Settings › OAuth clients).
- `device_delete_unavailable` on `tw kill`: the kill held, but the OAuth
  client has no Devices › Core scope; generate one with it, overwrite the
  file, `tw up`, revoke the old client; remove leftover devices in the
  console (Machines).
- `bootstrap_failed … without the tag:tw-mgmt tag`: control dropped the
  tag because `tag:tw` doesn't own `tag:tw-mgmt`; add the tagOwners
  snippet, remove the node in the console, `tw up` again.
- `not_admin` from `tw link`: you need `github.com/rosskukulinski/tailworks/cap/admin` from a grant
  to `tag:tw-mgmt`; the error prints the grant.
- `mgmt_unreachable` from a linked machine: is your Tailscale connected to
  the tailnet that runs tailworks, and does a grant let you reach
  `tag:tw-mgmt` on tcp:443?
- `tailnet_required`: tailworks couldn't name the tailnet (the OAuth client can't
  list devices and mgmt's node hasn't joined yet); pass `--tailnet`. Tailworks
  won't bootstrap from your Mac's Tailscale login.
- `acl_denied` on deploy: add the printed grant to your tailnet policy, or
  deploy with `--allow-missing-grants`. `preflight skipped` means the
  OAuth client has no Policy File › Read scope, so mgmt can't read the
  policy.
- `tw doctor` says `sandbox` failed: Seatbelt (`/usr/bin/sandbox-exec`)
  or Landlock didn't start workerd or didn't enforce a deny on this OS
  version. Report it; tenants are not confined until it is fixed.
- `tw doctor` warns `NO PROCESS SANDBOX` on Linux: the kernel has no
  Landlock (older than 5.13, or `landlock` missing from the `lsm=` boot
  parameter; check `cat /sys/kernel/security/lsm`). Add it to the boot
  parameters, use a newer kernel, or deploy with tier `microvm`.
- A Node app on Linux gets `EACCES` from `net.connect` or its own
  dispatcher: that's the sandbox; send egress through `HTTP(S)_PROXY` /
  `@tailworks/node`'s `fetch`.
- The supervisor (`tw __supervise`, which runs mgmt and the host) exits 0
  only on a clean stop (`tw down`, `systemctl --user stop tw`, `launchctl
  bootout`). When mgmt or the host stops on its own (mgmt's listener
  failed, or a component kept exiting), it logs why to
  `~/.tailworks/logs/supervisor.log` (`tw logs --component supervisor`)
  and exits 1 with `mgmt_failed` or `host_failed`. Under `tw up --service`
  that non-zero exit makes systemd (`Restart=on-failure`) or launchd
  (`KeepAlive` on an unsuccessful exit) start it again within a few
  seconds; without a service, run `tw up` again.
- Logs: `tw logs <workload>` (on the host or a linked machine; `-f` to
  follow), `tw logs --component mgmt|hostd|supervisor` (on the host only).
  From a linked machine, `host_unavailable` means mgmt could not reach the
  workload's host on its push port (the policy grant `tag:tw-mgmt` →
  `tag:tw-host` `tcp:7474`, or the host is down).
- `tw deploy` worked but the app doesn't answer, `identity_pending`,
  `peer_not_visible`, or a deployment `failed` in `tw ls`: see
  [troubleshooting.md](troubleshooting.md).

## Try it without a tailnet

You don't need a tailnet, a credential or `tw up` to run an app. `tw dev`
runs it on your machine with the same host agent (tw-hostd, netd) and
workerd as production. In place of your tailnet, it starts a test control
plane and relay on 127.0.0.1, with an in-memory mgmt. A local port reaches
the app through a dev client node, so `getCaller()` sees a real (dev)
caller.

```sh
tw init hello --template agent && cd hello
tw dev                      # ready on http://127.0.0.1:8787 · edits reload · Ctrl-C to stop
tw dev --once --json        # start, make one request, print it, exit (a smoke test)
```

`tw dev --help` lists the flags (`--port`, `--once`, `--workerd`, `-v`).
The `mcp` template serves on `/mcp` (its `/` answers 404); `tw dev` says
so and its smoke test sends an MCP `initialize` there. Not in dev yet: a
policy evaluator. Every dev node sees every other and gets the same test
capabilities, so a call that works in dev can still need a grant on a
tailnet.

## Isolation

tailworks' default is the **process tier**: every app runs in a workerd (or
Node) process on your machine, and each Durable Object gets its own tailnet
node and identity. V8 isolates and tailworks' egress guard are real boundaries,
but this is **dev-grade isolation**: there is no VM between tenants and
your user account. On top of that, tw-hostd runs every tenant's workerd
**and Node app** in a per-workload **process sandbox**, on by default
(`tw-hostd -sandbox auto`):

- **macOS: Seatbelt** (`sandbox-exec`, generated profile).
- **Linux: Landlock + seccomp** (no root and no user namespaces needed;
  kernel 5.13+ with Landlock enabled, e.g. Ubuntu 22.04+/24.04, Debian 12,
  Fedora). A small launcher in tw-hostd applies the rules, then execs
  workerd or node in place.

Either way a workload can't read your home dir (including `~/.tailworks`),
tailworks' state (node keys, mgmt custody, other tenants' storage) or any
tenant's API key file, and can't use mgmt's or hostd's sockets. **On
macOS** a workload can also write only its own dirs, open no network
except its own unix sockets and the TCP ports hostd gives it (a Node app:
its egress proxy and its own port), signal only itself and its children,
and exec only its runtime, its own dirs and the OS dirs; it can still
read what the user can outside the denied paths
([security-model.md](security-model.md#what-the-process-tier-sandbox-confines)).
`tw-hostd -sandbox seatbelt-legacy` brings back the previous files-only
profile for one release.
**Linux adds more:** a workload can write only its own instance and data
dirs and read only the OS, its runtime binary and its install prefix;
with Landlock ABI ≥ 4 (kernel 6.7+; Ubuntu 24.04's 6.8 has ABI 4) it may
open TCP only to its own app port and egress proxy, so a malicious Node
app can't bypass netd's proxy (below ABI 4, TCP is unrestricted); a seccomp filter refuses ptrace and
`process_vm_readv/writev` (so no reading a sibling's memory), kexec,
mount, bpf, perf_event_open, keyctl, new namespaces, io_uring, and
UDP/ICMP/raw/MPTCP sockets; and other processes' `/proc` entries (their
environment, cmdline) are unreadable. Workloads get **no unix sockets**
(seccomp; workerd's listener is inherited and its services are
token-checked loopback TCP), so the host's tailscaled or docker socket is
out of reach, and they may signal only themselves. Tailworks' own sockets
refuse sandboxed peers too. As defence in depth, if *your user* can reach
the system tailscaled's socket or a container engine (or is in the
docker/podman/lxd groups), `tw doctor` warns (`host_sockets`) and tailworks
serves one tenant only unless `tw up --insecure-shared-host`: run tailworks as
a dedicated user to share a host.

`tw doctor` checks that the sandbox enforces on your machine (Linux: the
Landlock ABI too), and while tailworks runs, that every workload is under it.
**Without a sandbox** (Linux without Landlock) `tw doctor` warns loudly,
and tailworks refuses to run a second tenant's process-tier workloads unless you
pass `tw up --insecure-shared-host`. **microVM isolation needs Linux with
KVM (Firecracker) or macOS (Virtualization.framework)**; see
[COMPAT.md](COMPAT.md#containers) and [the Linux microVM tier](#linux-the-microvm-tier-firecracker-kvm).
Don't run code you don't trust on the process tier.

## Upgrade

See [upgrading.md](upgrading.md): what may run together, upgrading a
`tw up` machine, and upgrading a fleet. Take a
[backup](#back-up-and-restore) first if you may want to go back.

## Back up and restore

Everything tailworks keeps is in its state dir (`~/.tailworks`, or `--state-dir`):

- `mgmt/`: the control plane. `mgmt.db` (SQLite) holds tenants,
  deployments, objects, the audit trail, queues and the journals of KV,
  D1 and R2 metadata. `custody/` holds the sealing key and your
  [secrets](secrets.md), and `tsnet/` holds mgmt's node state.
- `hostd/`: each workload's Durable Object storage
  (`hostd/tenants/<tenant>/workloads/<workload>/`) and, with the default
  `fs` backend, R2 object bytes ([r2.md](r2.md)).
- `tw.json`: how `tw up` was run (mint mode, credential file path).

To back up, stop tailworks so SQLite is consistent, copy the directory, and
start it again:

```sh
tw down
tar -czf tw-state-$(date +%F).tgz -C ~ .tailworks
tw up …
```

The copy contains node keys and the custody key: store it like a secret.
To restore, `tw down`, put the directory back in the same place, and run
`tw up`. Never run two copies of the same state at once: they share node
identities. For one database, `tw d1 export <db> --output <file>` works
while tailworks runs ([d1.md](d1.md)). Your OAuth client file
(`~/.config/tailworks/oauth.env`) is not in the state dir. `tw upgrade` takes
its own backup before it installs, in `~/.tailworks/backups/`
([upgrading.md](upgrading.md)).

## Uninstall

```sh
tw agent remove                               # if you installed the coding-agent skills
tw down --purge --service                     # kill every object, delete its device, stop, remove the service
sudo ~/.tailworks/bin/tw host setup --undo          # Linux, only if you ran host setup --microvm
rm -rf ~/.tailworks                                  # or: <release dir>/install.sh --uninstall --purge
```

`tw down --purge` deletes the objects' devices (and revokes unused warm
keys), but not tailworks' own nodes: remove `tw-mgmt` (and `tw-host-<id>`
if you had one) in the admin console › Machines. Then, if you're done with
tailworks on this tailnet, revoke the OAuth client, delete `~/.config/tailworks/oauth.env`, remove the tailworks
tags and grants from your tailnet policy, and remove the `PATH` line from
your shell profile.

## Tuning: how fast a new object's own node comes up

A new Durable Object answers through its workload in milliseconds. Its
**own** tailnet node (its MagicDNS name, tags and grants) normally needs a
key minted, a registration at the coordination server and a first network
check. `tw up` keeps that off the critical path with three settings. The
defaults are on, and each `tw up` stores them for the service:

| flag | default | what it does | what it costs |
|---|---|---|---|
| `--warm-keys N` | 5 | keeps up to N pre-minted, single-use keys per tag set, so a new object skips the key-API call (145 ms normally, seconds when the API is slow) | unused keys sit in tailworks' custody for up to 30 min; `tw down --purge` revokes them |
| `--pool-nodes N` | 1 | keeps N pre-joined nodes per class that had a new object on this host in the last hour; the next new object of that class is renamed onto one (~0.2 s) instead of joining a new node (1.5 s typical, 4–6 s when control is slow) | each pool node is a device on your tailnet (listed under `_pool` in `tw ls`); idle ones are deleted after an hour without new objects |
| `--port-mapping` | off | asks your router for port mappings (UPnP / NAT-PMP / PCP) for tailworks' nodes | off saves ~250 ms on every new node and keeps tailworks' nodes from each asking the router for a mapping; turn it on only if this host is behind a NAT that needs router-assisted direct connections (otherwise traffic goes direct or over DERP as usual) |

Measured on a production tailnet, with all three at their defaults: a new
object's first request on its own node arrives **~0.23 s** after it is
created (p95 ~0.3 s), down from ~7 s
([how it was measured](install-testing.md#fast-identity)).
`--warm-keys 0 --pool-nodes 0` turns both pools off.

## Linux

The same flow on a Linux host: one tarball per architecture, `install.sh`
into `~/.tailworks` without root, `tw doctor`, `tw up`. Which distros it was
tested on, and the measured timings, are in
[install-testing.md](install-testing.md#linux-support-matrix).

> **Isolation on Linux.** The **process tier** runs every tenant's workerd
> and Node app under **Landlock + seccomp** (unprivileged; no user
> namespaces needed), with the guarantees listed under [Isolation](#isolation).
> `tw doctor` reports the kernel's Landlock ABI and that the sandbox
> enforces; without Landlock it warns loudly and tailworks refuses to host a
> second tenant on the process tier. The **microVM tier** (Firecracker,
> jailer, encrypted per-workload DO volume) needs `/dev/kvm` and the
> one-time `sudo tw host setup --microvm` ([below](#linux-the-microvm-tier-firecracker-kvm));
> `tw doctor` reports it as available or unavailable, with the reasons.

### What you need

- **glibc ≥ 2.35, arm64 or amd64.** Ubuntu 22.04+, Debian 12+, Fedora 36+,
  RHEL/Alma/Rocky 10 all qualify. The pinned workerd itself imports
  symbols up to `GLIBC_2.27`; 2.35 is the floor tailworks is tested and
  supported on. **Not supported:** Alpine and other musl distros, Debian 11
  (2.31), RHEL/Alma/Rocky 8–9 (2.28/2.34), Amazon Linux 2 (2.26).
  `install.sh` refuses them with a clear message, and so does `bin/tw doctor`
  (a static binary, which runs anywhere).
- Kernel ≥ 5.13 with Landlock (the process sandbox; ABI ≥ 4, i.e. kernel
  6.7+, also restricts TCP; all the distros above have Landlock), cgroups v2 (the default), systemd (for `tw up --service`).
- No Node.js, Go or npm. `curl`/`scp` and `tar` to get the tarball.
- The same as on macOS: a tailnet where you are an admin and an
  [OAuth client](#the-oauth-client) file (`tw up --oauth-client-file …`;
  no HTTPS needed).

tailworks **never uses the machine's tailscaled**. If a system Tailscale is
installed and logged in to some other tailnet, it is left alone: every tailworks
node is its own tsnet node of the tailnet your OAuth client belongs to.
While tailworks runs, `tw doctor` checks that no tailworks process holds tailscaled's
socket or state. It reads `/proc/<pid>/fd` and matches unix-socket peers with
`ss -x`, and never connects to tailscaled.

### Install

```sh
mkdir -p ~/tw-release && cd ~/tw-release
# download (or copy) tailworks-<version>-linux-arm64.tar.gz (or -linux-amd64), SHA256SUMS and SHA256SUMS.sig here
# (or skip this block: `sh get.sh` does all of it, as on macOS; "Verify a release" checks the signature)
sha256sum -c SHA256SUMS --ignore-missing
tar -xzf tailworks-*-linux-*.tar.gz
cd tailworks-*-linux-*/ && ./install.sh          # no sudo; refuses to run as root
export PATH="$HOME/.tailworks/bin:$PATH"
echo 'export PATH="$HOME/.tailworks/bin:$PATH"' >> ~/.profile
tw doctor
```

On Linux, `install.sh` checks the architecture (`uname -m`: `aarch64` →
arm64, `x86_64` → amd64) and glibc before copying anything. On bash it
suggests `~/.profile`, because creating a `~/.bash_profile` on Debian or
Ubuntu would make bash skip `~/.profile`.

`tw doctor` on Linux adds these checks:

| check | ok when | otherwise |
|---|---|---|
| `glibc` | glibc ≥ 2.35 (and ≥ workerd's ELF `GLIBC_` floor) | **fail**: musl, or glibc too old (the message names the distro and the fix) |
| `kernel` | ≥ 5.13 | warn |
| `landlock` | `landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION)` returns an ABI ≥ 1 (the `sandbox` check then proves it enforces; ABI < 4 means TCP isn't restricted) | warn: `ENOSYS` (not built in) or `EOPNOTSUPP` (not in `lsm=`) |
| `user_namespaces` | informational (tailworks doesn't need them; Ubuntu 24.04's AppArmor restriction is reported) | |
| `kvm` | `/dev/kvm` opens read-write and answers `KVM_GET_API_VERSION` | skip, with the `usermod -aG kvm` fix if it's a permission problem |
| `firecracker` | firecracker (+ jailer) on PATH, in `~/.tailworks/bin` or `/usr/local/bin` | skip / warn (no jailer) |
| `cgroups_v2` | unified hierarchy (and what's delegated to your user manager) | warn |
| `systemd` | your systemd user manager answers; reports linger and `tw.service` | warn: no systemd / no user session |
| `microvm_tier` | KVM + firecracker + cgroups v2 + guest image + privileges | skip: "microVM tier unavailable: …", with every reason |
| `system_tailscaled_not_used` | no tailworks process holds tailscaled's socket or state | **fail** |

### Run tailworks as a systemd user service

```sh
tw up --oauth-client-file ~/.config/tailworks/oauth.env --service --yes           # as on macOS
systemctl --user status tw.service
```

`--service` writes `~/.config/systemd/user/tw.service`. That unit runs the
same supervisor as plain `tw up` (`tw __supervise`: by default one
process with mgmt and the embedded host agent; with `--separate-host`,
tw-mgmt then tw-hostd as children) with `Restart=on-failure`, and tailworks
enables and starts it. Logs go to the same `<state>/logs/` (`mgmt.log` and
`hostd.log` in both layouts).

**One process by default; scale out with `tw host add`.** As on macOS,
`tw up` adds one device to your tailnet (mgmt's node) and runs the host
agent inside the same process; only when you add machines (`tw host add
<id>`, then `tw-hostd join` + `tw-hostd` there) do hosts get their own
`tag:tw-host` nodes, which is also what the printed `tag:tw-host` policy
lines are for. `systemctl --user restart tw` brings tailworks back
in a few seconds, and deployed workloads rejoin with the same tailnet nodes
and IPs. A plain `tw down` stops it through systemd but leaves the unit
enabled, so it starts again at your next login or boot.
`tw down --service` stops, disables and removes it.

A user service stops when your last session ends, unless lingering is on.
`tw up --service` and `tw doctor` tell you when it's off. Tailworks never runs
sudo for you, so to keep it running after logout and start it at boot, run:

```sh
sudo loginctl enable-linger "$USER"
```

Across a restart or a reboot, **persistent** nodes (the default) rejoin from
their saved state with the same node IDs and IPs. **Ephemeral** nodes
(`tw up --ephemeral`) are logged out when tailworks stops, and control rejects
their saved state when they come back; tailworks notices (the backend sits in
`NeedsLogin`), deletes the dead device, mints a fresh key and rejoins
**under the same hostname**, so the MagicDNS name is stable but the node ID
and IP change.

**After a reboot** (with lingering on), the service starts at boot and
every object rejoins from its saved state: same node IDs, nothing
re-minted. A client that talked to the workload just before the reboot can
take up to about 15 s more to reach it, because it keeps sending on its old
WireGuard session until that times out; other clients reach it within a few
seconds. Measured times are in
[install-testing.md](install-testing.md#restarts-and-reboots).

### Per-distro notes

- **Ubuntu 24.04**: works out of the box (glibc 2.39, kernel 6.8, Landlock ABI 4).
  AppArmor restricts unprivileged user namespaces by default; tailworks doesn't use
  them, and doctor reports it.
- **Ubuntu 22.04**: glibc 2.35, exactly the floor. Kernel 5.15 has Landlock ABI 1,
  the oldest Landlock.
- **Debian 12**: glibc 2.36, kernel 6.1 (Landlock ABI 2). No extra packages needed;
  doctor's process checks use `/proc` and `ss`, not `lsof`.
- **Fedora 45 (Cloud)**: glibc 2.44, kernel 7.2 (Landlock ABI 10). No extra
  packages and no policy changes; everything runs as your user under `~/.tailworks`.
- **amd64**: the same release built for x86-64 (`@cloudflare/workerd-linux-64`).
- **Debian 11** (glibc 2.31) is refused by `install.sh` and fails doctor's
  `glibc` check. The pinned workerd does start there (`workerd --version`),
  because its ELF needs only `GLIBC_2.27`, but tailworks is not tested below 2.35.
  Debian 11's kernel 5.10 also has no Landlock.
- **Alpine / musl**: refused. workerd can't even be exec'd: there is no glibc
  loader. The same goes for RHEL/Alma/Rocky 8–9 and Amazon Linux 2 (glibc too
  old).

## Linux: the microVM tier (Firecracker, KVM)

On Linux with KVM, tailworks can run each workload in its own **Firecracker
microVM** instead of a host process. Every tailnet identity (node keys,
auth keys) stays in tw-hostd on the host; the guest gets only plaintext
DO storage and workerd. A linux-arm64 or linux-amd64 release **built with
the microVM assets** ships everything the tier needs in
`~/.tailworks/share/tailworks/vm/`, for its arch: the guest kernel (`vmlinux`, Linux
6.1 LTS with Firecracker's CI config plus erofs/overlayfs; `kernel.json` has
its hashes), the read-only guest image (`rootfs.ext4`, reproducible;
`manifest.json` has its hashes and arch), Firecracker and its jailer
(v1.17), plus `bin/tw-vmhelper`. Every input is pinned in
`guest/inputs.sh`; `make vm-assets GUEST_ARCH=amd64|arm64` builds either
arch on either. A guest of the wrong arch is refused (`tw doctor`:
`microvm_tier warn … is arm64, this host is amd64`).

> **Check your tarball.** The guest image can only be built on Linux.
> `make release` includes it when it runs on Linux or when Lima `tw-dev`
> is running on the Mac (`scripts/vm-assets-lima.sh` builds it there and
> caches it); otherwise it prints a warning and the tarball has **no
> microVM tier**: `share/tailworks/release.json` says `"microvm": false`,
> `share/tailworks/vm/` is absent, and `tw doctor` reports `firecracker: skip …
> install a linux release built with the microVM assets`. The process tier
> (Landlock + seccomp) is unaffected. The GitHub release workflow builds every guest
> on Linux runners and refuses to publish a tarball without one
> (`RELEASE_VM_ASSETS=1`), so a GitHub release always has the tier;
> `release.json` says `"microvm": true`.

### What you need

- Linux arm64 or amd64 (x86_64) with `/dev/kvm` (bare metal, or a VM with
  nested virtualization), cgroups v2 and systemd. Tested end to end on
  Ubuntu 24.04 arm64. On amd64 the guest boots to workerd, but the tier is
  not yet verified end to end
  ([why](install-testing.md#the-microvm-tier)).
- `cryptsetup`, `losetup`, `mkfs.ext4`, `ip` (Ubuntu/Debian cloud images
  have them; else `apt-get install cryptsetup-bin util-linux e2fsprogs iproute2`).

### One-time setup (the only sudo)

```sh
./install.sh                                   # as you, into ~/.tailworks (no sudo)
sudo ~/.tailworks/bin/tw host setup --microvm       # sudo's PATH lacks ~/.tailworks/bin: use the full path
tw doctor                                     # "microvm_tier: ok"
```

`tw host setup --microvm` (for `$SUDO_USER`, or `--user <name>`):

- installs **root-owned copies** of `tw-vmhelper`, `firecracker` and `jailer`
  in `/usr/local/libexec/tailworks` (root never runs code from your writable `~/.tailworks`);
- writes `/etc/tailworks/vmhelper.json` (which uids may use the helper) and
  `/etc/tailworks/setup.json` (what to undo);
- installs and starts **`tw-vmhelper.service`** (systemd, enabled at boot):
  root, with a bounded capability set (`CAP_NET_ADMIN CAP_SYS_ADMIN
  CAP_SYS_CHROOT CAP_SETUID CAP_SETGID CAP_CHOWN CAP_FOWNER CAP_DAC_OVERRIDE
  CAP_DAC_READ_SEARCH CAP_MKNOD CAP_KILL CAP_SYS_RESOURCE CAP_IPC_LOCK`),
  `NoNewPrivileges`, and `Delegate=yes` for the per-VM cgroups. It listens on
  `/run/tailworks/vmhelper.sock` and authorizes every connection by its peer uid;
- adds you to the `kvm` group (tailworks itself doesn't depend on it);
- reserves 2 MiB **huge pages** for guest RAM (`/etc/sysctl.d/60-tw-microvm.conf`;
  default enough for 4 VMs, at most ¼ of RAM; `--hugepages N`). Under nested
  virtualization they cut workerd start-up from ~3.7 s to ~0.5 s.

After that **tailworks runs as you**: `tw up …` as usual. `tw up` prints
`tiers: process [Landlock+seccomp, landlock (ABI 4)], microvm [Firecracker,
jailed, via tw-vmhelper; "tier": "microvm" in tailworks.jsonc]`. Without the
setup the host offers the process tier only: mgmt places `"tier": "microvm"` on a host that offers it (`503 no_host` if none), and a host that cannot run it refuses it (`tier_unavailable`), never running it in the process tier;
and `tw doctor` says why (`microvm_tier: warn … fix: sudo
~/.tailworks/bin/tw host setup --microvm`).

### Deploying in a microVM

Add `"tier": "microvm"` to the app's `tailworks.jsonc`, then `tw deploy`.
`tw status --json` shows the VM (pid, boot times, jail, volume). Each VM:

- runs through the **jailer**: its own chroot (`/var/lib/tailworks-vm/jail/…`),
  its own uid/gid, its own network namespace, cgroup v2 `cpu.max` (vCPUs)
  and `memory.max`, and Firecracker's default seccomp filters;
- has a **persistent, encrypted DO volume** per (tenant, workload):
  `~/.tailworks/hostd/tenants/<t>/workloads/<w>/do.img`, a LUKS2 image opened with
  dm-crypt **on the host**. Its key is created in tw-mgmt's custody and
  released only to the host that holds the workload's lease, at that epoch.
  The guest sees plaintext ext4 on `/dev/vdc`; the key never enters it.
  DO SQLite and KV survive a VM crash (SIGKILL), a redeploy, and a host
  reboot or power cut (the helper starts at boot; run `tw up` again, and
  objects rejoin with the same nodes);
- is attached to its volume **alone**: a redeploy stops the old VM (which
  unmounts and releases the volume) before the new one starts, and the
  helper refuses a second attach (`volume_busy`) or an older epoch
  (`volume_fenced`).

**Fast starts.** tailworks keeps a small **warm pool** of pre-booted VMs that hold
no tenant code (`tw-hostd -vm-pool`; auto: 2 within the huge-page
reservation; `-1` turns it off). A deploy or restart claims one and loads the
workload into it, and the VM is never reused for another workload. When the
pool is empty, a VM is **restored from a base snapshot** of a warm VM that
holds no tenant code and no keys (`-vm-snapshot`, on by default) instead of
booting one. Use `-vm-snapshot-refill` to refill the pool by restoring too:
refills are faster, but claims are slower. Each restored VM gets private
copies of its memory pages; no page is shared between VMs. The base is
rebuilt at every start and every hour (`-vm-snapshot-rotate`). The numbers
are in [install-testing.md](install-testing.md#the-microvm-tier).

Check a running VM: `tw host vmcheck <workload> [--keyscan]` (guest memory,
DO volume fsync latency, and a scan of the guest's disks, files and full RAM
for any node key, `tskey-` or the volume key).

### Removing it

```sh
tw down
sudo ~/.tailworks/bin/tw host setup --undo
```

This stops the service (and every microVM), and removes the unit,
`/usr/local/libexec/tailworks`, `/etc/tailworks`, `/var/lib/tailworks-vm`, the sysctl file
(huge pages back to their old value) and the `kvm` membership it added. Your
DO volumes stay in `~/.tailworks` until `install.sh --uninstall --purge`.
