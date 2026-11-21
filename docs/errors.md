# tailworks error catalog

Every refusal in tailworks is typed. The same code appears wherever the error
surfaces:

- **From `fetch()` on the tailnet** (netd ingress, egress, hostd): an HTTP
  status, a `tw-error: <code>` header and a JSON body
  `{code, message|detail, missing?, snippet?, doc_url, trace_id?}`.
- **From the SDK** (`@tailworks/tailnet`, `@tailworks/mcp`, `@tailworks/node`): a thrown
  `TailnetError` with `.code`, `.status`, `.missing`, `.snippet`, `.docUrl`.
- **From the mgmt REST API** (`/v1`, `docs/contracts/mgmt-api.md`): the same
  body; `trace_id` correlates with `tw audit --trace <id>`.
- **From the `tw` CLI**: `error [code]: message`, detail lines, `fix:` and a
  link to this page. With `--json`, one line on stderr:
  `{"error": {code, message, hint, doc_url, detail?, fix?, trace_id?}}`
  ([cli-json.md](cli-json.md)); `tw docs show errors.md#<code>` prints an
  entry offline.

The `doc_url` / link is `https://github.com/rosskukulinski/tailworks/blob/master/docs/errors.md#<code>`,
so every code below is a heading. Codes are alphabetical; the source column
says who emits it (netd = data plane and SDK, mgmt = REST API, hostd = host
agent, cli = `tw`). Detail text is redacted of key material before it is
printed anywhere.

### acl_denied
403 · netd, mgmt, cli. **When:** the peer is visible in the netmap but the port or service is not granted; from `tw deploy`, the preflight found grants or tagOwners your app's `tailnet.connects` needs missing. **Fix:** add the printed grant (`snippet`) to the tailnet policy, or deploy with `--allow-missing-grants`.

### already_joined
409 · mgmt. **When:** a credential (auth key) was requested for an object that already has a node. **Fix:** nothing to do; hostd retries by rejoining. Report it if it repeats.

### already_running
409 · cli. **When:** `tw up` finds a supervisor already running from this state dir. **Fix:** `tw status`; `tw down` first if you meant to restart.

### already_stopped
409 · cli. **When:** `tw undeploy <workload>` of a workload whose latest deployment is already undeployed. **Fix:** `tw ls --all` to see what is live.

### ambiguous_object
400 · cli. **When:** the object reference you gave (`tw kill`, `tw audit`) matches more than one object. **Fix:** name it by object ID (`tw ls` prints them).

### ambiguous_tenant
400 · cli. **When:** `--tenant` named a tailnet that has more than one tenant (several teams on one tailnet, [teams.md](teams.md)). **Fix:** name the tenant by its ID; the error lists them (`tw tenant ls`).

### approval_not_pending
409 · hostd, mgmt, cli. **When:** a decision (`tw approve`, the admin UI, `POST /_tw/approvals/<workflow>/<instance>`) names a Workflow instance that is not waiting for an approval: already approved or rejected, timed out, or it never asked. **Fix:** `tw approvals [workload] --all` shows what is waiting and what was decided ([approvals](approvals.md)).

### approval_requires_identity
Thrown in the Worker · platform. **When:** app code sent an event of type `approval` or `approval-*` to a Workflow instance (for example the Agents SDK's `approveWorkflow()`). On tailworks an approval is a decision by a tailnet identity, so only tailworks delivers it. **Fix:** approve with `tw approve <workload> <instance>`, the admin UI, or a same-origin `POST /_tw/approvals/<workflow>/<instance>` from the app's page on the workload's node ([approvals](approvals.md)).

### audit_unverified
500 · cli. **When:** `tw audit` found an audit chain whose hashes or host signatures do not verify (tampering, or a store problem). **Fix:** treat the trail as suspect; `tw logs --component mgmt` and report it.

### bad_bundle
400 · mgmt. **When:** a deploy bundle has no `tailworks.jsonc`, or neither `tailnet.tags` nor `tailnet.objects`. **Fix:** add `"tailnet": { "tags": ["tag:tw-wl-<name>"] }` to `tailworks.jsonc` (`tw init` templates show the shape).

### bad_cursor
400 · mgmt. **When:** a `cursor` query parameter does not decode. **Fix:** pass `nextCursor` from the previous page unchanged, with the same filters.

### bad_request
400 · netd, mgmt, hostd, cli. **When:** a malformed body, parameter, filter or command line (the CLI uses it for every usage error: missing argument, unknown template, unreadable key file, bad `tailworks.jsonc`). **Fix:** the detail says what was wrong; `tw <command> --help` lists the flags.

### bootstrap_failed
502 · netd, mgmt, cli. **When:** an object's node could not be created: the control plane refused the mint (tag not owned, key invalid), the token exchange failed, or the node never joined. From `tw up`, mgmt's or the host's own node failed to join. **Fix:** check the tagOwners snippet (`policy_missing_tag_owners`), the credential (`tw doctor`), then `tw logs --component mgmt`.

### build_failed
500 · cli. **When:** esbuild (workerd apps) or `create-tailworks build-node` (Node apps) could not bundle the app. **Fix:** the compiler output is in the detail; fix the source and re-run. `Could not resolve "@tailworks/…"` means tailworks' own JS packages are missing next to its bundler: in a source checkout run `make deps` (a plain `make build` doesn't install them), otherwise reinstall tailworks; `tw doctor` checks it.

### bundle_pruned
409 · mgmt, cli. **When:** `tw rollback` to a deployment whose bundle mgmt no longer holds: mgmt keeps the bundles of each workload's newest deployments (tw-mgmt `-bundle-retention`, default 10) and prunes older ones, keeping their record and hash. Nothing is deployed. **Fix:** `tw deployments <workload>` marks pruned bundles with `*`; roll back to a retained one, or check out that version and `tw deploy` it.

### cancelled
0 · cli. **When:** you answered no at a `tw up`, `tw agent setup` or `tw agent remove` confirmation prompt, or typed something other than the name at a delete's prompt. **Fix:** nothing was started, written or deleted; re-run (with the policy in place for `tw up`), or add `--yes` to skip the prompt.

### cap_limit_exceeded
403 · netd (SDK `@tailworks/mcp`). **When:** the caller holds the tool but a numeric limit or budget in its grant is exceeded (e.g. `maxUsd`, `perDayUsd`). **Fix:** raise the limit in the `app` capability of the grant, or wait for the daily budget to reset.

### cap_missing
403 · netd (SDK `@tailworks/mcp`), mgmt (`/mcp`, approvals), hostd, cli. **When:** the caller's app capability lacks the tool (or the capability itself) the MCP server requires. From tailworks' own MCP server: a `deploy`, `kill` or `undeploy` call without a `github.com/rosskukulinski/tailworks/cap/mcp` grant naming the tool, or, on the tailworks host, a `tw mcp` bridge started without `--allow <tool>`. **Fix:** add the `app` capability with the tool to the grant from the caller's tag to the server's tag (the error's `snippet` has it); for tailworks' MCP server, ask the tailnet admin, don't work around it. For approvals: the approver's `github.com/rosskukulinski/tailworks/cap/approve` grants do not cover the workload, Workflow, step, decision or a param limit (`missing` says which); grant it to the approver on the workload's tag (the app's page, `/_tw/approvals`) and on `tag:tw-mgmt` (`tw approve`, the admin UI) ([approvals](approvals.md)).

### confirmation_required
1 · cli. **When:** a command that asks first had no terminal to ask on (a coding agent, a script, or `--json`), so it did nothing. These commands ask first: `tw agent setup` and `tw agent remove` (they would write or delete files), `tw upgrade`, and every command that deletes data or a tenant's resources: `tw d1 delete`, `tw kv namespace delete`, `tw queues rm`, `tw queues purge`, `tw r2 bucket rm`, `tw tenant rm` and `tw host rm`. **Fix:** run it in a terminal and answer the prompt (a delete asks you to type the name), or add `--yes`. An agent should tell the user what will be lost (and show `--dry-run` where there is one) and add `--yes` only after the user agreed to that action.

### credential_file_insecure
403 · cli. **When:** the OAuth client file given to `tw up --oauth-client-file` is readable by other users. **Fix:** `chmod 600 <file>` then `tw up` again.

### d1_error
400 · mgmt, cli. **When:** a statement of `tw d1 execute`, `tw d1 migrations apply` or `POST …/d1/{name}/query` failed: SQLite's error (syntax, constraint, no such table) or a D1 limit (statement over 100 KB, over 100 parameters, a value over 2 MB, a result over 16 MiB, one write over 2 MiB encoded). Nothing of the batch was applied (a failing migration isn't recorded). In a Worker the same failures throw `D1_ERROR: …`, as on Cloudflare. **Fix:** fix the SQL; split a large import into several files or batches.

### delegation_exceeds_parent
403 · mgmt, netd. **When:** `ctx.tailnet.spawn()` asked for capabilities, tags or destinations the parent object does not have (`missing: {src, dst, cap, field}`). **Fix:** spawn with a subset of the parent's grants, or widen the parent's grant in the policy.

### delegation_invalid
403 · mgmt, hostd, netd. **When:** a spawn names a child tag not owned by the parent's tags, the parent is unknown (404) or dead (410), or an object tried to kill something that is not itself or a descendant. **Fix:** fix the `tagOwners` so the parent's tag owns the child's; kill through an ancestor or the CLI.

### deploy_crashloop
500 · cli. **When:** `tw deploy` was waiting for the deployment and its workerd, after it had answered its health check once, kept exiting: the deployment is `crashloop`. The host keeps restarting it with exponential backoff (200 ms doubling to 5 min) and never gives up; it reports `active` again once a restart answers. The detail lines are the attempt count and workerd's last log lines. **Fix:** `tw logs <workload>`; fix the Worker and deploy again. See [observability.md](observability.md#crash-loops).

### deploy_failed
500 · cli. **When:** the host could not start the workload (bad bundle or config, or workerd or node exited before it ever answered its health check); the host does not retry it. `tw dev` prints workerd's output. **Fix:** read the detail lines; `tw logs <workload>` on the host.

### derp_unreachable
502 · netd (SDK). **When:** no direct path to the peer and no reachable DERP relay. **Fix:** check the host's outbound connectivity (UDP 3478, TCP 443 to DERP).

### device_delete_unavailable
403 · mgmt, cli. **When:** a kill held (node closed, tombstoned) but the device could not be deleted: the tenant's OAuth client lacks the `devices:core` scope. **Fix:** generate an OAuth client with Auth Keys (write) and Devices › Core (write), overwrite the file, `tw up`; remove leftover devices in the admin console.

### dial_failed
502 · netd. **When:** the tailnet dial to a visible peer failed (connection refused, timeout). **Fix:** is the destination up and listening on that port? Try again; check `tw logs`.

### doctor_failed
1 · cli. **When:** `tw doctor` had at least one failing check. **Fix:** each failing line prints its own `fix:`.

### egress_unenforced
409 · hostd. **When:** a `"runtime": "node"` deployment on a host with no process sandbox that already serves other tenants: the Node tier's egress proxy is cooperative there, which would be a cross-tenant SSRF vector. **Fix:** use the workerd runtime, a single-tenant host, Linux with Landlock, or (tenants not isolated) `tw up --insecure-shared-host`.

### exists
409 · mgmt, cli. **When:** create of an ID that already exists (`tw host add <id>` for a known host). **Fix:** `tw host rm <id> --drain`, then add again; or pick another ID.

### feature_unavailable
409 · mgmt, cli. **When:** the deployment needs a host feature (`kv`, `r2`, `d1`, `telemetry`, …) that no eligible host advertises, or the workload's current host runs a tailworks too old to offer it. Hosts report their features when they register ([upgrading.md](upgrading.md)). **Fix:** upgrade the hosts (`tw upgrade --plan` lists them), or redeploy with `--replace` to move off the old host.

### forbidden
403 · mgmt, hostd. **When:** the caller's role is fine but the record is not its own: a host reading another host's deployment or lease, a request to a workerd instance from the wrong peer; a host node asking for the admin UI (`/ui/`); or, with the message `cross-site request refused`, a browser-originated mutation (it carried `Origin` or `Sec-Fetch-Mode`) whose `Sec-Fetch-Site` was not `same-origin`/`none`, whose `Origin` was not the request's own host and one of mgmt's own names, or which hit `/v1` without the `X-Tw-UI: 1` header (docs/ADMIN-UI.md). **Fix:** usually a bug or stale state; `tw status` and report it. For the cross-site refusal, use the admin UI from mgmt's own URL (`http(s)://tw-mgmt.<tailnet>/ui/`), never through another origin; CLI and hostd clients send none of those headers and are never affected.

### forbidden_destination
403 · netd. **When:** egress to a destination class netd refuses: loopback, link-local, multicast, cloud metadata, the object itself, a sibling object on the same host, the host, or a non-tailnet IP address (ADR-019; an internet name is [`internet_egress_unavailable`](#internet_egress_unavailable)). **Fix:** only tailnet peers are reachable from an object; talk to siblings through the tailnet name of another host, or `tw up --allow-sibling-egress` for dev.

### funnel_required
412 · cli. **When:** `tw deploy` of an app with tailworks.jsonc `"public"` routes, and the tenant policy has no `funnel` node attribute for `tag:tw-public` (ADR-029). **Fix:** add the printed `nodeAttrs` entry for `tag:tw-public` (never `tag:tw-mgmt`) and deploy again.

### helper_failed
500 · cli. **When:** `tw host setup --microvm` installed `tw-vmhelper.service` but it did not come up. **Fix:** `journalctl -u tw-vmhelper`.

### host_failed
1 · cli. **When:** `tw up` (single process): the host agent embedded in tw-mgmt failed to start or to register with mgmt. Also the supervisor's exit error (in `supervisor.log`) when tw-hostd (two processes) kept exiting and the supervisor gave up; it exits 1 so the service manager restarts it. **Fix:** the detail has the cause; `tw logs --component hostd` and `tw doctor`, then `tw up` again (under `tw up --service` it restarts on its own).

### host_holds_leases
409 · mgmt, cli. **When:** `tw host rm <id>` while the host still holds namespace leases. **Fix:** `tw host rm <id> --drain`.

### host_identity_mismatch
403 · mgmt. **When:** a `tag:tw-host` node claims a host ID bound to another node. **Fix:** each host needs its own ID (`tw host add <new-id>`); a replaced machine needs `tw host rm` first.

### host_not_registered
403 · mgmt. **When:** a `tag:tw-host` node calls mgmt before `PUT /v1/hosts/{id}`. **Fix:** start tw-hostd with the join key file from `tw host add`; it registers itself.

### host_token_invalid
403 · mgmt. **When:** a request on the local-dev unix socket carried a wrong `Tw-Host-Token`. **Fix:** `tw up --local-dev` writes the token; restart hostd from the same state dir.

### host_token_required
401 · mgmt. **When:** a request on the local-dev unix socket carried no `Tw-Host-Token`. **Fix:** as above; in normal (tsnet) operation this socket is not used.

### host_token_unconfigured
503 · mgmt. **When:** mgmt in local-dev mode was started without a host token; it fails closed. **Fix:** `tw down`, then `tw up` (or `tw up --local-dev`) again.

### host_unavailable
503 · cli. **When:** tw-hostd is not answering, or did not register with tw-mgmt after `tw up`. **Fix:** `tw logs --component hostd`.

### hostd_unavailable
503 · hostd, netd (SDK). **When:** the host agent could not be reached from the platform wrapper (hostapi socket down). **Fix:** `tw status`; the host is restarting or dead.

### identity_conflict
409 · mgmt. **When:** an object ID collided across identities (two workloads derived the same ID). **Fix:** rename one object or class; report it (IDs are HMAC-derived and this should not happen).

### identity_pending
503 · netd, mgmt, cli. **When:** the object's node is minted but not ready yet (`Retry-After: 1`); `tw dev` waited 60 s for a deploy. **Fix:** retry; if it persists, `tw logs <workload>` and check the control plane.

### image_build_failed
1 · cli. **When:** `tw deploy` could not build or push an app's Dockerfile container image with `docker buildx`. **Fix:** the build output is in the detail; check `docker buildx version` works on the deploying machine.

### immutable_field
400 · mgmt. **When:** a PATCH touched a field the caller may not change (IDs, tailnet, creation time). **Fix:** send only the mutable fields (`tw tenant set`, `PATCH /v1/objects/{id}`).

### internal
500 · mgmt, cli. **When:** an unexpected error with no better code; the CLI wraps any untyped error this way. **Fix:** the detail is redacted but usually names the cause; `tw logs --component mgmt|hostd`; report it with the `trace_id`.

### internet_egress_unavailable
403 · netd. **When:** a Worker (or Node app) called a name that is not a tailnet name, such as `https://api.github.com/`. A tailnet name is a bare short name, a name under a tailnet's MagicDNS suffix, or any `*.ts.net` name; anything else would be internet egress, and Workers have none. There is no setting that turns it on; names are never resolved via host DNS. **Fix:** put a gateway for the API on your tailnet and call that ([recipes.md](recipes.md#call-an-internet-api)). Containers are the exception: one started with `enableInternet: true` may reach the hosts in `tailnet.egress.internet` ([containers.md](containers.md)). A tailnet name that isn't in the netmap is [`peer_not_visible`](#peer_not_visible) instead.

### keys_in_guest
1 · cli. **When:** `tw host vmcheck --keyscan` found key material inside a microVM guest. **Fix:** this must never happen (keys stay in hostd); report it with the output.

### killed
410 · netd, mgmt, cli. **When:** the object was killed: in-flight flows were RST, the node closed, and its tombstone answers new requests for the tombstone window; `tw kill` of an object that is already killed. **Fix:** a killed object does not come back; a new request to the same name creates a fresh object with a new node.

### lease_held
409 · mgmt. **When:** another host holds the namespace lease a host tried to take. **Fix:** nothing; placement picks the lease holder. If the holder is dead, `tw host rm <id> --drain`.

### lease_lost
409 · mgmt, hostd, netd (SDK). **When:** the host's lease epoch was fenced: the store knows a newer holder (after a partition or a restart race). **Fix:** hostd re-acquires on its own; persistent `lease_lost` means two hosts with the same host ID.

### login_timeout
408 · cli. **When:** a tailnet node tailworks joins interactively (`tw link --tsnet`) printed a login URL and nobody completed the browser login within 5 minutes. **Fix:** run the command again and finish the login, or pass an auth key file for an unattended join.

### mgmt_failed
1 · cli. **When:** `tw up` (single process): the embedded tw-mgmt failed to start. Also the supervisor's exit error (in `supervisor.log`) when mgmt stopped after start: its listener failed (single process) or tw-mgmt kept exiting (two processes); it exits 1 so the service manager restarts it. **Fix:** the detail has the cause; `tw logs --component mgmt` and `tw doctor`, then `tw up` again (under `tw up --service` it restarts on its own).

### mgmt_not_found
404 · cli. **When:** `tw link` with no endpoint found no `tw-mgmt` through MagicDNS. **Fix:** `tw link tw-mgmt.<tailnet>.ts.net`.

### mgmt_unavailable
503 · cli. **When:** tw-mgmt did not become healthy after `tw up`. **Fix:** `tw logs --component mgmt`.

### mgmt_unreachable
503 · cli. **When:** a linked developer machine cannot reach mgmt's node. **Fix:** is your Tailscale connected to the tailnet that runs tailworks, and does a grant let you reach `tag:tw-mgmt` on tcp:443/80?

### mint_mode_removed
400 · mgmt, cli. **When:** a Tailscale API key (`--api-key-file` without `--control=headscale`) or workload identity federation was asked for, or a state dir or tenant an older tailworks set up in the `apikey` or `wif` mint mode tried to mint. Both modes were removed: tailworks mints with an OAuth client. **Fix:** create an OAuth client (INSTALL.md), then `tw up --oauth-client-file <file>` (keeps the tenant and its deployments), or `tw tenant set <tenant> --oauth-client-file <file>` for a tenant added with `tw tenant add`.

### mint_mode_required
400 · cli. **When:** `tw up` was given no credential. **Fix:** `tw up --oauth-client-file <file>` with an OAuth client of your tailnet (the error prints how to create one; INSTALL.md); on Headscale, `--control=headscale … --api-key-file <file>`.

### misdirected_request
421 · mgmt. **When:** a request on mgmt's tailnet listeners (`:80`, `:443`, the `-tsnet-service` VIP, the operator console) whose `Host` header is neither one of mgmt's own names (its MagicDNS FQDN or short name, `-tsnet-hostname`, `-tsnet-service`) nor an IP literal. This is the DNS-rebinding guard: a web page on a domain rebound to mgmt's IP sends its own domain as `Host` (docs/ADMIN-UI.md). Refused before WhoIs, for every method. **Fix:** address mgmt by its tailnet name (`http(s)://tw-mgmt.<tailnet>/`, `tw whoami` prints it) or its tailnet IP; not through a CNAME or another DNS name that points at it. If you renamed the mgmt node, restart tw-mgmt so it learns the new name.

### missing_tools
1 · cli. **When:** `tw host setup --microvm` needs `ip`, `cryptsetup`, `losetup`, `mkfs.ext4` and one is missing. **Fix:** `apt-get install iproute2 cryptsetup-bin util-linux e2fsprogs` (Debian/Ubuntu) or `dnf install iproute cryptsetup util-linux e2fsprogs` (Fedora).

### name_prefix_required
409 · mgmt, cli. **When:** a tenant was created on a tailnet that already has a tenant, without a `namePrefix` that differs from every other tenant's there (and doesn't start with another's followed by a hyphen), or with a prefix an unprefixed tenant's existing workload names already use. **Fix:** `tw tenant add <tailnet> --owner-tag <tag> --name-prefix <p>` with an unused prefix ([teams.md](teams.md)).

### namespace_ambiguous
409 · mgmt. **When:** a class is declared by several workloads and the request did not say which. **Fix:** pass `workload` (or rename the class).

### namespace_conflict
409 · hostd, netd (SDK). **When:** mgmt returned another identity's object for this workload's class + object name: an invariant violation, so tailworks fails closed rather than share a node. **Fix:** report it; `tw ls --all` shows the two objects.

### needs_root
1 · cli. **When:** `tw host setup` changes the system and was not run as root. **Fix:** `sudo ~/.tailworks/bin/tw host setup --microvm` (sudo's PATH lacks `~/.tailworks/bin`).

### no_host
503 · mgmt. **When:** a deployment has no live host to run on (none registered, none in the tenant's pool, or all stale). **Fix:** `tw status` on the host; `tw host ls`.

### no_identity
401 · netd, mgmt, hostd. **When:** WhoIs failed for the connection: the request carries no verified tailnet caller (a non-tailnet source, or the egress proxy's credentials are stale). **Fix:** call over the tailnet; from a developer machine use `tw link`.

### no_kvm
1 · cli. **When:** `/dev/kvm` does not exist, so this machine cannot run microVMs. **Fix:** bare metal or a VM with nested virtualization; the process tier works without it.

### no_logs
404 · cli. **When:** `tw logs` found no log file for that component or workload. **Fix:** `tw ls`; logs live on the host that runs `tw up`.

### no_rollback_target
409 · mgmt, cli. **When:** `tw rollback <workload>` found no earlier deployment that ran (the host reported it `active` and it did not fail) with a bundle different from the current one; or the deployment named is the current one, or was deployed without a bundle. **Fix:** `tw deployments <workload>`, then `tw rollback <workload> <deployment>` to pick one.

### no_systemd
1 · cli. **When:** `tw host setup --microvm` needs systemd for the helper service. **Fix:** a systemd distro, or skip the microVM tier.

### no_tenant
404 · cli. **When:** the linked mgmt has no tenant yet. **Fix:** finish `tw up` on the host.

### node_required
1 · cli. **When:** building a `"runtime": "node"` app needs Node.js on this machine. **Fix:** install Node.js ≥ 24 (and run `npm install` in the app), or deploy from a machine that has it.

### not_admin
403 · mgmt, cli. **When:** a tailnet caller without the `github.com/rosskukulinski/tailworks/cap/admin` capability called an admin endpoint (`tw link`), or `/mcp` without `github.com/rosskukulinski/tailworks/cap/admin` or `github.com/rosskukulinski/tailworks/cap/mcp`. **Fix:** add the printed grant (`snippet`) from your user/group/tag to `tag:tw-mgmt`.

### not_found
404 · mgmt. **When:** no such record (deployment, host, object, volume). **Fix:** check the ID (`tw ls --all`, `tw host ls`).

### not_installed
1 · cli. **When:** workerd, or create-tailworks (bundler and templates), is not where tailworks expects it. **Fix:** install from a release tarball (`install.sh`); in a checkout `make deps`, or `--workerd <path>` / `$TW_WORKERD`, `$TW_CREATE_TAILWORKS`.

### not_ready
409 · mgmt. **When:** a node certificate was requested before the object is `ready`. **Fix:** wait for `identity_pending` to clear; hostd retries.

### not_running
409 · cli. **When:** tailworks is set up in this state dir but its supervisor is not running. **Fix:** `tw up` (or `systemctl --user start tw`).

### not_supported
501 · cli, hostd. **When:** a command or call tailworks doesn't build yet: `tw r2 object get|put` (object bytes live in the hosts' R2 backend, which mgmt can't reach), container preview URLs. **Fix:** go through the workload (its `env.BUCKET` binding); see [COMPAT.md](COMPAT.md).

### not_up
409 · cli. **When:** this state dir has no tailworks config or tenant yet. **Fix:** `tw up …` on the host, or `tw link <mgmt>` on a developer machine.

### oauth_exchange_failed
401 · cli. **When:** Tailscale refused the OAuth client in `--oauth-client-file` (wrong secret, revoked client). **Fix:** generate a new client (Settings › OAuth clients), overwrite the file (0600), `tw up`.

### object_killed
410 · netd. **When:** a new request reached a killed object: its node's tombstone answered. **Fix:** as `killed`.

### object_quota_exceeded
429 · mgmt. **When:** the tenant's `maxObjects` is reached. **Fix:** `tw tenant set <tenant> --max-objects N`, or kill idle objects.

### peer_not_visible
403 · netd. **When:** the destination is a tailnet name or address that is not in this node's netmap: a grant is missing, or the name does not exist (also how one tenant's objects see another tenant's nodes). An internet name gets [`internet_egress_unavailable`](#internet_egress_unavailable) instead. **Fix:** add a grant from the object's tag to the destination's tag (`tw deploy` prints it), or check the name.

### plan_feature_missing
501 · netd (SDK). **When:** the tailnet's plan or control server lacks a feature tailworks would use; also `ctx.tailnet.connect()` (raw TCP egress), which is not implemented yet. **Fix:** use `fetch()`; see STATUS.md for what is built.

### policy_apply_failed
500 · cli. **When:** `--policy-apply` could not write the policy snippet (validation failed, or the ETag changed). **Fix:** add the printed snippet in the admin console.

### policy_missing_tag_owners
403 · mgmt, cli. **When:** the control plane refused to mint a key for tailworks' own tags: `tag:tw` does not own `tag:tw-mgmt` / `tag:tw-host`. Nothing was minted. **Fix:** add the printed `tagOwners` snippet and re-run `tw up`.

### policy_unavailable
503 · mgmt. **When:** mgmt cannot read the tenant policy: the OAuth client has no `policy_file:read`, so spawn checks and deploy preflight cannot run. **Fix:** give the client Policy File › Read; until then `tw deploy` says `preflight skipped`.

### precondition_failed
412 · mgmt. **When:** `If-Match` did not match the record's current `ETag`. **Fix:** re-read, then retry with the new ETag.

### preview_conflict
409 · mgmt, cli. **When:** `tw deploy --preview` would run the preview as `<workload>--preview-<alias>`, and a workload of that name exists that is not a preview of this workload. **Fix:** pick another `--preview-alias`, or undeploy that workload.

### preview_failed
409 · mgmt, cli. **When:** `tw promote` named a preview its host reported `failed`. **Fix:** `tw logs <workload>--preview-<alias>`, fix the app, and deploy a new preview.

### preview_unsupported
400 · mgmt, cli. **When:** `tw deploy --preview` of a bundle that binds queues (queues are shared by the whole tenant, so a preview would send to and consume the workload's messages), or of a workload that is itself a preview or a replica. **Fix:** deploy it as a workload of its own (`--name`); preview the workload itself, not its preview or replica.

### profile_unsupported
400 · mgmt, cli. **When:** the tenant's control-plane profile can't serve what was asked. On the Headscale profile (ADR-015): `tailnet.service` (Headscale has no Tailscale Services), public routes or `publicIngress: allow` (no Funnel), or an OAuth client (`--oauth-client-file`: Headscale mints with its API key). **Fix:** remove `tailnet.service` and reach the workload at its own workload node's MagicDNS name; serve public traffic some other way. See [docs/headscale.md](headscale.md).

### public_name_in_use
409 · mgmt. **When:** a release of a public name (`DELETE /v1/tenants/{id}/public-names/{name}`) while the workload still serves it: its latest deployment declares `"public"` routes, or its public node is live. **Fix:** `tw undeploy <workload>`, or remove `"public"` from its `tailworks.jsonc` and redeploy, then release the name ([public-ingress](public-ingress.md#name-ownership)).

### public_name_owned
409 · mgmt, cli. **When:** a deploy (or `--dry-run`) declares `"public"` routes under a workload name that another tenant on the same tailnet owns as its public name. The first tenant to deploy public routes under a name keeps `https://<name>.<tailnet>.ts.net/` across undeploys, until it releases the name, so its public traffic (webhooks, OAuth callbacks) can't move to someone else. **Fix:** deploy under another workload name, or ask the operator to have the owning tenant release the name ([public-ingress](public-ingress.md#name-ownership)).

### public_unreachable
1 · cli. **When:** `tw deploy --wait-public` waited for the workload's public URL and it didn't answer from the public internet in time. The deploy itself succeeded. **Fix:** a new public name's certificate and DNS can take minutes; retry with a longer `--wait-public`, check the tailnet has HTTPS certificates on, and see [docs/public-ingress.md](public-ingress.md).

### push_unauthorized
403 · hostd. **When:** something other than mgmt's node pushed to hostd's push listener (port 7474). **Fix:** only `tag:tw-mgmt` may push; check the grant and who is on that tag.

### r2_backend_unavailable
503 · hostd. **When:** the host's R2 backend (its `-r2-backend` directory or S3-compatible service) failed a read, write or delete of an R2 object's bytes. In app code it surfaces as an R2 error (code 10001) whose message starts `tw r2: r2_backend_unavailable:`. **Fix:** the host's operator checks the backend (`tw logs --component hostd`): disk space, the S3 endpoint, its credentials. Retry the call.

### r2_blob_missing
500 · hostd. **When:** a bucket's index names an object whose bytes are not in this host's R2 backend: the workload moved from another host and the backend is `fs` (bytes stay on the old host's disk), or the backend lost them. Surfaces as an R2 error (code 10001) whose message starts `tw r2: r2_blob_missing:`. **Fix:** run hosts that share workloads on one S3-compatible backend (`-r2-backend s3`, [r2.md](r2.md)); write the object again.

### r2_too_large
400 · hostd. **When:** an R2 object or part over 5 GiB − 5 MiB. Surfaces as R2's "exceeds the maximum allowed object size" (code 100100). **Fix:** use a multipart upload, parts up to that size each.

### release_bad_signature
1 · cli. **When:** `tw upgrade` on a `tw up` machine refused a release because its `SHA256SUMS.sig` doesn't verify: it is signed by a key that isn't pinned in this `tw` (or in `TW_RELEASE_SIGNERS`), a file changed after signing, a file isn't in the signed `SHA256SUMS`, or this `tw` build pins no release key. Nothing in the release ran. **Fix:** download the release again from GitHub (`tw upgrade [<version>]`); check it by hand as in [INSTALL.md](INSTALL.md), "Verify a release". For a build you made yourself, add `--allow-unsigned`.

### release_unsigned
1 · cli. **When:** `tw upgrade` on a `tw up` machine refused a release (a download or `--from`) that has no `SHA256SUMS.sig` or no `SHA256SUMS` inside it. Nothing in the release ran. **Fix:** upgrade to a published release (`tw upgrade [<version>]`). For a build you made yourself (`make release`), add `--allow-unsigned` (or set `TW_ALLOW_UNSIGNED=1`).

### remote_unsupported
400 · cli. **When:** a command that runs only on the host that runs `tw up` (`tw logs`, `tw tenant add`) was run on a linked developer machine. **Fix:** run it on the host.

### queue_limit
400 · mgmt, hostd. **When:** a queue send is too big: a message over 128 KB, a `sendBatch` over 100 messages or 256 KB, a `delaySeconds` over 43200, or a queue over 1 GiB of kept messages. In a Worker, `send()` throws `Error: [queue_limit] … (400)`. **Fix:** send smaller messages (store large payloads elsewhere and send a reference), or let consumers drain the queue.

### queue_unavailable
503 · hostd. **When:** a queue producer's send (or `metrics()`) couldn't reach mgmt through hostd. `send()` throws `Error: [queue_unavailable] … (503)`; nothing was enqueued. **Fix:** retry; check `tw status` for mgmt and the host.

### reserved_tag
400 · mgmt. **When:** a bundle or namespace asks for one of tailworks' own tags (`tag:tw`, `tag:tw-mgmt`, `tag:tw-host`). **Fix:** use your app's tags (`tag:tw-wl-<name>`, class tags) in `tailworks.jsonc`.

### sandbox_unavailable
409 · hostd, cli. **When:** a second tenant's process-tier workload on a host that must not be shared: no process sandbox (Linux without Landlock, or `-sandbox none`), or the tailworks user can reach a container engine socket, is in the docker/podman/lxd/incus group, or can reach the system tailscaled while a workload could use it. **Fix:** run tailworks as a dedicated user without those sockets and groups, enable Landlock, use TCP listen mode for Node apps, deploy with `"tier": "microvm"`; or (tenants NOT isolated) `tw up --insecure-shared-host`.

### service_conflict
409 · cli. **When:** the systemd unit / LaunchAgent already runs tailworks for another state dir. **Fix:** `tw down --service --state-dir <that dir>`, then `tw up --service` again.

### service_failed
500 · cli. **When:** `systemctl --user` or `launchctl` failed while installing, starting or removing the service. **Fix:** the detail has the command and its output; `systemctl --user status tw` / `launchctl print gui/$(id -u)`.

### service_running
409 · cli. **When:** tailworks runs as a service (installed by `tw up --service`) and you ran a plain `tw up`/`tw down` that would fight it. **Fix:** `tw down --service`, then `tw up` again; or manage it with `systemctl --user`.

### service_unsupported
1 · cli. **When:** `--service` needs systemd (Linux, with a reachable user manager) or launchd (macOS) and neither applies. **Fix:** run `tw up` without `--service`, or fix the user session (`loginctl enable-linger`).

### shared_owner_tag
409 · mgmt, cli. **When:** a tenant was created (or its owner tag changed) on a tailnet where another tenant uses `tag:tw` as its owner tag, or the same owner tag: either tenant's OAuth client could mint the other's tags. **Fix:** give each tenant on the tailnet its own owner tag: `tw tenant set <existing> --owner-tag tag:tw-t-<team> --oauth-client-file <a client scoped to it>`, then `tw tenant add … --owner-tag …` ([teams.md](teams.md)).

### spawn_budget_exceeded
429 · mgmt, netd (SDK). **When:** too many live descendants (`maxLive`) or too deep a chain (`maxDepth`) under `tailnet.spawn` in `tailworks.jsonc`. **Fix:** kill finished sub-agents (`ttl` helps), or raise the budget.

### state_conflict
409 · cli. **When:** the state dir is bound to another mint mode, tailnet, `--local-dev` setting or mgmt hostname than this command asks for; the API key file already serves another tenant; mgmt already has this host ID bound to another node; `tw link` on a machine that runs tailworks itself. **Fix:** the detail names the binding; use another `--state-dir`, or `tw down --purge` and start over.

### stop_timeout
500 · cli. **When:** `tw down` waited 3 min for the supervisor to stop and killed it. **Fix:** `tw status`; `tw logs --component supervisor`.

### store_conflict
409 · mgmt. **When:** a platform store journal append named a head (`after`) or incarnation that is no longer current: another writer (a replica, an admin) appended first, or the store was deleted. **Fix:** nothing for app code: the store object catches up and retries on its own. A client of the REST API reads the journal from its `after` and retries.

### store_degraded
409 · hostd, mgmt. **When:** a platform store's object couldn't apply a journal entry while catching up (a D1 statement that fails on replay, which only non-deterministic SQL can do; an entry written by a newer tailworks; a corrupt entry). It stopped there instead of skipping the entry: it serves reads from what it applied before it, refuses writes, and tries the entry again on later syncs. A Worker's D1 call fails with `D1_ERROR: [store_degraded] …` naming the entry and SQLite's error. **Fix:** an entry from a newer tailworks: upgrade tw-hostd. Otherwise, for D1, `tw d1 export <db> --output backup.sql` (it exports what applied, headed by a comment naming the failing entry), check it, then `tw d1 delete <db>`, `tw d1 create <db>` and `tw d1 execute <db> --file backup.sql` ([d1.md](d1.md), "Recovery").

### store_limit
400 · mgmt, hostd. **When:** a platform store write is too big: an entry over 2 MiB, an append over 4 MiB or 128 entries, a compaction key over 1 KiB, or a store over 1 GiB. **Fix:** write less at once, or delete data you no longer need.

### store_unavailable
503 · hostd, mgmt. **When:** a platform store couldn't reach mgmt's journal (through hostd). Writes fail; reads still answer from the host's copy if the store has caught up with the journal on this host before, and fail if it never has (a new host). From the mgmt API it also means its database is unreachable. **Fix:** retry; check `tw status` for mgmt and the host.

### superseded
409 · mgmt. **When:** undeploy of a deployment that is not the latest for its workload. **Fix:** `tw undeploy <workload>` acts on the latest.

### supervisor_failed
500 · cli. **When:** the supervisor exited at start, or the systemd unit started but no supervisor came up. **Fix:** `tw logs --component supervisor`, then `tw up`.

### systemd_failed
500 · cli. **When:** `systemctl` failed during `tw host setup`. **Fix:** `journalctl -u tw-vmhelper`.

### tag_not_owned
400 · mgmt, cli. **When:** a tenant with its own owner tag deployed or spawned with a tag that, in the tailnet policy's `tagOwners`, its owner tag doesn't own, or another tenant's owner tag on the same tailnet also owns; `tw policy` lists them as `tagOwnerProblems`. **Fix:** own the tag by the tenant's owner tag only (the printed `snippet`), or use a tag of your own team's ([teams.md](teams.md)).

### tailnet_required
400 · cli. **When:** the tailnet could not be determined from the OAuth client, or the Headscale profile was used without `--tailnet` (its `dns.base_domain`). Tailworks never bootstraps from the machine's own Tailscale login. **Fix:** `tw up --tailnet <name> …`.

### tenant_forbidden
403 · mgmt, cli. **When:** a tenant admin (`github.com/rosskukulinski/tailworks/cap/tenant-admin`) asked for a tenant or action that no single grant value names, called an admin-only route (hosts, tenant settings, releases, upgrades, status), or holds the capability but every value is invalid (missing `tenants` or `actions`, a wildcard, an unknown action or field). **Fix:** add the printed grant (`snippet`) to the tailnet policy, or ask an admin; `tw whoami` lists what your values allow and why any grant nothing ([teams.md](teams.md)).

### tenant_has_live_objects
409 · mgmt, cli. **When:** `tw tenant rm` of a tenant with live objects. **Fix:** `tw tenant rm <tenant> --force` (kills and purges first).

### types_stale
1 · cli. **When:** `tw types --check` found the app's `worker-configuration.d.ts` (or `--out`) missing, or different from what `tw types` writes for the current wrangler config, tailworks.jsonc and secret names. **Fix:** run `tw types` and commit the file.

### unhealthy
503 · cli. **When:** `tw status` finds tailworks not healthy: mgmt not answering, or no live host. **Fix:** `tw logs --component mgmt|hostd`.

### unknown_namespace
404 · mgmt. **When:** `(workload, class)` is not declared in the workload's `tailworks.jsonc`. **Fix:** declare the class under `tailnet.objects`, redeploy.

### unknown_preview
404 · mgmt, cli. **When:** `tw promote <workload> <preview>` named a preview the workload doesn't have, or one that is stopped (a promotion undeploys its preview). **Fix:** `tw previews <workload>` lists the running ones.

### unknown_object
404 · netd, hostd, cli. **When:** no such object for this ID or name (or not in the tenant you named). **Fix:** `tw ls --all --tenant <tailnet>`.

### unknown_store
404 · mgmt. **When:** the workload has no platform store of that kind and name (`tw kv namespace`, `GET …/stores/{kind}/{name}`). A store exists from its first write. **Fix:** check the name, or create it.

### unknown_queue
404 · mgmt. **When:** `tw queues info|rm|purge|messages` (or the API) named a queue the tenant doesn't have. A send or a consumer's first claim creates the queue, so this only comes from operator calls. **Fix:** `tw queues ls` for the names.

### unknown_tenant
404 · mgmt, cli. **When:** no tenant with that tailnet or ID. **Fix:** `tw tenant ls`.

### unknown_workload
404 · cli, mgmt. **When:** `tw logs`, `tw undeploy` or `tw host vmcheck` named a workload with no deployment; `tw d1 execute|export|migrations` named a workload that isn't running (a D1 database is reached through the running deployment that binds it). **Fix:** `tw ls`; `tw deploy`.

### unsupported
1 · cli. **When:** `tw host setup` on an OS without the microVM tier (it needs Linux with KVM). **Fix:** nothing to set up; every workload runs in the process tier here.

### update_check_failed
1 · cli. **When:** `tw version --check` could not find the latest release: GitHub was unreachable, answered with an error (rate limit), or the repository has no published release. **Fix:** retry later, or look at the releases page; `TW_REPO` names another repository, as for `get.sh`.

### upgrade_blocked
409 · mgmt, cli. **When:** `tw upgrade` can't start a fleet upgrade run because its plan has blockers: a release missing from the catalog, a host's tarball not imported, parts of the fleet more than one release line behind, or an intermediate release with contract migrations. **Fix:** `tw upgrade --plan` prints each blocker and its fix ([upgrading.md](upgrading.md)).

### upgrade_state
409 · mgmt, cli. **When:** `tw upgrade pause|resume|abort` on a run that isn't running (pause), paused (resume) or active (abort). **Fix:** `tw upgrade status` shows the run's state.

### upstream_unavailable
503 · netd. **When:** the platform upstream (workerd or node behind netd) is not answering. **Fix:** it is restarting or crashed; `tw logs <workload>`.

### usermod_failed
1 · cli. **When:** `tw host setup --microvm` could not add the user to the `kvm` group. **Fix:** `sudo usermod -aG kvm <user>` and log in again.

### version_skew
409 · mgmt, cli. **When:** two tailworks components are outside the supported version window: a host newer than tw-mgmt, or more than one release line behind it; a CLI more than one line behind mgmt (in `--json` mode); mgmt replicas more than one line apart; or a tw-mgmt older than the store's minimum (a newer tw-mgmt ran a contract migration). **Fix:** upgrade in order, mgmt first, then hosts ([upgrading.md](upgrading.md)); a store contracted by a newer tw-mgmt needs that version or newer, or the pre-upgrade backup restored.

### wrangler_required
1 · cli. **When:** `tw deploy --wrangler` / `tw dev --wrangler` found no wrangler CLI (`$TW_WRANGLER`, the project's `node_modules/.bin/wrangler`, or `wrangler` on PATH). **Fix:** `npm install --save-dev wrangler`, or drop `--wrangler` to bundle with tailworks' built-in esbuild ([wrangler.md](wrangler.md)).
