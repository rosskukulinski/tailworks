# twd error catalog

Every refusal in twd is typed. The same code appears wherever the error
surfaces:

- **From `fetch()` on the tailnet** (netd ingress, egress, hostd): an HTTP
  status, a `twd-error: <code>` header and a JSON body
  `{code, message|detail, missing?, snippet?, doc_url, trace_id?}`.
- **From the SDK** (`@twd/tailnet`, `@twd/mcp`, `@twd/node`): a thrown
  `TailnetError` with `.code`, `.status`, `.missing`, `.snippet`, `.docUrl`.
- **From the mgmt REST API** (`/v1`, `docs/contracts/mgmt-api.md`): the same
  body; `trace_id` correlates with `twd audit --trace <id>`.
- **From the `twd` CLI**: `error [code]: message`, detail lines, `fix:` and a
  link to this page. With `--json`, one line on stderr:
  `{"error": {code, message, hint, doc_url, detail?, fix?, trace_id?}}`
  ([cli-json.md](cli-json.md)); `twd docs show errors.md#<code>` prints an
  entry offline.

The `doc_url` / link is `https://github.com/rosskukulinski/twd/blob/master/docs/errors.md#<code>`,
so every code below is a heading. Codes are alphabetical; the source column
says who emits it (netd = data plane and SDK, mgmt = REST API, hostd = host
agent, cli = `twd`). Detail text is redacted of key material before it is
printed anywhere.

### acl_denied
403 · netd, mgmt, cli. **When:** the peer is visible in the netmap but the port or service is not granted; from `twd deploy`, the preflight found grants or tagOwners your app's `tailnet.connects` needs missing. **Fix:** add the printed grant (`snippet`) to the tailnet policy, or deploy with `--allow-missing-grants`.

### already_joined
409 · mgmt. **When:** a credential (auth key) was requested for an object that already has a node. **Fix:** nothing to do; hostd retries by rejoining. Report it if it repeats.

### already_running
409 · cli. **When:** `twd up` finds a supervisor already running from this state dir. **Fix:** `twd status`; `twd down` first if you meant to restart.

### already_stopped
409 · cli. **When:** `twd undeploy <workload>` of a workload whose latest deployment is already undeployed. **Fix:** `twd ls --all` to see what is live.

### ambiguous_object
400 · cli. **When:** the object reference you gave (`twd kill`, `twd audit`) matches more than one object. **Fix:** name it by object ID (`twd ls` prints them).

### approval_not_pending
409 · hostd, mgmt, cli. **When:** a decision (`twd approve`, the admin UI, `POST /_twd/approvals/<workflow>/<instance>`) names a Workflow instance that is not waiting for an approval: already approved or rejected, timed out, or it never asked. **Fix:** `twd approvals [workload] --all` shows what is waiting and what was decided ([approvals](approvals.md)).

### approval_requires_identity
Thrown in the Worker · platform. **When:** app code sent an event of type `approval` or `approval-*` to a Workflow instance (for example the Agents SDK's `approveWorkflow()`). On twd an approval is a decision by a tailnet identity, so only twd delivers it. **Fix:** approve with `twd approve <workload> <instance>`, the admin UI, or a same-origin `POST /_twd/approvals/<workflow>/<instance>` from the app's page on the workload's node ([approvals](approvals.md)).

### audit_unverified
500 · cli. **When:** `twd audit` found an audit chain whose hashes or host signatures do not verify (tampering, or a store problem). **Fix:** treat the trail as suspect; `twd logs --component mgmt` and report it.

### bad_bundle
400 · mgmt. **When:** a deploy bundle has no `twd.jsonc`, or neither `tailnet.tags` nor `tailnet.objects`. **Fix:** add `"tailnet": { "tags": ["tag:twd-wl-<name>"] }` to `twd.jsonc` (`twd init` templates show the shape).

### bad_cursor
400 · mgmt. **When:** a `cursor` query parameter does not decode. **Fix:** pass `nextCursor` from the previous page unchanged, with the same filters.

### bad_request
400 · netd, mgmt, hostd, cli. **When:** a malformed body, parameter, filter or command line (the CLI uses it for every usage error: missing argument, unknown template, unreadable key file, bad `twd.jsonc`). **Fix:** the detail says what was wrong; `twd <command> --help` lists the flags.

### bootstrap_failed
502 · netd, mgmt, cli. **When:** an object's node could not be created: the control plane refused the mint (tag not owned, key invalid), the token exchange failed, or the node never joined. From `twd up`, mgmt's or the host's own node failed to join. **Fix:** check the tagOwners snippet (`policy_missing_tag_owners`), the credential (`twd doctor`), then `twd logs --component mgmt`.

### build_failed
500 · cli. **When:** esbuild (workerd apps) or `create-twd build-node` (Node apps) could not bundle the app. **Fix:** the compiler output is in the detail; fix the source and re-run.

### cancelled
0 · cli. **When:** you answered no at a `twd up`, `twd agent setup` or `twd agent remove` confirmation prompt. **Fix:** nothing was started or written; re-run with the policy in place, or `--yes` to skip the prompt.

### cap_limit_exceeded
403 · netd (SDK `@twd/mcp`). **When:** the caller holds the tool but a numeric limit or budget in its grant is exceeded (e.g. `maxUsd`, `perDayUsd`). **Fix:** raise the limit in the `app` capability of the grant, or wait for the daily budget to reset.

### cap_missing
403 · netd (SDK `@twd/mcp`), mgmt (`/mcp`, approvals), hostd, cli. **When:** the caller's app capability lacks the tool (or the capability itself) the MCP server requires. From twd's own MCP server: a `deploy`, `kill` or `undeploy` call without a `twd.dev/cap/mcp` grant naming the tool, or, on the twd host, a `twd mcp` bridge started without `--allow <tool>`. **Fix:** add the `app` capability with the tool to the grant from the caller's tag to the server's tag (the error's `snippet` has it); for twd's MCP server, ask the tailnet admin, don't work around it. For approvals: the approver's `twd.dev/cap/approve` grants do not cover the workload, Workflow, step, decision or a param limit (`missing` says which); grant it to the approver on the workload's tag (the app's page, `/_twd/approvals`) and on `tag:twd-mgmt` (`twd approve`, the admin UI) ([approvals](approvals.md)).

### confirmation_required
1 · cli. **When:** `twd agent setup` or `twd agent remove` would write or delete files, and there is no terminal to ask on (a coding agent, a script, or `--json`). Nothing was written. **Fix:** review the change list with `--dry-run`, then run it in a terminal to answer the prompt, or add `--yes`. An agent should show the user the dry run and let them decide.

### credential_file_insecure
403 · cli. **When:** the OAuth client file given to `twd up --oauth-client-file` is readable by other users. **Fix:** `chmod 600 <file>` then `twd up` again.

### d1_error
400 · mgmt, cli. **When:** a statement of `twd d1 execute`, `twd d1 migrations apply` or `POST …/d1/{name}/query` failed: SQLite's error (syntax, constraint, no such table) or a D1 limit (statement over 100 KB, over 100 parameters, a value over 2 MB, a result over 16 MiB, one write over 2 MiB encoded). Nothing of the batch was applied (a failing migration isn't recorded). In a Worker the same failures throw `D1_ERROR: …`, as on Cloudflare. **Fix:** fix the SQL; split a large import into several files or batches.

### delegation_exceeds_parent
403 · mgmt, netd. **When:** `ctx.tailnet.spawn()` asked for capabilities, tags or destinations the parent object does not have (`missing: {src, dst, cap, field}`). **Fix:** spawn with a subset of the parent's grants, or widen the parent's grant in the policy.

### delegation_invalid
403 · mgmt, hostd, netd. **When:** a spawn names a child tag not owned by the parent's tags, the parent is unknown (404) or dead (410), or an object tried to kill something that is not itself or a descendant. **Fix:** fix the `tagOwners` so the parent's tag owns the child's; kill through an ancestor or the CLI.

### deploy_crashloop
500 · cli. **When:** `twd deploy` was waiting for the deployment and its workerd, after it had answered its health check once, kept exiting: the deployment is `crashloop`. The host keeps restarting it with exponential backoff (200 ms doubling to 5 min) and never gives up; it reports `active` again once a restart answers. The detail lines are the attempt count and workerd's last log lines. **Fix:** `twd logs <workload>`; fix the Worker and deploy again. See [observability.md](observability.md#crash-loops).

### deploy_failed
500 · cli. **When:** the host could not start the workload (bad bundle or config, or workerd or node exited before it ever answered its health check); the host does not retry it. `twd dev` prints workerd's output. **Fix:** read the detail lines; `twd logs <workload>` on the host.

### derp_unreachable
502 · netd (SDK). **When:** no direct path to the peer and no reachable DERP relay. **Fix:** check the host's outbound connectivity (UDP 3478, TCP 443 to DERP).

### device_delete_unavailable
403 · mgmt, cli. **When:** a kill held (node closed, tombstoned) but the device could not be deleted: the tenant's OAuth client lacks the `devices:core` scope, or the WIF lifecycle credential is missing. **Fix:** generate an OAuth client with Auth Keys (write) and Devices › Core (write), overwrite the file, `twd up`; remove leftover devices in the admin console.

### dial_failed
502 · netd. **When:** the tailnet dial to a visible peer failed (connection refused, timeout). **Fix:** is the destination up and listening on that port? Try again; check `twd logs`.

### doctor_failed
1 · cli. **When:** `twd doctor` had at least one failing check. **Fix:** each failing line prints its own `fix:`.

### egress_unenforced
409 · hostd. **When:** a `"runtime": "node"` deployment on a host with no process sandbox that already serves other tenants: the Node tier's egress proxy is cooperative there, which would be a cross-tenant SSRF vector. **Fix:** use the workerd runtime, a single-tenant host, Linux with Landlock, or (tenants not isolated) `twd up --insecure-shared-host`.

### exists
409 · mgmt, cli. **When:** create of an ID that already exists (`twd host add <id>` for a known host). **Fix:** `twd host rm <id> --drain`, then add again; or pick another ID.

### feature_unavailable
409 · mgmt, cli. **When:** the deployment needs a host feature (`kv`, `r2`, `d1`, `telemetry`, …) that no eligible host advertises, or the workload's current host runs a twd too old to offer it. Hosts report their features when they register ([upgrading.md](upgrading.md)). **Fix:** upgrade the hosts (`twd upgrade --plan` lists them), or redeploy with `--replace` to move off the old host.

### forbidden
403 · mgmt, hostd. **When:** the caller's role is fine but the record is not its own: a host reading another host's deployment or lease, a request to a workerd instance from the wrong peer; a host node asking for the admin UI (`/ui/`); or, with the message `cross-site request refused`, a browser-originated mutation (it carried `Origin` or `Sec-Fetch-Mode`) whose `Sec-Fetch-Site` was not `same-origin`/`none`, whose `Origin` was not mgmt's own host, or which hit `/v1` without the `X-Twd-UI: 1` header (docs/ADMIN-UI.md). **Fix:** usually a bug or stale state; `twd status` and report it. For the cross-site refusal, use the admin UI from mgmt's own URL (`http(s)://twd-mgmt.<tailnet>/ui/`), never through another origin; CLI and hostd clients send none of those headers and are never affected.

### forbidden_destination
403 · netd. **When:** egress to a destination class netd refuses: loopback, link-local, multicast, cloud metadata, the object itself, a sibling object on the same host, the host, or a non-tailnet address (ADR-019). **Fix:** only tailnet peers are reachable from an object; talk to siblings through the tailnet name of another host, or `twd up --allow-sibling-egress` for dev.

### funnel_required
412 · cli. **When:** `twd up --wif` needs mgmt's node to publish its OIDC issuer over Funnel, and `tag:twd-mgmt` has no funnel node attribute. **Fix:** add `"nodeAttrs": [{"target": ["tag:twd-mgmt"], "attr": ["funnel"]}]` and re-run `twd up`; or use an OAuth client (no Funnel).

### helper_failed
500 · cli. **When:** `twd host setup --microvm` installed `twd-vmhelper.service` but it did not come up. **Fix:** `journalctl -u twd-vmhelper`.

### host_failed
1 · cli. **When:** `twd up` (single process): the host agent embedded in twd-mgmt failed to start or to register with mgmt. **Fix:** the detail has the cause; `twd logs --component hostd` and `twd doctor`, then `twd up` again.

### host_holds_leases
409 · mgmt, cli. **When:** `twd host rm <id>` while the host still holds namespace leases. **Fix:** `twd host rm <id> --drain`.

### host_identity_mismatch
403 · mgmt. **When:** a `tag:twd-host` node claims a host ID bound to another node. **Fix:** each host needs its own ID (`twd host add <new-id>`); a replaced machine needs `twd host rm` first.

### host_not_registered
403 · mgmt. **When:** a `tag:twd-host` node calls mgmt before `PUT /v1/hosts/{id}`. **Fix:** start twd-hostd with the join key file from `twd host add`; it registers itself.

### host_token_invalid
403 · mgmt. **When:** a request on the local-dev unix socket carried a wrong `Twd-Host-Token`. **Fix:** `twd up --local-dev` writes the token; restart hostd from the same state dir.

### host_token_required
401 · mgmt. **When:** a request on the local-dev unix socket carried no `Twd-Host-Token`. **Fix:** as above; in normal (tsnet) operation this socket is not used.

### host_token_unconfigured
503 · mgmt. **When:** mgmt in local-dev mode was started without a host token; it fails closed. **Fix:** `twd down`, then `twd up` (or `twd up --local-dev`) again.

### host_unavailable
503 · cli. **When:** twd-hostd is not answering, or did not register with twd-mgmt after `twd up`. **Fix:** `twd logs --component hostd`.

### hostd_unavailable
503 · hostd, netd (SDK). **When:** the host agent could not be reached from the platform wrapper (hostapi socket down). **Fix:** `twd status`; the host is restarting or dead.

### https_required
412 · cli. **When:** `twd up --wif` on a tailnet without HTTPS certificates (needed for the Funnel-published issuer). **Fix:** admin console › DNS › HTTPS Certificates › Enable, then `twd up` again; or use an OAuth client.

### identity_conflict
409 · mgmt. **When:** an object ID collided across identities (two workloads derived the same ID). **Fix:** rename one object or class; report it (IDs are HMAC-derived and this should not happen).

### identity_pending
503 · netd, mgmt, cli. **When:** the object's node is minted but not ready yet (`Retry-After: 1`); `twd dev` waited 60 s for a deploy. **Fix:** retry; if it persists, `twd logs <workload>` and check the control plane.

### image_build_failed
1 · cli. **When:** `twd deploy` could not build or push an app's Dockerfile container image with `docker buildx`. **Fix:** the build output is in the detail; check `docker buildx version` works on the deploying machine.

### immutable_field
400 · mgmt. **When:** a PATCH touched a field the caller may not change (IDs, tailnet, creation time). **Fix:** send only the mutable fields (`twd tenant set`, `PATCH /v1/objects/{id}`).

### internal
500 · mgmt, cli. **When:** an unexpected error with no better code; the CLI wraps any untyped error this way. **Fix:** the detail is redacted but usually names the cause; `twd logs --component mgmt|hostd`; report it with the `trace_id`.

### issuer_unreachable
503 · cli. **When:** `twd up --wif` published mgmt's OIDC issuer over Funnel, but it never answered from the public internet. **Fix:** check the funnel node attribute and `twd logs --component mgmt`; the first certificate for a new node name can take 1–2 minutes.

### keys_in_guest
1 · cli. **When:** `twd host vmcheck --keyscan` found key material inside a microVM guest. **Fix:** this must never happen (keys stay in hostd); report it with the output.

### killed
410 · netd, mgmt, cli. **When:** the object was killed: in-flight flows were RST, the node closed, and its tombstone answers new requests for the tombstone window; `twd kill` of an object that is already killed. **Fix:** a killed object does not come back; a new request to the same name creates a fresh object with a new node.

### lease_held
409 · mgmt. **When:** another host holds the namespace lease a host tried to take. **Fix:** nothing; placement picks the lease holder. If the holder is dead, `twd host rm <id> --drain`.

### lease_lost
409 · mgmt, hostd, netd (SDK). **When:** the host's lease epoch was fenced: the store knows a newer holder (after a partition or a restart race). **Fix:** hostd re-acquires on its own; persistent `lease_lost` means two hosts with the same host ID.

### login_timeout
408 · cli. **When:** `twd up --wif` printed a login URL and nobody completed the browser login within `--login-timeout` (default 5 min). **Fix:** run `twd up` again and finish the login, or `--mgmt-authkey-file` for an unattended join.

### mgmt_failed
1 · cli. **When:** `twd up` (single process): the embedded twd-mgmt failed to start. **Fix:** the detail has the cause; `twd logs --component mgmt` and `twd doctor`, then `twd up` again.

### mgmt_not_found
404 · cli. **When:** `twd link` with no endpoint found no `twd-mgmt` through MagicDNS. **Fix:** `twd link twd-mgmt.<tailnet>.ts.net`.

### mgmt_unavailable
503 · cli. **When:** twd-mgmt did not become healthy after `twd up`. **Fix:** `twd logs --component mgmt`.

### mgmt_unreachable
503 · cli. **When:** a linked developer machine cannot reach mgmt's node. **Fix:** is your Tailscale connected to the tailnet that runs twd, and does a grant let you reach `tag:twd-mgmt` on tcp:443/80?

### mint_mode_required
400 · cli. **When:** `twd up` was given no credential. **Fix:** `--oauth-client-file <file>` (recommended), `--wif`, or `--tailnet <t> --api-key-file <file>`; see INSTALL.md.

### missing_tools
1 · cli. **When:** `twd host setup --microvm` needs `ip`, `cryptsetup`, `losetup`, `mkfs.ext4` and one is missing. **Fix:** `apt-get install iproute2 cryptsetup-bin util-linux e2fsprogs` (Debian/Ubuntu) or `dnf install iproute cryptsetup util-linux e2fsprogs` (Fedora).

### namespace_ambiguous
409 · mgmt. **When:** a class is declared by several workloads and the request did not say which. **Fix:** pass `workload` (or rename the class).

### namespace_conflict
409 · hostd, netd (SDK). **When:** mgmt returned another identity's object for this workload's class + object name: an invariant violation, so twd fails closed rather than share a node. **Fix:** report it; `twd ls --all` shows the two objects.

### needs_root
1 · cli. **When:** `twd host setup` changes the system and was not run as root. **Fix:** `sudo ~/.twd/bin/twd host setup --microvm` (sudo's PATH lacks `~/.twd/bin`).

### no_host
503 · mgmt. **When:** a deployment has no live host to run on (none registered, none in the tenant's pool, or all stale). **Fix:** `twd status` on the host; `twd host ls`.

### no_identity
401 · netd, mgmt, hostd. **When:** WhoIs failed for the connection: the request carries no verified tailnet caller (a non-tailnet source, or the egress proxy's credentials are stale). **Fix:** call over the tailnet; from a developer machine use `twd link`.

### no_kvm
1 · cli. **When:** `/dev/kvm` does not exist, so this machine cannot run microVMs. **Fix:** bare metal or a VM with nested virtualization; the process tier works without it.

### no_logs
404 · cli. **When:** `twd logs` found no log file for that component or workload. **Fix:** `twd ls`; logs live on the host that runs `twd up`.

### no_systemd
1 · cli. **When:** `twd host setup --microvm` needs systemd for the helper service. **Fix:** a systemd distro, or skip the microVM tier.

### no_tenant
404 · cli. **When:** the linked mgmt has no tenant yet. **Fix:** finish `twd up` on the host.

### node_required
1 · cli. **When:** building a `"runtime": "node"` app needs Node.js on this machine. **Fix:** install Node.js ≥ 24 (and run `npm install` in the app), or deploy from a machine that has it.

### not_admin
403 · mgmt, cli. **When:** a tailnet caller without the `twd.dev/cap/admin` capability called an admin endpoint (`twd link`), or `/mcp` without `twd.dev/cap/admin` or `twd.dev/cap/mcp`. **Fix:** add the printed grant (`snippet`) from your user/group/tag to `tag:twd-mgmt`.

### not_found
404 · mgmt. **When:** no such record (deployment, host, object, volume). **Fix:** check the ID (`twd ls --all`, `twd host ls`).

### not_installed
1 · cli. **When:** workerd, or create-twd (bundler and templates), is not where twd expects it. **Fix:** install from a release tarball (`install.sh`); in a checkout `make deps`, or `--workerd <path>` / `$TWD_WORKERD`, `$TWD_CREATE_TWD`.

### not_ready
409 · mgmt. **When:** a node certificate was requested before the object is `ready`. **Fix:** wait for `identity_pending` to clear; hostd retries.

### not_running
409 · cli. **When:** twd is set up in this state dir but its supervisor is not running. **Fix:** `twd up` (or `systemctl --user start twd`).

### not_supported
501 · cli, hostd. **When:** a command or call twd doesn't build yet: `twd r2 object get|put` (object bytes live in the hosts' R2 backend, which mgmt can't reach), container preview URLs. **Fix:** go through the workload (its `env.BUCKET` binding); see [COMPAT.md](COMPAT.md).

### not_up
409 · cli. **When:** this state dir has no twd config or tenant yet. **Fix:** `twd up …` on the host, or `twd link <mgmt>` on a developer machine.

### oauth_exchange_failed
401 · cli. **When:** Tailscale refused the OAuth client in `--oauth-client-file` (wrong secret, revoked client). **Fix:** generate a new client (Settings › OAuth clients), overwrite the file (0600), `twd up`.

### object_killed
410 · netd. **When:** a new request reached a killed object: its node's tombstone answered. **Fix:** as `killed`.

### object_quota_exceeded
429 · mgmt. **When:** the tenant's `maxObjects` is reached. **Fix:** `twd tenant set <tenant> --max-objects N`, or kill idle objects.

### peer_not_visible
403 · netd. **When:** the destination is not in this node's netmap: a grant is missing, or the name does not exist (also how one tenant's objects see another tenant's nodes). **Fix:** add a grant from the object's tag to the destination's tag (`twd deploy` prints it), or check the name.

### plan_feature_missing
501 · netd (SDK). **When:** the tailnet's plan or control server lacks a feature twd would use; also `ctx.tailnet.connect()` (raw TCP egress), which is not implemented yet. **Fix:** use `fetch()`; see STATUS.md for what is built.

### policy_apply_failed
500 · cli. **When:** `--policy-apply` could not write the policy snippet (validation failed, or the ETag changed). **Fix:** add the printed snippet in the admin console.

### policy_missing_tag_owners
403 · mgmt, cli. **When:** the control plane refused to mint a key for twd's own tags: `tag:twd` does not own `tag:twd-mgmt` / `tag:twd-host` (and in `--wif`, your login does not own `tag:twd-mgmt`). Nothing was minted. **Fix:** add the printed `tagOwners` snippet and re-run `twd up`.

### policy_unavailable
503 · mgmt. **When:** mgmt cannot read the tenant policy: no lifecycle credential or OAuth client with `policy_file:read`, so spawn checks and deploy preflight cannot run. **Fix:** give the client Policy File › Read (or add the WIF lifecycle credential); until then `twd deploy` says `preflight skipped`.

### precondition_failed
412 · mgmt. **When:** `If-Match` did not match the record's current `ETag`. **Fix:** re-read, then retry with the new ETag.

### profile_unsupported
400 · mgmt, cli. **When:** the tenant's control-plane profile can't serve what was asked. On the Headscale profile (ADR-015): `tailnet.service` (Headscale has no Tailscale Services), public routes or `publicIngress: allow` (no Funnel), or `twd up --wif` (no workload identity federation). **Fix:** remove `tailnet.service` and reach the workload at its own workload node's MagicDNS name; serve public traffic some other way. See [docs/headscale.md](headscale.md).

### public_unreachable
1 · cli. **When:** `twd deploy --wait-public` waited for the workload's public URL and it didn't answer from the public internet in time. The deploy itself succeeded. **Fix:** a new public name's certificate and DNS can take minutes; retry with a longer `--wait-public`, check the tailnet has HTTPS certificates on, and see [docs/public-ingress.md](public-ingress.md).

### push_unauthorized
403 · hostd. **When:** something other than mgmt's node pushed to hostd's push listener (port 7474). **Fix:** only `tag:twd-mgmt` may push; check the grant and who is on that tag.

### r2_backend_unavailable
503 · hostd. **When:** the host's R2 backend (its `-r2-backend` directory or S3-compatible service) failed a read, write or delete of an R2 object's bytes. In app code it surfaces as an R2 error (code 10001) whose message starts `twd r2: r2_backend_unavailable:`. **Fix:** the host's operator checks the backend (`twd logs --component hostd`): disk space, the S3 endpoint, its credentials. Retry the call.

### r2_blob_missing
500 · hostd. **When:** a bucket's index names an object whose bytes are not in this host's R2 backend: the workload moved from another host and the backend is `fs` (bytes stay on the old host's disk), or the backend lost them. Surfaces as an R2 error (code 10001) whose message starts `twd r2: r2_blob_missing:`. **Fix:** run hosts that share workloads on one S3-compatible backend (`-r2-backend s3`, [r2.md](r2.md)); write the object again.

### r2_too_large
400 · hostd. **When:** an R2 object or part over 5 GiB − 5 MiB. Surfaces as R2's "exceeds the maximum allowed object size" (code 100100). **Fix:** use a multipart upload, parts up to that size each.

### remote_unsupported
400 · cli. **When:** a command that runs only on the host that runs `twd up` (`twd logs`, `twd tenant add`) was run on a linked developer machine. **Fix:** run it on the host.

### queue_limit
400 · mgmt, hostd. **When:** a queue send is too big: a message over 128 KB, a `sendBatch` over 100 messages or 256 KB, a `delaySeconds` over 43200, or a queue over 1 GiB of kept messages. In a Worker, `send()` throws `Error: [queue_limit] … (400)`. **Fix:** send smaller messages (store large payloads elsewhere and send a reference), or let consumers drain the queue.

### queue_unavailable
503 · hostd. **When:** a queue producer's send (or `metrics()`) couldn't reach mgmt through hostd. `send()` throws `Error: [queue_unavailable] … (503)`; nothing was enqueued. **Fix:** retry; check `twd status` for mgmt and the host.

### reserved_tag
400 · mgmt. **When:** a bundle or namespace asks for one of twd's own tags (`tag:twd`, `tag:twd-mgmt`, `tag:twd-host`). **Fix:** use your app's tags (`tag:twd-wl-<name>`, class tags) in `twd.jsonc`.

### sandbox_unavailable
409 · hostd, cli. **When:** a second tenant's process-tier workload on a host that must not be shared: no process sandbox (Linux without Landlock, or `-sandbox none`), or the twd user can reach a container engine socket, is in the docker/podman/lxd/incus group, or can reach the system tailscaled while a workload could use it. **Fix:** run twd as a dedicated user without those sockets and groups, enable Landlock, use TCP listen mode for Node apps, deploy with `"tier": "microvm"`; or (tenants NOT isolated) `twd up --insecure-shared-host`.

### service_conflict
409 · cli. **When:** the systemd unit / LaunchAgent already runs twd for another state dir. **Fix:** `twd down --service --state-dir <that dir>`, then `twd up --service` again.

### service_failed
500 · cli. **When:** `systemctl --user` or `launchctl` failed while installing, starting or removing the service. **Fix:** the detail has the command and its output; `systemctl --user status twd` / `launchctl print gui/$(id -u)`.

### service_running
409 · cli. **When:** twd runs as a service (installed by `twd up --service`) and you ran a plain `twd up`/`twd down` that would fight it. **Fix:** `twd down --service`, then `twd up` again; or manage it with `systemctl --user`.

### service_unsupported
1 · cli. **When:** `--service` needs systemd (Linux, with a reachable user manager) or launchd (macOS) and neither applies. **Fix:** run `twd up` without `--service`, or fix the user session (`loginctl enable-linger`).

### spawn_budget_exceeded
429 · mgmt, netd (SDK). **When:** too many live descendants (`maxLive`) or too deep a chain (`maxDepth`) under `tailnet.spawn` in `twd.jsonc`. **Fix:** kill finished sub-agents (`ttl` helps), or raise the budget.

### state_conflict
409 · cli. **When:** the state dir is bound to another mint mode, tailnet, `--local-dev` setting or mgmt hostname than this command asks for; the API key file already serves another tenant; mgmt already has this host ID bound to another node; `twd link` on a machine that runs twd itself. **Fix:** the detail names the binding; use another `--state-dir`, or `twd down --purge` and start over.

### stop_timeout
500 · cli. **When:** `twd down` waited 3 min for the supervisor to stop and killed it. **Fix:** `twd status`; `twd logs --component supervisor`.

### store_conflict
409 · mgmt. **When:** a platform store journal append named a head (`after`) or incarnation that is no longer current: another writer (a replica, an admin) appended first, or the store was deleted. **Fix:** nothing for app code: the store object catches up and retries on its own. A client of the REST API reads the journal from its `after` and retries.

### store_limit
400 · mgmt, hostd. **When:** a platform store write is too big: an entry over 2 MiB, an append over 4 MiB or 128 entries, a compaction key over 1 KiB, or a store over 1 GiB. **Fix:** write less at once, or delete data you no longer need.

### store_unavailable
503 · hostd, mgmt. **When:** a platform store couldn't reach mgmt's journal (through hostd). Writes fail; reads still answer from the host's copy if the store has caught up with the journal on this host before, and fail if it never has (a new host). From the mgmt API it also means its database is unreachable. **Fix:** retry; check `twd status` for mgmt and the host.

### superseded
409 · mgmt. **When:** undeploy of a deployment that is not the latest for its workload. **Fix:** `twd undeploy <workload>` acts on the latest.

### supervisor_failed
500 · cli. **When:** the supervisor exited at start, or the systemd unit started but no supervisor came up. **Fix:** `twd logs --component supervisor`, then `twd up`.

### systemd_failed
500 · cli. **When:** `systemctl` failed during `twd host setup`. **Fix:** `journalctl -u twd-vmhelper`.

### tailnet_required
400 · cli. **When:** apikey mode (`--api-key-file`) needs `--tailnet`, or the tailnet could not be determined from the OAuth client. twd never bootstraps from the machine's own Tailscale login. **Fix:** `twd up --tailnet <name>.ts.net …`.

### tenant_has_live_objects
409 · mgmt, cli. **When:** `twd tenant rm` of a tenant with live objects. **Fix:** `twd tenant rm <tenant> --force` (kills and purges first).

### unhealthy
503 · cli. **When:** `twd status` finds twd not healthy: mgmt not answering, or no live host. **Fix:** `twd logs --component mgmt|hostd`.

### unknown_namespace
404 · mgmt. **When:** `(workload, class)` is not declared in the workload's `twd.jsonc`. **Fix:** declare the class under `tailnet.objects`, redeploy.

### unknown_object
404 · netd, hostd, cli. **When:** no such object for this ID or name (or not in the tenant you named). **Fix:** `twd ls --all --tenant <tailnet>`.

### unknown_store
404 · mgmt. **When:** the workload has no platform store of that kind and name (`twd kv namespace`, `GET …/stores/{kind}/{name}`). A store exists from its first write. **Fix:** check the name, or create it.

### unknown_queue
404 · mgmt. **When:** `twd queues info|rm|purge|messages` (or the API) named a queue the tenant doesn't have. A send or a consumer's first claim creates the queue, so this only comes from operator calls. **Fix:** `twd queues ls` for the names.

### unknown_tenant
404 · mgmt, cli. **When:** no tenant with that tailnet or ID. **Fix:** `twd tenant ls`.

### unknown_workload
404 · cli, mgmt. **When:** `twd logs`, `twd undeploy` or `twd host vmcheck` named a workload with no deployment; `twd d1 execute|export|migrations` named a workload that isn't running (a D1 database is reached through the running deployment that binds it). **Fix:** `twd ls`; `twd deploy`.

### unsupported
1 · cli. **When:** `twd host setup` on an OS without the microVM tier (it needs Linux with KVM). **Fix:** nothing to set up; every workload runs in the process tier here.

### upgrade_blocked
409 · mgmt, cli. **When:** `twd upgrade` can't start a fleet upgrade run because its plan has blockers: a release missing from the catalog, a host's tarball not imported, parts of the fleet more than one release line behind, or an intermediate release with contract migrations. **Fix:** `twd upgrade --plan` prints each blocker and its fix ([upgrading.md](upgrading.md)).

### upgrade_state
409 · mgmt, cli. **When:** `twd upgrade pause|resume|abort` on a run that isn't running (pause), paused (resume) or active (abort). **Fix:** `twd upgrade status` shows the run's state.

### upstream_unavailable
503 · netd. **When:** the platform upstream (workerd or node behind netd) is not answering. **Fix:** it is restarting or crashed; `twd logs <workload>`.

### usermod_failed
1 · cli. **When:** `twd host setup --microvm` could not add the user to the `kvm` group. **Fix:** `sudo usermod -aG kvm <user>` and log in again.

### version_skew
409 · mgmt, cli. **When:** two twd components are outside the supported version window: a host newer than twd-mgmt, or more than one release line behind it; a CLI more than one line behind mgmt (in `--json` mode); mgmt replicas more than one line apart; or a twd-mgmt older than the store's minimum (a newer twd-mgmt ran a contract migration). **Fix:** upgrade in order, mgmt first, then hosts ([upgrading.md](upgrading.md)); a store contracted by a newer twd-mgmt needs that version or newer, or the pre-upgrade backup restored.

### wif_credential_required
412 · cli. **When:** `twd up --wif --yes` ran without the trust credential's client ID; the stack is running and the console steps were printed. **Fix:** add the trust credential in the admin console, then `twd up --wif-client-id <id>`.

### wif_exchange_failed
401 · cli. **When:** Tailscale did not accept a token from mgmt's issuer for the mint credential: the credential's issuer or subject does not match what `twd up` printed (trailing slash, wrong tenant ID). **Fix:** fix it in the console and `twd up --wif-client-id <id>`.

### wrangler_required
1 · cli. **When:** `twd deploy --wrangler` / `twd dev --wrangler` found no wrangler CLI (`$TWD_WRANGLER`, the project's `node_modules/.bin/wrangler`, or `wrangler` on PATH). **Fix:** `npm install --save-dev wrangler`, or drop `--wrangler` to bundle with twd's built-in esbuild ([wrangler.md](wrangler.md)).
