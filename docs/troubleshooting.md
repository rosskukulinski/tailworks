# Troubleshooting

The usual problems, in the order people hit them: [`tw up`
fails](#tw-up-fails), [a first deploy doesn't
answer](#my-first-deploy-doesnt-answer), [`tw link` or a linked
machine](#tw-link-and-linked-machines), [`tw doctor`
warns](#tw-doctor-warns). Every error code links to its entry in
[errors.md](errors.md), and `tw docs show errors.md#<code>` prints it
offline.

## `tw up` fails

`tw up` checks the OAuth client, and the policy if the client can read
it, before it mints anything
([what it checks](INSTALL.md#the-oauth-client)).
After fixing the cause, run `tw up` again.

- `mint_mode_required`: no credential was given; pass
  `--oauth-client-file` ([errors.md](errors.md#mint_mode_required); the
  OAuth client: [INSTALL.md](INSTALL.md#the-oauth-client)).
- `mint_mode_removed`: this state dir (or tenant) is in the API-key or WIF
  mode an older tailworks used
  ([errors.md](errors.md#mint_mode_removed);
  [Upgrading from API-key or WIF mode](INSTALL.md#upgrading-from-api-key-or-wif-mode)).
- `policy_missing_tag_owners`: `tag:tw` doesn't own `tag:tw-mgmt` and
  `tag:tw-host` ([errors.md](errors.md#policy_missing_tag_owners)).
- `oauth_exchange_failed`: the client secret is wrong or the client was
  revoked ([errors.md](errors.md#oauth_exchange_failed)).
- `credential_file_insecure`: a credential file is readable by others
  ([errors.md](errors.md#credential_file_insecure)).
- `sandbox_layout_unsafe` (Linux): the state dir, a credential file or the
  home dir is under a directory every workload may read
  ([errors.md](errors.md#sandbox_layout_unsafe)).
- `tailnet_required`: tailworks couldn't name the tailnet
  ([errors.md](errors.md#tailnet_required)). Tailworks won't bootstrap
  from your Mac's Tailscale login.
- `bootstrap_failed … without the tag:tw-mgmt tag`: mgmt's node joined
  without its tag ([errors.md](errors.md#bootstrap_failed)).
- `device_delete_unavailable` on `tw kill`: the kill held, but the OAuth
  client can't delete devices
  ([errors.md](errors.md#device_delete_unavailable)).
- `mgmt_failed` or `host_failed`, or tailworks stops after it started:
  the supervisor (`tw __supervise`, which runs mgmt and the host) exits 0
  only on a clean stop (`tw down`, `systemctl --user stop tw`, `launchctl
  bootout`). When mgmt or the host stops on its own (mgmt's listener
  failed, or a component kept exiting), it logs why to
  `~/.tailworks/logs/supervisor.log` (`tw logs --component supervisor`)
  and exits 1 with `mgmt_failed` or `host_failed`. Under `tw up --service`
  that non-zero exit makes systemd (`Restart=on-failure`) or launchd
  (`KeepAlive` on an unsuccessful exit) start it again within a few
  seconds; without a service, run `tw up` again.
- Logs: `tw logs --component mgmt|hostd|supervisor` (on the host only).

## My first deploy doesn't answer

The usual reasons a first `tw deploy` fails, or succeeds but the workload
doesn't answer, in the order people hit them.

The examples use the quick start's `support` app: workload tag
`tag:tw-wl-support`, object class tag `tag:tw-support-support-agent`.
Your tags are in your app's `tailworks.jsonc` (`tailnet.tags` and
`tailnet.objects`); the words are explained in [concepts.md](concepts.md).

### `curl` hangs, or the name doesn't resolve

`tw deploy` said `✓ deployed`, but
`curl http://tw-support-<id>.<tailnet>.ts.net/…` hangs or fails with
`Could not resolve host`.

**Most likely: no grant lets you reach the workload.** A tailnet policy
that doesn't allow everything lets nothing reach a new tag until a grant
does, and without a grant the node isn't even in your device's netmap.
`tw deploy` doesn't check who may call your app, so the deploy succeeds
anyway. (`tw deploy --apply-policy` writes this grant for a new app, but
not for a tag your policy already had: [the caller grant](tailworks-jsonc.md#the-caller-grant).)
Add a grant to the workload's tag on port 80 (workload nodes listen
on port 80):

```jsonc
"grants": [
  // everyone in the tailnet may call the support app
  {"src": ["autogroup:member"], "dst": ["tag:tw-wl-support"], "ip": ["tcp:80"]},
]
```

`tw deploy` prints this line next to the tagOwners snippet when it finds
new tags. Grants apply within seconds of saving the policy; nothing needs a
restart.

- `autogroup:member` is every **user** of the tailnet. A tagged device
  (a server, a CI runner, another workload) is not a member: name its tag
  in `src`.
- To call an object on its **own** node (its name in `tw ls`'s NODE
  column) rather than through the workload's URL, grant its class tag too
  (`tag:tw-support-support-agent`).
- To check from the calling device: `tailscale status` lists the workload
  node once a grant lets you see it, and `tailscale ping
  tw-support-<id>` tells you if it answers.
- Still nothing? Is the calling device on the same tailnet as tailworks, and
  connected? Then `tw status` and `tw ls` on the host: the deployment
  should be `active`.

### `identity_pending`

From **`tw deploy`**: `error [identity_pending]: support not ready after
3m0s (status …, node …)`. The deployment was placed, but its workload node
never became ready within `--wait` (3 minutes by default). The status and
node in the message say where it stopped.

- `tw status` and `tw logs support`; `tw logs --component hostd` on the
  host shows the node joining.
- If the deploy said `preflight skipped`, mgmt couldn't read your tailnet
  policy, so it didn't check your tags first. A tag with no tagOwners
  entry then fails when tailworks mints the node, usually as
  [`bootstrap_failed`](errors.md#bootstrap_failed). Add the tagOwners (see
  [below](#a-tag-isnt-in-tagowners-after-renaming-the-app)), and give the
  credential Policy File › Read so later deploys check it.
- A control plane that is slow to register nodes can push a first deploy
  past the limit: `tw deploy --wait 10m`.

From **a request** (`503`, header `tw-error: identity_pending`,
`Retry-After: 1`): a new object's own node isn't ready yet. The first
request to a new object normally waits for it (about 0.2 s with the
default [pool nodes](concepts.md#pool-node-_pool)), and answers
`identity_pending` only if the node takes longer than 30 s. Retry. If it
keeps happening, `tw ls` shows the object's status and `tw logs
support` the reason.

### A tag isn't in tagOwners after renaming the app

`tw deploy` prints `› preflight: tagOwners tag:… … ✗ not in tagOwners`
and fails with [`acl_denied`](errors.md#acl_denied).

`tw init` names an app's tags after the app (`tag:tw-wl-<name>`,
`tag:tw-<name>-<class>`; see
[tailworks-jsonc.md](tailworks-jsonc.md#tags-and-tw-init)). If you rename the app
and its tags, or run `tw init` again under a new name, the new tags need
their own tagOwners entries. `tw deploy` prints them: add them and deploy
again. Two more things a rename changes:

- Your caller grant (above) still names the old tag. Update its `dst`.
- A new `name` is a new workload. The old one keeps running under its old
  name until you `tw undeploy <old-name>`.

### `peer_not_visible` when the workload calls something

A `fetch()` from your workload to another tailnet device fails with `403`,
header `tw-error: peer_not_visible`. The destination isn't in the calling
node's netmap: no grant lets that node see it, or the name doesn't exist.
([`acl_denied`](errors.md#acl_denied) is the next step: the peer is
visible, but not on that port.) A call to an internet name, such as
`https://api.github.com/`, goes out from the host's network instead; it
gets [`internet_egress_unavailable`](errors.md#internet_egress_unavailable)
only when the workload's `tailnet.egress.internet` (or the host) turns
internet egress off ([recipes.md](recipes.md#call-an-internet-api)).

Which node calls depends on the class's mode
([concepts.md](concepts.md#facet-mode-and-plain-do-mode)): in facet mode,
the default, `fetch()` leaves from the **object's own node**, so the grant's
`src` is the class tag; from the workload's own code, or a plain-DO class
(`"egress": "workload"`), it's the workload tag.

The easy way to get the grant right: list the destination's tag in
`tailnet.connects`, and `tw deploy` checks a grant from each of the app's
tags to it, printing any that are missing:

```jsonc
// tailworks.jsonc
{ "tailnet": { "tags": ["tag:tw-wl-support"], "connects": ["tag:db"] } }
```

```jsonc
// tailnet policy: the line tw deploy prints (port 80; change it to the port the destination listens on)
{ "src": ["tag:tw-support-support-agent"], "dst": ["tag:db"], "ip": ["tcp:80"] },
```

`tw dev` has no policy evaluator (every dev node sees every other), so a
call that works in `tw dev` can still need a grant once deployed.
Destinations outside the tailnet and the host itself are refused with
[`forbidden_destination`](errors.md#forbidden_destination) instead. An
object on the same host is reached like one on another host (netd delivers
the call in process), with the same grant.

### The deployment is `failed` in `tw ls`

`tw deploy` failed with [`deploy_failed`](errors.md#deploy_failed), or
`tw ls` shows the deployment as `failed` with an `error:` line under it:

```
  support      dp_…                   h1       failed     http://…/
               error: workerd exited 6 times: … · tw logs support --console
```

The host couldn't start the app: workerd (or Node) exited. The `error:`
line is the first line of what it printed. `tw logs support --console`
shows the app's raw output (workerd's log, the Node app's log, or the
microVM console), usually with the exception or the config error that
stopped it. Fix it and `tw deploy` again. If the app runs under `tw dev`
but not deployed, compare the tier and runtime (`"tier"`, `"runtime"` in
`tailworks.jsonc`) and any [secrets](secrets.md): `.dev.vars` is for `tw dev`
only, and `tw secret put` is applied on the next deploy.

### Other deploy problems

- `acl_denied` on deploy: add the printed grant to your tailnet policy
  ([errors.md](errors.md#acl_denied)). `preflight skipped` means the
  OAuth client has no Policy File › Read scope, so mgmt can't read the
  policy.
- A Node app on Linux gets `EACCES` from `net.connect` or its own
  dispatcher: that's the sandbox; send egress through `HTTP(S)_PROXY` /
  `@tailworks/node`'s `fetch`.

## `tw link` and linked machines

- `not_admin` from `tw link`: you need
  `github.com/rosskukulinski/tailworks/cap/admin` from a grant to
  `tag:tw-mgmt`; the error prints the grant
  ([errors.md](errors.md#not_admin)).
- `mgmt_unreachable` from a linked machine: your Tailscale isn't on the
  tailnet that runs tailworks, or no grant lets you reach `tag:tw-mgmt`
  ([errors.md](errors.md#mgmt_unreachable)).
- Logs: `tw logs <workload>` works on the host or a linked machine (`-f`
  to follow). From a linked machine, `host_unavailable` means mgmt could
  not reach the workload's host on its push port
  ([errors.md](errors.md#host_unavailable)).

## `tw doctor` warns

Every `tw doctor` line has a `fix:` when it fails. The ones that need more:

- `workerd does not run on this machine`: re-sign it (`codesign -f -s -
  ~/.tailworks/bin/workerd`) and clear quarantine (`xattr -d com.apple.quarantine
  ~/.tailworks/bin/workerd`), then `tw doctor`.
- `tw doctor` says `sandbox` failed: Seatbelt (`/usr/bin/sandbox-exec`)
  or Landlock didn't start workerd or didn't enforce a deny on this OS
  version. Report it; tenants are not confined until it is fixed.
- `tw doctor` warns `NO PROCESS SANDBOX` on Linux: the kernel has no
  Landlock (older than 5.13, or `landlock` missing from the `lsm=` boot
  parameter; check `cat /sys/kernel/security/lsm`). Add it to the boot
  parameters, use a newer kernel, or deploy with tier `microvm`.
- `host_sockets`: your user can reach the system tailscaled's socket or a
  container engine, so tailworks serves one tenant only
  ([security-model.md](security-model.md#when-a-host-is-shared-between-tenants)).
