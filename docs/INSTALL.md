# Installing tailworks

tailworks runs on **macOS (Apple silicon)** and **Linux (arm64 and amd64, glibc
≥ 2.35)**. Each has a self-contained release tarball and the same
`install.sh`; nothing needs root. With Node.js, [npm](#install-with-npm)
installs the same binaries: `npx tailworks …` or `npm install -g tailworks`. Windows isn't supported, not even for
the `tw` CLI alone. Every prerequisite is in [one table](#prerequisites).

The path through this page: install on [macOS](#macos) or [Linux](#linux),
then your first deploy (the [README quick start](../README.md#quick-start),
and [troubleshooting](troubleshooting.md) if it doesn't answer), then
[upgrade](#upgrade), [back up](#back-up-and-restore) and
[uninstall](#uninstall) when the time comes. More than one machine (`tw
host add`, placement, sizing): [hosts.md](hosts.md). How workloads are
isolated: [security-model.md](security-model.md#isolation-tiers). The
words (tenant, workload, tier, …) are explained in
[concepts.md](concepts.md). Where each platform is tested, and what was
measured there: [install-testing.md](install-testing.md). What `tw up` runs,
the ports and devices it adds and the files it writes, on one page:
[footprint.md](footprint.md).

## Prerequisites

| To | You need |
|---|---|
| Run tailworks (the release) | macOS 14 or later on Apple silicon, or Linux arm64/amd64 with glibc ≥ 2.35; a Tailscale tailnet where you're an admin, or a self-hosted Headscale server ([headscale.md](headscale.md)). No Go and no Node.js. |
| Install it with npm (`npx tailworks`, `npm install -g tailworks`) | The same platforms, and Node.js ≥ 18 with npm ([Install with npm](#install-with-npm)). |
| Scaffold an app and type-check it (`npm create tailworks`) | Node.js ≥ 20 only. |
| Try an app locally (`tw dev`) | The release only: no tailnet, no credentials. |
| Build tailworks from source | Go ≥ 1.27 and Node.js ≥ 20. |
| Run a Node.js app (`"runtime": "node"`, the `node-workflow` template) | Node.js ≥ 24 on each host that runs it, and `npm install` on the deploying machine ([node-tier.md](node-tier.md)). |
| Use the Linux microVM tier | `/dev/kvm`, cgroups v2, systemd and a one-time sudo setup ([details](#linux-the-microvm-tier-firecracker-kvm)). |

Windows isn't supported, not even as a CLI client.

## Install with npm

If you have Node.js, npm installs tailworks without the tarball steps:

```sh
npx tailworks init hello && cd hello && npx tailworks dev    # try it, nothing installed globally
npm install -g tailworks && tw doctor                        # or install tw (and tailworks) on your PATH
```

The `tailworks` package is a small shim. npm installs it with one platform
package as an optional dependency: `@tailworks/cli-darwin-arm64`,
`@tailworks/cli-linux-arm64` or `@tailworks/cli-linux-amd64`, picked by
the package's `os`, `cpu` and `libc`. The platform package holds the
release tarball's own files (`tw`, `tw-mgmt`, `tw-hostd`, workerd,
the templates and their vendored modules). They are made from the
signed tarball and published with npm provenance, so the binaries are the
same ones. Neither package has an install script.

How it differs from a tarball install:

- **State** is where it always is: `~/.tailworks` (or `--state-dir`). Only
  the binaries live in npm's directory, so switching between npm and the
  tarball keeps your apps, keys and stores.
- **Upgrade with npm:** `tw backup && tw down && npm install -g tailworks@latest && tw up`.
  `tw upgrade` refuses (`npm_install`), because npm owns the directory;
  `tw upgrade --plan`, `tw version --check` and `tw doctor` print the npm
  command. npm gives you no signature check or automatic rollback, so the
  command takes a [`tw backup`](#back-up-a-running-install-tw-backup)
  first. See
  [upgrading.md](upgrading.md#installed-with-npm).
- **The login service** (`tw up --service`, on by default) needs a global
  install, which has a stable path. From `npx`, `tw up` runs in the
  background without a service, and an explicit `--service` refuses,
  because npm may clear npx's cache at any time. If you change Node.js
  versions with nvm or similar, the service keeps running the old
  version's copy until you `tw up` again from the new one.
- **No microVM tier.** The npm packages leave out the microVM guest
  (kernel and rootfs, about 200 MB). The process tier works as usual. For
  the microVM tier, install the release tarball (`scripts/get.sh`).
  `tw doctor` reports which kind of install it found.
- **`--omit=optional`** (or `--no-optional`) skips the platform package,
  and `tw` then says so. Reinstall without it.
- **Size:** about 120 MB to download and 400 MB on disk, mostly workerd.

`npm create tailworks` runs `tw init` when `tw` is on your PATH. Without
it, it copies the template and names the app's workload and tags after the
directory the same way; only `--tags` and `--object-tags` need `tw`.

To remove it: `tw uninstall`, then `npm uninstall -g tailworks` (which
`tw uninstall` prints last). `tw uninstall` also deletes your state in
`~/.tailworks` unless you pass `--keep-state` ([Uninstall](#uninstall)).

## macOS

These are the exact steps for an Apple-silicon Mac with nothing installed:
no Go, no Node.js, no Homebrew packages.

### What you need

- macOS 14 or later on Apple silicon (arm64). For Linux, see [Linux](#linux).
- A tailnet you control, where you are an **admin** (`autogroup:admin`).
- An **OAuth client** (admin console › Settings › Trust credentials; scope
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

The tarball is self-contained (darwin-arm64: about 165 MB compressed,
585 MB installed, 220 MB of it the microVM guest; the linux tarballs are
about 10% larger): the Go binaries `tw`, `tw-mgmt` and
`tw-hostd` (static, `CGO_ENABLED=0`; esbuild is built into
`tw`, so deploys need no Node.js), the pinned `workerd` (1.20261121.1, from
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
./install.sh                  # installs into ~/.tailworks; safe to re-run (repair); to upgrade, use `tw upgrade` (docs/upgrading.md): while tw runs, install.sh refuses another version
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
running `tw up` needs a restart. Its upgrade command matches how this `tw`
was installed, as `tw doctor`'s install row and `tw upgrade --plan` do:
`tw upgrade` for an `install.sh` prefix, the npm or `npx` command for npm,
`git pull && make build` for a source checkout, and `get.sh` otherwise. An interactive tailworks also prints a one-line
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
   mgmt's and the host's tags, so the client can mint their keys. Only the
   `"tag:tw": ["autogroup:admin"]` line in `tagOwners` has to be pasted by
   hand (the tag must exist before the console lets you tag a client with
   it): with **Policy File › Write** on the client (step 2), `tw up`
   (step 3) offers to write the rest. The whole snippet, to paste
   yourself instead, is in step 2 of the
   [README quick start](../README.md#quick-start) (offline: `tw docs show
   ../README.md#quick-start`); `tw up` prints the lines that are missing. To let your Prometheus
   scrape `/metrics`, add the grant in
   [observability.md](observability.md).
2. **Generate the client** (admin console › Settings › Trust credentials ›
   **Credential** › **OAuth**). When a scope asks for a tag, pick **`tag:tw`**.
   Scopes can't be changed on an existing client.

   | Scope | Without it |
   |---|---|
   | **Auth Keys › Write** | Required: nothing can be minted. |
   | **Devices › Core › Write** | `tw kill` still holds (the node is unreachable and tombstoned), but the device stays listed: the kill result says [`device_delete_unavailable`](errors.md#device_delete_unavailable). |
   | **Policy File › Write** (recommended), or **Read** | With Write, `tw up` adds the snippet and `tw deploy` (or `tw policy --apply`) each app's `tag:tw*` tagOwners, grants and Service auto-approval, and for a new app the grant that lets the tailnet's users call it ([tailworks-jsonc.md](tailworks-jsonc.md#tags-and-tw-init)); both show the lines and ask first, and only ever add. With Read you paste the lines yourself, and `tw deploy` still preflights your app's grants and `spawn()` checks sub-agent tags/caps against the policy. With neither, deploy says `preflight skipped` and `spawn()` answers `policy_unavailable`. |
   | **Services › Write** | A workload with `tailnet.service` (a Tailscale Service: a stable name and VIP, [services.md](services.md)) fails to deploy with `service_api_failed`. |

3. **Start tailworks** and paste the **Client Secret** when asked:
   ```sh
   tw up
   ```
   On a terminal, `tw up` prints the `tagOwners` and grants the policy
   lacks and asks before writing them. Without a terminal, or with
   `--json`, it only prints them: automation passes `--apply-policy --yes`
   (a bare `--yes` never writes the policy), and `--apply-policy=false`
   never asks or writes. It merges them into the policy
   (comments kept, nothing changed or removed), has the control plane
   validate the result, and writes it with `If-Match`, retrying once if
   someone saved the policy in the meantime. A tag that is already in
   `tagOwners` keeps its owners (`tag:tw-mgmt` and `tag:tw-host` only
   gain `tag:tw`). If the client has no Policy File › Write scope, `tw up`
   doesn't ask (with `--apply-policy` it says why) and goes on printing the
   lines. Answering `n` goes on the same way, unless `tag:tw` doesn't own
   `tag:tw-mgmt` yet: then nothing can be minted and `tw up` stops with
   `policy_missing_tag_owners`. With `--apply-policy`, declining stops
   `tw up` (`cancelled`) before anything is minted.

   It saves the secret to `~/.config/tailworks/oauth.env` (mode 0600; the
   client ID is part of the secret) and installs a login service (a macOS
   LaunchAgent or a systemd user unit; `--service=false` skips it). With
   no terminal (automation), write the file yourself, with the single line
   `TS_OAUTH_CLIENT_SECRET=tskey-client-…`, or pass
   `--oauth-client-file <file>`. A file other users can read is tightened
   to 0600. On Linux the file must not live under `/etc`, `/opt` or
   another OS dir the process sandbox lets workloads read
   (`sandbox_layout_unsafe`); `~/.config/tailworks/` is the place.

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
tw init support --template agent        # or hello, mcp, static-site; tw init --list for every template
cd support && tw dev                    # optional: run it locally against an embedded control plane
tw deploy                               # bundles here (built-in esbuild), uploads to mgmt over the tailnet, waits until the node is ready
tw ls · tw audit <object> · tw kill <object>
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

#### More than one machine

`tw up` runs **one process** by default: the supervisor hosts tw-mgmt and
the host agent together, so `tw-mgmt` is the only infrastructure device
tailworks adds to your tailnet. To add hosts (`tw host add`), see how mgmt
places a workload on them, or size them: [hosts.md](hosts.md).

`tw up --local-dev` keeps the old unix socket + per-host token for
`tw dev` and hermetic tests only; `tw doctor` flags it.

### Self-hosted control: Headscale

tailworks also runs on a [Headscale](https://github.com/juanfont/headscale)
server (v0.29 or later) instead of Tailscale: `tw up --control-url
<server_url> --tailnet <base_domain>`, with `HEADSCALE_API_KEY=…` in
`~/.config/tailworks/headscale.env` (or `--api-key-file <file>`; it asks
before writing the policy). The profile has no Tailscale Services,
no Funnel and no posture. See [headscale.md](headscale.md).

### 4. Day 2, stop and uninstall

`tw up` starts `tw-mgmt` (SQLite, local key custody) and `tw-hostd`
(workerd supervisor) under a detached supervisor; state and logs are in
`~/.tailworks` (`--state-dir` to use another directory; on Linux it, and
every credential file, must stay outside `/usr`, `/lib*`, `/bin`, `/sbin`,
`/etc`, `/opt`, `/nix/store` and `/run/current-system`, which the process
sandbox lets every workload read: `tw up` refuses `--state-dir
/opt/tailworks` or an OAuth client file in `/etc/tailworks/` with
`sandbox_layout_unsafe`; `/var/lib/tailworks` is fine). `tw deploy` preflights
the grants your app declares (`tailnet.connects`), bundles it, and waits
until the workload node is on your tailnet, then prints its address:
`http://tw-support-<id>.<tailnet>.ts.net/`. To call it, a grant must let
you reach the workload's tag on port 80 (`tw deploy` offers to write
one for a new app; the
[quick start](../README.md#quick-start)
shows it, and `tw deploy` prints it with the tagOwners for new tags). If
the first deploy doesn't answer, see [troubleshooting.md](troubleshooting.md).
Day 2: `tw ls`, `tw logs support`, `tw kill <object>`,
`tw audit <object>`, `tw status`.

#### A second tenant (another tailnet) on the same Mac

```sh
tw tenant add <other-tailnet>.ts.net           # asks for that tailnet's own OAuth client secret
tw deploy --tenant <other-tailnet>.ts.net      # also: ls, kill, audit, logs --tenant
tw tenant ls
```

Like the first `tw up`, `tw tenant add` asks for the OAuth client secret on
a terminal and saves it to `~/.config/tailworks/tn_<other-tailnet>-oauth.env`
(0600), reusing that file next time. Create the client in the other
tailnet's admin console as for `tw up` (Auth Keys write, tag `tag:tw`, and
`tag:tw` in its `tagOwners`). The tenant's tailnet needs nothing else in
its policy: tailworks' own nodes stay on your tailnet, and `tw tenant add`
writes nothing to the other tailnet's policy. In a script, or with `--json`
or `--state-dir`, pass the file instead:

```sh
umask 077; printf 'TS_OAUTH_CLIENT_SECRET=tskey-client-…\n' > ~/.config/tailworks/other-oauth.env
tw tenant add <other-tailnet>.ts.net --oauth-client-file ~/.config/tailworks/other-oauth.env
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
tw down                   # stop (tw up starts it again; state kept)
tw uninstall              # remove tailworks: objects, devices, service, install and ~/.tailworks
```

`tw uninstall` prints what it removes and asks first; `--keep-state` keeps
your data. See [Uninstall](#uninstall).

### Troubleshooting

When `tw up` fails, a first deploy doesn't answer, `tw link` can't reach
mgmt or `tw doctor` warns: [troubleshooting.md](troubleshooting.md)
(offline: `tw docs show troubleshooting.md`). Every error code is in
[errors.md](errors.md).

## Try it without a tailnet

`tw dev` runs an app on your machine with no tailnet, no credential and no
`tw up`: [dev-debugging.md](dev-debugging.md#try-it-without-a-tailnet)
(offline: `tw docs show dev-debugging.md#try-it-without-a-tailnet`).

## Isolation

The default **process tier** is **dev-grade isolation**: every app runs in
a workerd (or Node) process on your machine, under a per-workload process
sandbox (Seatbelt on macOS, Landlock + seccomp on Linux), with no VM
between tenants and your user account. Don't run code you don't trust on
the process tier; use the [microVM tier](#linux-the-microvm-tier-firecracker-kvm).
What each sandbox confines, and when a host may be shared between tenants:
[security-model.md](security-model.md#isolation-tiers).

## Upgrade

See [upgrading.md](upgrading.md): what may run together, upgrading a
`tw up` machine, and upgrading a fleet. Take a
[backup](#back-up-and-restore) first if you may want to go back.

## What is in ~/.tailworks

`~/.tailworks` is two directories that share a default. The **prefix**
is where `install.sh` puts the release (`TW_HOME`, `tw upgrade
--prefix`). The **state dir** is where tailworks keeps its data
(`TW_STATE_DIR`, `--state-dir`). Either can move without the other. Every
variable `tw`, `get.sh` and `install.sh` read is in
[cli.md](cli.md#environment).

`tw down --purge` deletes no files: it kills every object and deletes its
device. "Uninstall" below is [`tw uninstall`](#uninstall): it removes every
entry in both tables, unless you pass `--keep-state`, which keeps the state
dir's entries. It deletes nothing else it finds there, and names it.

**State dir**

| Entry | Written by | In `tw backup`? | Removed by uninstall? | Safe to delete by hand? |
|---|---|---|---|---|
| `tw.json` | `tw up`: how it was run (mint mode, credential file path) | yes | yes, unless `--keep-state` | no |
| `mgmt/` | tw-mgmt: `mgmt.db` (SQLite: tenants, deployments, objects, the audit trail, queues, the KV, D1 and R2 journals), `custody/` (the sealing key and your [secrets](secrets.md)), `tsnet/` (mgmt's node state), `registry/` (container image blobs) | yes, except `registry/` | yes, unless `--keep-state` | no |
| `hostd/` | tw-hostd: each workload's Durable Object storage (`hostd/tenants/<tenant>/workloads/<workload>/`), R2 object bytes with the `fs` backend ([r2.md](r2.md)), node state, `objects.json`, the audit key | DO storage and R2 bytes (not with `--hostd=false`) | yes, unless `--keep-state` | no |
| `logs/` | tw up's supervisor: `supervisor.log`, `mgmt.log`, `hostd.log` | no | yes, unless `--keep-state` | yes, after `tw down` |
| `run/` | tw up: unix sockets. When `<state>/run` would be longer than 48 bytes (macOS caps socket paths), the sockets go in `/tmp/tw-<hash>` instead | no | yes, unless `--keep-state` | yes, after `tw down` |
| `supervisor.pid` | tw up's supervisor | no | yes, unless `--keep-state` | yes, after `tw down` |
| `backups/` | `tw backup` without `--out`, `tw upgrade`, `tw restore` (`<time>-before-restore`), `tw custody migrate` | no | yes, unless `--keep-state` | yes, once you won't restore them. They hold the custody key and node state: keep them like `mgmt/` |
| `upgrade/` | `tw upgrade`: the staged release, removed when it ends | no | yes, unless `--keep-state` | yes, when no upgrade runs |
| `link.json` | `tw link` (a developer machine) | no | yes, unless `--keep-state` | yes (`tw link --unlink` does it) |
| `cli/` | `tw link --tsnet`: the CLI's own node state | no | yes, unless `--keep-state` | yes; the next `tw link --tsnet` joins a new node |
| `update-check.json` | `tw`: the once-a-day update check | no | yes, unless `--keep-state` | yes |
| `vm/` | you, if at all: hostd looks here for microVM guest assets when the release has none in `share/tailworks/vm` | no | yes, unless `--keep-state` | yes, if the release has its guest |

**Prefix**

| Entry | Written by | In `tw backup`? | Removed by uninstall? | Safe to delete by hand? |
|---|---|---|---|---|
| `bin/`, `share/`, `VERSION`, `install.sh` | `install.sh`, `tw upgrade` | no | yes | no: it is the install |
| `bin.prev/`, `share.prev/`, `VERSION.prev` | `install.sh` on an upgrade: the previous release, for `tw upgrade rollback` (or `install.sh --rollback`) | no | yes | yes, if you won't roll back |

`/tmp/tw-<hash>` (the sockets of a long state dir) goes too. Without a
working `tw` binary, `install.sh --uninstall --purge` (from the prefix or a
release directory) removes the whole prefix, the state in it included, and
`install.sh --uninstall` removes only the prefix's own entries.

## Back up and restore

What each entry in the state dir is, and which ones a backup holds:
[What is in ~/.tailworks](#what-is-in-tailworks).

### Back up a running install: `tw backup`

```sh
tw backup --out ~/tw-backup-$(date +%F)
```

`tw backup` works while tailworks runs. Nothing is stopped and no request
waits. It writes a new directory with:

- a consistent copy of `mgmt.db` (SQLite `VACUUM INTO`),
- the rest of `mgmt/`: custody, the ID secret, mgmt's node state,
- `tw.json`,
- `hostd/`: each workload's **Durable Object storage**, and with the `fs`
  backend, the **R2 object bytes** (see below),
- `backup.json`, which says what the backup holds and what it doesn't.
  Its `hostd` entry lists the workloads whose DO storage it holds and
  counts the databases and R2 blobs.

Without `--out` it writes to `~/.tailworks/backups/`. The backup holds the
custody key and mgmt's node key, so store it like a secret.

How `hostd/` is copied while workloads write to it:

- **Durable Object storage.** Each object's storage is a SQLite database.
  `tw backup` copies each one with `VACUUM INTO`, through a read-only
  connection that shares workerd's locks. Each copy is the object as of
  one committed write, never a torn one, and workerd keeps writing while
  it is taken. Objects are copied one after another, not at a single
  instant. No Durable Object transaction spans two objects, so that is
  enough.
- **R2 bytes** (`fs` backend). Blobs are immutable: a put writes a
  temporary file and renames it into place, and a blob is never
  rewritten. So every blob in the backup is whole. They are copied once
  before mgmt's store (which holds the R2 index) and again after it, to
  pick up blobs written in between. A blob on the same disk as the backup
  is hard-linked, not copied, which is safe because it never changes.
  One case is not covered: an object written *and* then overwritten or
  deleted during the backup itself can come back from the restore
  without its bytes (reading it fails). Take the backup again if that
  matters.

`tw backup --hostd=false` copies the control plane only, as `tw upgrade`'s
backup does.

It doesn't hold:

- **A microVM workload's DO volume** (`do.img`). The guest has it mounted,
  so `tw backup` lists it as not included. Copy it with tailworks stopped.
- **Node.js workloads' data directories** (`TW_DATA_DIR`). These are plain
  files the app writes. Copy them with tailworks stopped.
- **The rest of `hostd/`:** node state, logs, `objects.json`. `tw restore`
  leaves this machine's copy in place.
- **Other hosts.** `tw backup` copies this machine's `hostd/` only. Workloads
  on a host added with `tw host add` keep their DO storage on that host.
- **R2 bytes in an `s3` backend.** Back up the S3 bucket.
- **`mgmt/registry/`:** container image blobs. If you deploy container
  images, copy it separately; it is content-addressed, so a copy taken
  while tailworks runs is fine.
- **Your OAuth client file** (`~/.config/tailworks/oauth.env`).
- **A Postgres store.** `pg_dump` it ([postgres.md](postgres.md#back-up-and-restore-with-postgres)).

For one database, `tw d1 export <db> --output <file>` also works while
tailworks runs ([d1.md](d1.md)). `tw upgrade` takes the control-plane part
of this backup before it installs, in `~/.tailworks/backups/`
([upgrading.md](upgrading.md)).

To copy everything, microVM volumes and Node.js data included, stop
tailworks first:

```sh
tw down
tar -czf tw-state-$(date +%F).tgz -C ~ .tailworks
tw up
```

To restore that copy, `tw down`, put the directory back in the same place,
and run `tw up`.

### Restore: `tw restore`

```sh
tw down
tw restore ~/tw-backup-2026-11-21 --yes
tw up
```

`tw restore` needs tailworks stopped. It also takes a `tw upgrade` backup.
It:

1. copies the state it replaces to `~/.tailworks/backups/<time>-before-restore`,
   so you can undo the restore with another `tw restore`;
2. puts the backup's `mgmt/` and `tw.json` back;
   if the backup holds `hostd/`, it also replaces every workload's Durable
   Object storage and the R2 blobs with the backup's. What it replaces
   moves into the safety backup from step 1. DO storage of workloads
   created after the backup moves there too, so a workload that is
   created again later starts empty;
3. runs the **restore fence**: every lease epoch moves up past any epoch
   handed out since the backup, so no fencing token
   ([locks.md](locks.md)) is ever issued twice.

Everything that changed after the backup is gone: KV and D1 writes, Queue
messages, deploys and secrets. Store objects on the host whose copy is
ahead of the restored journal start over from it on their next sync.
Objects created after the backup are not in it, and their devices may be
left in the admin console. Delete them there.

Restore on the same machine, or on a new one that takes its place. Never
run two copies of the same state at once: they share node identities.

### When to switch to Postgres

SQLite in `~/.tailworks/mgmt/mgmt.db` suits one machine, or one mgmt with a
few hosts. Move mgmt's store to Postgres when the store's disk must
outlive the machine (a managed database with its own backups), or before
you run more than one mgmt replica. A replica set keeps the API answering
while one replica is down; one mgmt on Postgres doesn't. `tw-mgmt migrate
--from sqlite` copies a stopped install's store, then `tw up --db-file`
runs on it. HA runs `tw-mgmt` directly on each replica machine. Both are in
[postgres.md](postgres.md).

## Uninstall

```sh
tw uninstall --dry-run        # what it would do and delete
tw uninstall                  # do it (asks first; --yes without a terminal)
```

It ends in one of three places:

- **`tw uninstall`**: nothing of tailworks is left on this machine. It
  kills every object and deletes its device (`tw down --purge`), deregisters
  this host (with `tw up --separate-host`), stops tailworks and removes its
  login service, deletes mgmt's own `tw-mgmt` node with your OAuth client
  (or Headscale API key), removes tailworks' skills from the coding agents
  it finds (`tw agent remove`), removes the install (`bin/`, `share/`, the
  `.prev` copies, `VERSION`, `install.sh`) and deletes the state dir. That
  includes `mgmt.db` (every tenant's KV, D1, R2 and queue data), the audit
  trail and `backups/`. Take a `tw backup --out <a directory outside
  ~/.tailworks>` first if you may want any of it back.
- **`tw uninstall --keep-state`**: the same, but the state dir's data stays.
  `tw.json` then forgets mgmt's deleted node, so a later `tw up` joins a new
  one.
- **A developer machine** (`tw link`, no `tw up`): it removes `link.json`,
  `cli/` and the install.

It refuses, before it changes anything:

- **The microVM tier's helper is installed** (Linux, `tw host setup
  --microvm`). Undoing it needs root and this `tw` binary, so it goes
  first: `sudo ~/.tailworks/bin/tw host setup --undo`, then `tw uninstall`.
- **tailworks is set up but mgmt isn't running or healthy.** The purge needs
  mgmt. Run `tw up` first, or pass `--force` and delete the objects'
  devices in the admin console › Machines yourself.
- **Other hosts are registered with this mgmt.** Uninstalling it would
  orphan them. `tw host drain` and `tw host rm` each one first.

Installed with npm? `tw uninstall` removes everything but the package and
prints `npm uninstall -g tailworks` to run last. If its OAuth client may
not delete devices, it says which node to delete in the admin console ›
Machines.

It never changes your tailnet policy. It prints what it deliberately
leaves, for you to do if you're done with tailworks: revoke the OAuth
client and delete its file (`~/.config/tailworks/oauth.env`), remove the
tailworks tags and grants from the policy, drop a Postgres store or setec
entries (`tw up --db`, `--custody setec`), `loginctl disable-linger` on
Linux, and the `PATH` line in your shell profile. What it removes, entry by
entry: [What is in ~/.tailworks](#what-is-in-tailworks).

With no working `tw` binary, `install.sh --uninstall --purge` (from
`~/.tailworks` or a release directory) removes the whole prefix. It does
none of the rest: run `tw down --purge` before it if you can.

## Linux

The same flow on a Linux host: one tarball per architecture, `install.sh`
into `~/.tailworks` without root, `tw doctor`, `tw up`. Which distros it was
tested on, and the measured timings, are in
[install-testing.md](install-testing.md#linux-support-matrix).

> **Isolation on Linux.** The **process tier** runs every tenant's workerd
> and Node app under **Landlock + seccomp** (unprivileged; no user
> namespaces needed), with the guarantees listed in
> [security-model.md](security-model.md#what-the-process-tier-sandbox-confines).
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
| `side_channels` | every `/sys/devices/system/cpu/vulnerabilities/*` entry is mitigated or not affected, SMT off, KSM off, no `mitigations=off` | warn, naming each problem and when it matters (a shared host, a Node-runtime workload, the microVM tier; [security-model.md](security-model.md#side-channels)); on macOS: skip, the posture can't be read |
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
<id>`, then `tw-hostd -join-token-file <id>.join.env` there) do hosts get their own
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
  Tested on arm64 with OAuth-only minting and the Landlock sandbox
  ([install-testing.md](install-testing.md#linux-support-matrix)).
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
  Ubuntu 24.04 arm64. On amd64, `make e2e-vm` passes on a nested KVM test
  host (2026-11-22); containers on amd64 are not verified yet
  ([details](install-testing.md#the-microvm-tier)).
- For `cloudflare/sandbox` containers on arm64: tailworks' arm64 rebuilds in
  mgmt's registry. They aren't published yet, so build them once with Docker
  and `tw image push` them ([containers.md](containers.md#platforms)).
- `cryptsetup`, `losetup`, `mkfs.ext4`, `ip` (Ubuntu/Debian cloud images
  have them; else `apt-get install cryptsetup-bin util-linux e2fsprogs iproute2`).
- For tenants that don't trust each other, a host set up as Firecracker's
  [production host setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md)
  describes: the kernel's side-channel **mitigations on** (no
  `mitigations=off`; nothing `Vulnerable` in
  `/sys/devices/system/cpu/vulnerabilities/`, current microcode), **SMT
  off** (`nosmt`), and **KSM off** (`/sys/kernel/mm/ksm/run` = 0). A guest
  runs native code, so these are what separates its tenant from the
  others on the same cores. `tw doctor` reports them (`side_channels`);
  see [security-model.md](security-model.md#side-channels).

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
DO volumes stay in `~/.tailworks` until `tw uninstall`.
