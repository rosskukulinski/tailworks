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
- **From mgmt to a browser** opening `/` or the admin UI (`/ui/...`): when
  a `GET` or `HEAD` there prefers `text/html` to JSON (`*/*` alone or a tie
  stays JSON), the refusal is an HTML page with the same status and
  `tw-error` header, the code, the message, the `snippet`, a fix and the
  link here ([ADMIN-UI.md](ADMIN-UI.md#url-and-who-gets-in)). `/v1`,
  `/mcp` and the CLI always get JSON.
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
403 · netd, mgmt, cli. **When:** the peer is visible in the netmap but the port or service is not granted; from `tw deploy`, the preflight found grants or tagOwners your app's `tailnet.connects` needs missing. **Fix:** add the printed grant (`snippet`) to the tailnet policy, or let tailworks add its `tag:tw*` lines with `tw policy --apply` / `tw deploy --apply-policy` (needs the OAuth client's Policy File write scope), or deploy with `--allow-missing-grants`. On a terminal, plain `tw deploy` shows the lines and asks to write them first when the client can; you see `acl_denied` after answering no, without a terminal, with `--json` or a bare `--yes`, or with `--apply-policy=false`. If saving the policy fails with `tag not found: "tag:<name>"`, the `dst` tag doesn't exist in your tailnet yet: add it to `tagOwners` first (for example `"tag:db": ["autogroup:admin"]`) and tag the device it names, or remove it from `tailnet.connects`.

### already_joined
409 · mgmt. **When:** a credential (auth key) was requested for an object that already has a node. **Fix:** nothing to do; hostd retries by rejoining. Report it if it repeats.

### already_running
409 · cli. **When:** `tw up` finds a supervisor already running from this state dir, or `tw restore` or `tw custody migrate` was run while tailworks runs. **Fix:** `tw status`; `tw down` first if you meant to restart, restore or move custody.

### already_stopped
409 · cli. **When:** `tw undeploy <workload>` of a workload whose latest deployment is already undeployed. **Fix:** `tw ls --all` to see what is live.

### ambiguous_object
400 · cli. **When:** the object reference you gave (`tw kill`, `tw audit`) matches more than one object. **Fix:** name it by object ID (`tw ls` prints them).

### ambiguous_tenant
400 · cli. **When:** `--tenant` named a tailnet that has more than one tenant (several teams on one tailnet, [teams.md](teams.md)). **Fix:** name the tenant by its ID; the error lists them (`tw tenant ls`).

### approval_not_pending
409 · hostd, mgmt, cli. **When:** a decision (`tw approve`, the admin UI, `POST /_tw/approvals/<workflow>/<instance>`) names a Workflow instance that is not waiting for an approval: already approved or rejected, being decided by another request right now, timed out, or it never asked. **Fix:** `tw approvals [workload] --all` shows what is waiting and what was decided ([approvals](approvals.md)).

### approval_requires_identity
Thrown in the Worker · platform. **When:** app code sent an event of type `approval` or `approval-*` to a Workflow instance (for example the Agents SDK's `approveWorkflow()`). On tailworks an approval is a decision by a tailnet identity, so only tailworks delivers it. **Fix:** approve with `tw approve <workload> <instance>`, the admin UI, or a same-origin `POST /_tw/approvals/<workflow>/<instance>` from the app's page on the workload's node ([approvals](approvals.md)).

### audit_unverified
500 · cli. **When:** `tw audit` found an audit chain whose hashes or host signatures do not verify (tampering, or a store problem). **Fix:** treat the trail as suspect; `tw logs --component mgmt` and report it.

### backup_failed
500 · cli. **When:** `tw backup` could not copy the store, custody, `tw.json`, a Durable Object database or an R2 blob (a full disk, an unreadable file); nothing is left in `--out`. **Fix:** free space or fix the permission it names, then `tw backup` again.

### bad_bundle
400 · mgmt. **When:** a deploy bundle has no `tailworks.jsonc`, or neither `tailnet.tags` nor `tailnet.objects`. **Fix:** add `"tailnet": { "tags": ["tag:tw-wl-<name>"] }` to `tailworks.jsonc` (`tw init` templates show the shape).

### bad_cursor
400 · mgmt. **When:** a `cursor` query parameter does not decode. **Fix:** pass `nextCursor` from the previous page unchanged, with the same filters.

### bad_object_name
400 · hostd, mgmt. **When:** code created (`get`, `idFromName`, `spawn`, a public create) an object whose name contains a control character: a C0 control (NUL, TAB, CR, LF, ESC, …), DEL, a C1 control or a bidi control (U+202A–U+202E, U+2066–U+2069, LRM/RLM). Objects created with such a name before this check are not renamed or deleted. **Fix:** name objects with printable text; if the name comes from user input, strip or encode those characters first (for example `encodeURIComponent`).

### bad_request
400 · netd, mgmt, hostd, cli. **When:** a malformed body, parameter, filter or command line (the CLI uses it for every usage error: missing argument, unknown template, unreadable key file, bad `tailworks.jsonc`). **Fix:** the detail says what was wrong; `tw <command> --help` lists the flags.

### binding_forbidden
403 · hostd. **When:** a request named a service binding entrypoint or RPC method (the `Tailworks-Binding` header) but did not come from a tailworks workload or object of the target's tenant (its node has no certificate from the tenant), or it reached an object's node or a public route rather than a workload's entry node. Plain requests are not affected. **Fix:** call named entrypoints and RPC methods through a wrangler `services` binding of a workload in the same tenant ([wrangler.md](wrangler.md#service-bindings)); from anywhere else, call the target's default `fetch()` over the tailnet.

### bootstrap_failed
502 · netd, mgmt, cli. **When:** an object's node could not be created: the control plane refused the mint (tag not owned, key invalid), the token exchange failed, or the node never joined. From `tw up`, mgmt's or the host's own node failed to join. From `tw deploy`, "the workload node for <workload> is killed": the workload's entry node was killed (`tw kill`) while it was deployed. **Fix:** check the tagOwners snippet (`policy_missing_tag_owners`), the credential (`tw doctor`), then `tw logs --component mgmt`. From `tw up`, "… without the tag:tw-mgmt tag" means control dropped the tag because `tag:tw` doesn't own `tag:tw-mgmt`: add the tagOwners snippet, remove the half-joined node in the admin console (Machines), then `tw up` again. For a killed workload node: `tw undeploy <workload>`, then deploy it again (see `killed`).

### build_command_failed
1 · cli. **When:** the wrangler config's custom build (`build.command`) exited non-zero; `tw deploy`, `tw dev` and `tw policy` run it before bundling ([wrangler.md](wrangler.md#custom-builds)). Nothing is deployed. **Fix:** its output is printed above the error; fix the build and re-run, or run it yourself and remove `build` from the wrangler config.

### build_failed
500 · cli. **When:** esbuild (workerd apps) or `create-tailworks build-node` (Node apps) could not bundle the app. **Fix:** the compiler output is in the detail; fix the source and re-run. `Could not resolve "@tailworks/…"` means tailworks' own JS packages are missing next to its bundler: in a source checkout run `make deps` (a plain `make build` doesn't install them), otherwise reinstall tailworks; `tw doctor` checks it.

### bundle_pruned
409 · mgmt, cli. **When:** `tw rollback` to a deployment whose bundle mgmt no longer holds: mgmt keeps the bundles of each workload's newest deployments (tw-mgmt `-bundle-retention`, default 10) and prunes older ones, keeping their record and hash. Nothing is deployed. **Fix:** `tw deployments <workload>` marks pruned bundles with `*`; roll back to a retained one, or check out that version and `tw deploy` it.

### cancelled
0 · cli. **When:** you answered no at a `tw up --apply-policy`, `tw tenant add --apply-policy`, `tw deploy --apply-policy`, `tw policy --apply`, `tw agent setup` or `tw agent remove` confirmation prompt (no at the policy question plain `tw up` and `tw deploy` ask on a terminal isn't an error: they go on printing the lines), or typed something other than the name at a delete's prompt. **Fix:** nothing was started, written or deleted; re-run (with the policy in place for `tw up`), or add `--yes` to skip the prompt.

### cap_limit_exceeded
403 · netd (SDK `@tailworks/mcp`). **When:** the caller holds the tool but a numeric limit or budget in its grant is exceeded (e.g. `maxUsd`, `perDayUsd`). **Fix:** raise the limit in the `app` capability of the grant, or wait for the daily budget to reset.

### cap_missing
403 · netd (SDK `@tailworks/mcp`), mgmt (`/mcp`, approvals), hostd, cli. **When:** the caller's app capability lacks the tool (or the capability itself) the MCP server requires. From tailworks' own MCP server: a `deploy`, `kill` or `undeploy` call without a `github.com/rosskukulinski/tailworks/cap/mcp` grant naming the tool, or, on the tailworks host, a `tw mcp` bridge started without `--allow <tool>`. **Fix:** add the `app` capability with the tool to the grant from the caller's tag to the server's tag (the error's `snippet` has it); for tailworks' MCP server, ask the tailnet admin, don't work around it. For approvals: the approver's `github.com/rosskukulinski/tailworks/cap/approve` grants do not cover the workload, Workflow, step, decision or a param limit (`missing` says which); grant it to the approver on the workload's tag (the app's page, `/_tw/approvals`) and on `tag:tw-mgmt` (`tw approve`, the admin UI) ([approvals](approvals.md)).

### confirmation_required
1 · cli. **When:** a command that asks first had no terminal to ask on (a coding agent, a script, or `--json`), so it did nothing. These commands ask first: `tw agent setup` and `tw agent remove` (they would write or delete files), `tw upgrade`, `tw upgrade fleet` and `tw upgrade rollback`, `--apply-policy` on `tw up`, `tw tenant add` and `tw deploy`, `tw policy --apply`, and every command that deletes data or a tenant's resources: `tw d1 rm`, `tw kv namespace rm`, `tw queues rm`, `tw queues purge`, `tw r2 bucket rm`, `tw tenant rm`, `tw host rm`, `tw restore`, `tw upgrade rollback` when it restores the store, and `tw uninstall`. **Fix:** run it in a terminal and answer the prompt (a delete asks you to type the name), or add `--yes`. An agent should tell the user what will be lost (and show `--dry-run` where there is one) and add `--yes` only after the user agreed to that action. A bare `--yes` never writes the tailnet policy: that takes `--apply-policy --yes`.

### credential_file_insecure
403 · cli. **When:** a credential file (`tw tenant add --oauth-client-file`, `--api-key-file`, `--db-file`) is readable by other users, or is owned by another user. (`tw up` tightens its own OAuth client file to 0600 instead.) **Fix:** `chmod 600 <file>` then run the command again.

### custody_migrate_failed
500 · cli. **When:** `tw custody migrate` could not read an entry from file custody, or could not write, or read back the same value from, setec; `tw.json` still names file custody. **Fix:** fix the cause it names (setec unreachable, its grants), then run `tw custody migrate` again. Entries already copied with the same value are kept.

### custody_unmapped
409 · cli. **When:** `tw custody migrate` found file custody entries that no store row and no `custody_names` entry names, so it can't tell setec what to call them, and copied nothing. Nothing in tailworks reads such an entry: it is left by an interrupted write, or by data deleted before custody names were indexed. **Fix:** move the listed files out of `<state>/mgmt/custody/secrets`, then run `tw custody migrate` again; or keep file custody ([postgres.md](postgres.md#move-file-custody-to-setec)).

### d1_error
400 · mgmt, cli. **When:** a statement of `tw d1 execute`, `tw d1 migrations apply` or `POST …/d1/{name}/query` failed: SQLite's error (syntax, constraint, no such table) or a D1 limit (statement over 100 KB, over 100 parameters, a value over 2 MB, a result over 16 MiB, one write over 2 MiB encoded). Nothing of the batch was applied (a failing migration isn't recorded). In a Worker the same failures throw `D1_ERROR: …`, as on Cloudflare. **Fix:** fix the SQL; split a large import into several files or batches.

### delegation_exceeds_parent
403 · mgmt, netd. **When:** `ctx.tailnet.spawn()` asked for capabilities, tags or destinations the parent object does not have (`missing: {src, dst, cap, field}`). **Fix:** spawn with a subset of the parent's grants, or widen the parent's grant in the policy.

### delegation_invalid
403 · mgmt, hostd, netd. **When:** a spawn names a child tag not owned by the parent's tags, the parent is unknown (404) or dead (410), or an object tried to kill something that is not itself or a descendant. **Fix:** fix the `tagOwners` so the parent's tag owns the child's; kill through an ancestor or the CLI.

### deploy_crashloop
500 · cli. **When:** `tw deploy`, `tw promote` or `tw rollback` was waiting for the deployment and its workerd, after it had answered its health check once, kept exiting: the deployment is `crashloop`. The host keeps restarting it with exponential backoff (200 ms doubling to 5 min) and never gives up; it reports `active` again once a restart answers. The detail lines are the attempt count and workerd's last log lines. **Fix:** `tw logs <workload>`; fix the Worker and deploy again. See [observability.md](observability.md#crash-loops).

### deploy_failed
500 · cli. **When:** the host could not start the workload (bad bundle or config, or workerd or node exited before it ever answered its health check); the host does not retry it. `tw dev` prints workerd's output. **Fix:** read the detail lines; `tw logs <workload>` on the host.

### derp_unreachable
502 · netd (SDK). **When:** no direct path to the peer and no reachable DERP relay. **Fix:** check the host's outbound connectivity (UDP 3478, TCP 443 to DERP).

### device_delete_unavailable
403 · mgmt, cli. **When:** a kill held (node closed, tombstoned) but the device could not be deleted: the tenant's OAuth client lacks the `devices:core` scope. **Fix:** generate an OAuth client with Auth Keys (write) and Devices › Core (write), overwrite the file, `tw up`, then revoke the old client; remove leftover devices in the admin console (Machines).

### dial_failed
502 · netd, hostd. **When:** the tailnet dial to a visible peer failed (connection refused, timeout), or the TLS handshake of `ctx.tailnet.connect(addr, { tls: "netd" })` failed. **Fix:** is the destination up and listening on that port, with a certificate for that name? Try again; check `tw logs`.

### doctor_failed
1 · cli. **When:** `tw doctor` had at least one failing check. **Fix:** each failing line prints its own `fix:`.

### egress_unenforced
409 · hostd. **When:** a `"runtime": "node"` deployment on a host with no process sandbox that already serves other tenants: the Node runtime's egress proxy is cooperative there, which would be a cross-tenant SSRF vector. **Fix:** use the workerd runtime, a single-tenant host, Linux with Landlock, or (tenants not isolated) `tw up --insecure-shared-host`.

### exists
409 · mgmt, cli. **When:** create of an ID that already exists (`tw host add <id>` for a known host). **Fix:** `tw host rm <id> --force`, then add again; or pick another ID.

### exit_node_unavailable
503 · netd. **When:** a Worker, Node app or container called an internet destination on a host whose internet path is a tailnet exit node (`tw up --worker-internet=exit-node=<node>`, or `tw-hostd -worker-internet=exit-node=<node>`), and the exit node can't be used right now: it isn't in the host node's netmap, it isn't offered to the host as an exit node (no approved exit routes, or no `autogroup:internet` grant for `tag:tw-host`), it is offline, the dial through it failed, or it refuses to answer DNS for the host's node. The detail says which. tailworks never falls back to the host's own network. **Fix:** `tw doctor` on the host. Advertise and approve exit routes on the exit node; grant `tag:tw-host` → `autogroup:internet` in the tailnet policy; for an exit node that refuses DNS for tagged peers, add `dns=<resolver ip>` to the setting (`exit-node=<node>,dns=1.1.1.1`) so names resolve at that resolver through the exit node ([tailworks-jsonc.md](tailworks-jsonc.md#tailnetegressvia)). Egress that is turned off by config is [`internet_egress_unavailable`](#internet_egress_unavailable) instead.

### experimental_flag
403 · mgmt, hostd, cli. **When:** the app's `compatibility_flags` (wrangler config) list a flag workerd marks experimental (`unsafe_module`, `rtti_api`, `precise_timers`, `experimental`, … ; the full list is `internal/compatflags`), and no host allows them. tailworks runs every workerd with `--experimental` for its own platform worker, so an experimental flag would otherwise reach tenant code unreviewed (`workerd:unsafe`, eval, runtime type introspection, another clock); upstream marks them not ready. mgmt refuses the deploy and its dry run (`tw deploy`'s preflight), and a host refuses the generated config whatever mgmt decided. **Fix:** remove the flag from `compatibility_flags` (the stable flags, `nodejs_compat` among them, are fine: [COMPAT.md](COMPAT.md#experimental-flags)). If you run the host and accept the risk, start it with `tw up --allow-experimental-flags` (or `tw-hostd -allow-experimental-flags`); mgmt then places such deployments on a host that allows them.

### feature_unavailable
409 · mgmt, cli. **When:** the deployment needs a host feature (`kv`, `r2`, `d1`, `telemetry`, …) that no eligible host advertises, or the workload's current host runs a tailworks too old to offer it. Hosts report their features when they register ([upgrading.md](upgrading.md)). **Fix:** upgrade the hosts (`tw upgrade --plan` lists them), or redeploy with `--replace` to move off the old host.

### forbidden
403 · mgmt, hostd. **When:** the caller's role is fine but the record is not its own: a host reading another host's deployment or lease, a request to a workerd instance from the wrong peer; a host node asking for the admin UI (`/ui/`); or, with the message `cross-site request refused`, a browser-originated mutation (it carried `Origin` or `Sec-Fetch-Mode`) whose `Sec-Fetch-Site` was not `same-origin`/`none`, whose `Origin` was not the request's own host and one of mgmt's own names, or which hit `/v1` without the `X-Tw-UI: 1` header (docs/ADMIN-UI.md). **Fix:** usually a bug or stale state; `tw status` and report it. For the cross-site refusal, use the admin UI from mgmt's own URL (`http(s)://tw-mgmt.<tailnet>/ui/`), never through another origin; CLI and hostd clients send none of those headers and are never affected.

### forbidden_destination
403 · netd. **When:** egress to a destination class netd always refuses: loopback, private (RFC 1918, ULA), link-local, multicast, cloud metadata, CGNAT and tailnet addresses that aren't peers, the object itself, or the host (ADR-019). For internet egress this is checked on the address actually dialled, so an internet name that resolves to any such address is refused too, as is a name under a special-use suffix (`.internal`, `.local`, `.localhost`, `.lan`, …), which is never looked up. On a host with no internet path, any non-tailnet IP address. **Fix:** call tailnet peers by their tailnet name (an object on the same host is one: netd delivers the call in process). A service on your LAN isn't reachable as "internet": put it on the tailnet.

### funnel_required
412 · cli. **When:** `tw deploy` of an app with tailworks.jsonc `"public"` routes, and the tenant policy has no `funnel` node attribute for `tag:tw-public` (ADR-029). **Fix:** add the printed `nodeAttrs` entry for `tag:tw-public` (never `tag:tw-mgmt`) and deploy again.

### helper_failed
500 · cli. **When:** `tw host setup --microvm` installed `tw-vmhelper.service` but it did not come up. **Fix:** `journalctl -u tw-vmhelper`.

### host_failed
1 · cli. **When:** `tw up` (single process): the host agent embedded in tw-mgmt failed to start or to register with mgmt. Also the supervisor's exit error (in `supervisor.log`) when tw-hostd (two processes) kept exiting and the supervisor gave up; it exits 1 so the service manager restarts it. **Fix:** the detail has the cause; `tw logs --component hostd` and `tw doctor`, then `tw up` again (under `tw up --service` it restarts on its own).

### host_holds_leases
409 · mgmt, cli. **When:** `tw host rm <id>` while the host still holds namespace leases. **Fix:** `tw host drain <id>` moves its workloads off first. If the host is gone for good, `tw host rm <id> --force` kills its objects and releases the leases; whatever they kept on that host is lost. (`--drain` is a deprecated spelling of `--force`.)

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
503 · cli. **When:** tw-hostd is not answering, or did not register with tw-mgmt after `tw up`. From a linked machine (`tw logs`), mgmt could not reach the workload's host on its push port. **Fix:** `tw logs --component hostd`. From a linked machine: check the policy grant `tag:tw-mgmt` → `tag:tw-host` `tcp:7474`, and that the host is up.

### hostd_unavailable
503 · hostd, netd (SDK). **When:** the host agent could not be reached from the platform wrapper (hostapi socket down). **Fix:** `tw status`; the host is restarting or dead.

### identity_conflict
409 · mgmt. **When:** an object ID collided across identities (two workloads derived the same ID). **Fix:** rename one object or class; report it (IDs are HMAC-derived and this should not happen).

### identity_pending
503 · netd, mgmt, cli. **When:** the object's node is minted but not ready yet (`Retry-After: 1`); an outbound call from a new object waited 20 s for mgmt to accept its ready report (its node certificate), or that report failed; `tw dev` waited 60 s for a deploy. **Fix:** retry (a failed ready report is re-sent on a later call); if it persists, `tw logs <workload>` and check the control plane and mgmt.

### image_build_failed
1 · cli. **When:** `tw deploy` could not build or push an app's Dockerfile container image with `docker buildx`. **Fix:** the build output is in the detail; check `docker buildx version` works on the deploying machine.

### image_unavailable
502 · mgmt, hostd. **When:** a container image could not be pinned at deploy: it isn't in tailworks' registry, it has no image for any of the pool's arches, or the upstream mirror failed. On an arm64 host, `cloudflare/sandbox:<v>` is pinned to tailworks' rebuild `tw.local/cloudflare-sandbox:<v>-arm64`; mgmt fills that from the alias's published `source` image on first use, and only when the alias has no `source` (or the fetch failed) does the deploy stop here. From hostd: the container's pin is for another arch, or names a repository outside the tenant's scope. **Fix:** the message names the image and the registry repository. For an image your app builds, run `tw deploy` (it builds and pushes). For an arm64 sandbox rebuild, check mgmt can reach the `source` registry; without one, or with `-no-mirror`, build it with `guest/images/build-sandbox-images.sh` and push it with `tw image push` ([containers.md](containers.md#platforms)). From hostd, redeploy so mgmt pins the image for that host's arch.

### immutable_field
400 · mgmt. **When:** a PATCH touched a field the caller may not change (IDs, tailnet, creation time). **Fix:** send only the mutable fields (`tw tenant set`, `PATCH /v1/objects/{id}`).

### internal
500 · mgmt, cli. **When:** an unexpected error with no better code; the CLI wraps any untyped error this way. **Fix:** the detail is redacted but usually names the cause; `tw logs --component mgmt|hostd`; report it with the `trace_id`.

### internet_egress_unavailable
403 · netd. **When:** a Worker (or Node app) called an internet destination, such as `https://api.github.com/`, and internet egress is turned off by config: the workload's `tailnet.egress.internet` is `false` or `[]`, the host isn't on its allowlist, or the host's operator runs `tw up --worker-internet=off` (or `tw-hostd -worker-internet=off`). Internet egress is on by default ([tailworks-jsonc.md](tailworks-jsonc.md#tailnetegressinternet)). An internet destination is a public IP, or a name that is not a tailnet name (a bare short name, a name under a tailnet's MagicDNS suffix, or any `*.ts.net` name); a refused one is never looked up in DNS. **Fix:** allow the host in `tailnet.egress.internet` (or remove the setting) and redeploy, or put a gateway for the API on your tailnet and call that ([recipes.md](recipes.md#call-an-internet-api)). A tailnet name that isn't in the netmap is [`peer_not_visible`](#peer_not_visible) instead, and a private or metadata address is [`forbidden_destination`](#forbidden_destination).

### internet_port_refused
403 · netd. **When:** a Worker, Durable Object, Node app or container opened an internet flow to **TCP port 25** (`connect("smtp.example.com:25")`, `node:net`, a `fetch()` to `http://host:25/`), and the workload's `tailnet.egress.internet` doesn't name that destination with the port. Internet flows leave from the host's own address, so mail relayed from a workload would come from the operator; port 25 is refused by default, before DNS and before any dial, and the refusal is audited as `deny` with this code. Authenticated submission (465, 587) and every other port are not affected ([security-model.md](security-model.md#egress)). **Fix:** for a relay you run or trust, list it with the port in `tailnet.egress.internet` (`["smtp.example.com:25"]`; an entry that names a port covers that port only, so keep a separate entry for the host's other ports) and redeploy, or send mail through a provider's API or submission port, or through a gateway on your tailnet ([recipes.md](recipes.md#call-an-internet-api)). Hosts older than this policy need the `internet-ports` feature for the entry ([upgrading.md](upgrading.md)).

### keys_in_guest
1 · cli. **When:** `tw host vmcheck --keyscan` found key material inside a microVM guest. **Fix:** this must never happen (keys stay in hostd); report it with the output.

### killed
410 · netd, mgmt, cli. **When:** the object was killed: in-flight flows were RST, the node closed, and its tombstone answers new requests for the tombstone window; `tw kill` of an object that is already killed. **Fix:** a killed name stays killed while its workload is deployed (redeploys and rollbacks keep the kill). To use the name again, `tw undeploy <workload>` and deploy it again: that releases every name the workload had (the release is audited as `purge`). `tw down --purge` releases every killed name of the tenant on this machine.

### lease_held
409 · mgmt. **When:** another host holds the namespace lease a host tried to take. **Fix:** nothing; placement picks the lease holder. To move a workload off a live holder, redeploy with `--replace` (or `tw host drain <id>` to move everything off that host). If the holder is gone for good, `tw host rm <id> --force`.

### lease_lost
409 · mgmt, hostd, netd (SDK). **When:** the host's lease epoch was fenced: the store knows a newer holder (after a partition or a restart race). **Fix:** hostd re-acquires on its own; persistent `lease_lost` means two hosts with the same host ID.

### login_timeout
408 · cli. **When:** a tailnet node tailworks joins interactively (`tw link --tsnet`) printed a login URL and nobody completed the browser login within 5 minutes. **Fix:** run the command again and finish the login, or pass an auth key file (`tw link --tsnet --tsnet-authkey-file <file>`) for an unattended join.

### mgmt_failed
1 · cli. **When:** `tw up` (single process): the embedded tw-mgmt failed to start. Also the supervisor's exit error (in `supervisor.log`) when mgmt stopped after start: its listener failed (single process) or tw-mgmt kept exiting (two processes); it exits 1 so the service manager restarts it. **Fix:** the detail has the cause; `tw logs --component mgmt` and `tw doctor`, then `tw up` again (under `tw up --service` it restarts on its own).

### mgmt_not_found
404 · cli. **When:** `tw link` with no endpoint found no `tw-mgmt` through MagicDNS. **Fix:** `tw link tw-mgmt.<tailnet>.ts.net`.

### mgmt_unavailable
503 · cli. **When:** tw-mgmt did not become healthy after `tw up`. **Fix:** `tw logs --component mgmt`.

### mgmt_unreachable
503 · cli. **When:** a linked developer machine cannot reach mgmt's node. **Fix:** is your Tailscale connected to the tailnet that runs tailworks, and does a grant let you reach `tag:tw-mgmt` on tcp:443/80?

### mint_mode_removed
400 · mgmt, cli. **When:** a Tailscale API key (`--api-key-file` with Tailscale's control URL and without `--control=headscale`, or with `--control=tailscale`) or workload identity federation was asked for, or a state dir or tenant an older tailworks set up in the `apikey` or `wif` mint mode tried to mint. Both modes were removed: tailworks mints with an OAuth client. **Fix:** create an OAuth client (INSTALL.md), then `tw up --oauth-client-file <file>` (keeps the tenant and its deployments), or `tw tenant set <tenant> --oauth-client-file <file>` for a tenant added with `tw tenant add`. On Headscale, add `--control-url <your Headscale server_url>` (and `--tailnet <base_domain>` for `tw up`).

### mint_mode_required
400 · cli. **When:** `tw up` was given no credential. **Fix:** `tw up --oauth-client-file <file>` with an OAuth client of your tailnet (the error prints how to create one; INSTALL.md); on Headscale, `tw up --control-url <server_url> --tailnet <base_domain>` with the API key in `~/.config/tailworks/headscale.env` or `--api-key-file <file>`.

### mirror_disabled
409 · mgmt. **When:** `tw image public add` named an external image, and this tw-mgmt does not mirror external images (`-no-mirror`). **Fix:** push the image into the public namespace instead (`tw image push`), or restart tw-mgmt without `-no-mirror` ([containers.md](containers.md#images-build-registry-pinning-host-pull-adr-032-d3)).

### misdirected_request
421 · mgmt. **When:** a request on mgmt's tailnet listeners (`:80`, `:443`, the `-tsnet-service` VIP, the operator console) whose `Host` header is neither one of mgmt's own names (its MagicDNS FQDN or short name, `-tsnet-hostname`, `-tsnet-service`) nor an IP literal. This is the DNS-rebinding guard: a web page on a domain rebound to mgmt's IP sends its own domain as `Host` (docs/ADMIN-UI.md). Refused before WhoIs, for every method. **Fix:** address mgmt by its tailnet name (`http(s)://tw-mgmt.<tailnet>/`, `tw whoami` prints it) or its tailnet IP; not through a CNAME or another DNS name that points at it. If you renamed the mgmt node, restart tw-mgmt so it learns the new name. A browser opening the admin UI by the wrong name gets an HTML page listing mgmt's own names.

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

### network
1 · cli. **When:** `tw upgrade` could not reach the release repository to find the latest release or to download its tarball. **Fix:** check the connection and retry, or download the tarball yourself and run `tw upgrade --from <tarball>`; `TW_REPO` and `TW_RELEASE_BASE` point it at another repository or a mirror, as for `get.sh`.

### no_cgroup2
1 · cli. **When:** `tw host setup --microvm` on a Linux host without the cgroup v2 unified hierarchy (`/sys/fs/cgroup/cgroup.controllers` is missing). **Fix:** boot with the unified hierarchy (`systemd.unified_cgroup_hierarchy=1`, the default on current distributions); the process tier works without it.


### no_host
503 · mgmt. **When:** a deployment has no live host to run on (none registered, none in the tenant's pool, or all stale). **Fix:** `tw status` on the host; `tw host ls`.

### no_identity
401 · netd, mgmt, hostd. **When:** WhoIs failed for the connection: the request carries no verified tailnet caller (a non-tailnet source, or the egress proxy's credentials are stale). **Fix:** call over the tailnet; from a developer machine use `tw link`. In a browser (the admin UI), open it from a device on the tailnet.

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
403 · mgmt, cli. **When:** a tailnet caller without the `github.com/rosskukulinski/tailworks/cap/admin` capability called an admin endpoint (`tw link`), or `/mcp` without `github.com/rosskukulinski/tailworks/cap/admin` or `github.com/rosskukulinski/tailworks/cap/mcp`. **Fix:** add the printed grant (`snippet`) from your user/group/tag to `tag:tw-mgmt`. Opening the admin UI in a browser shows the same grant on an HTML page: ask your tailnet admin to add it (members of `autogroup:admin` already have it), or use `tw` on the host.

### not_found
404 · mgmt. **When:** no such record (deployment, host, object, volume). **Fix:** check the ID (`tw ls --all`, `tw host ls`).

### not_installed
1 · cli. **When:** workerd, or create-tailworks (bundler and templates), is not where tailworks expects it. **Fix:** install from a release tarball (`install.sh`); in a checkout `make deps`, or `--workerd <path>` / `$TW_WORKERD`, `$TW_CREATE_TAILWORKS`.

### not_ready
409 · mgmt. **When:** a node certificate was requested through the deprecated `GET /v1/objects/{id}/certificate` before the object is `ready`. **Fix:** wait for the object to reach `ready`; better, switch to `GET /v1/certificates/{stableNodeID}?tenant=` with the object's `stableNodeID` and `tenantID`.

### not_running
409 · cli. **When:** tailworks is set up in this state dir but its supervisor is not running. **Fix:** `tw up` (or `systemctl --user start tw`).

### not_supported
501 · cli, hostd, SDK. **When:** a command or call tailworks doesn't build yet: `tw r2 object get|put` (object bytes live in the hosts' R2 backend, which mgmt can't reach), container preview URLs, or `tw dev`/`tw deploy` of an app whose own code imports `cloudflare:email` ("cloudflare:email is not supported on tailworks yet (imported by <file>)": the built-in bundler refuses it; a dependency's import under `node_modules`, such as the Agents SDK's, gets a stub that throws on `new EmailMessage()`); `ctx.tailnet.connect()` in a plain-DO object (`"egress": "workload"`: it has no node of its own to connect from) or with `secureTransport` other than `"off"`. **Fix:** go through the workload (its `env.BUCKET` binding); for sockets, use facet mode or stock `connect()` (which leaves as the workload node), and `{ tls: "netd" }` for TLS; see [COMPAT.md](COMPAT.md#connect); email isn't supported yet: remove the `cloudflare:email` import, or keep it out of the tailworks build (behind a module only your Cloudflare build imports), see [COMPAT.md](COMPAT.md#email).

### not_up
409 · cli. **When:** this state dir has no tailworks config or tenant yet. **Fix:** `tw up …` on the host, or `tw link <mgmt>` on a developer machine.

### npm_install
1 · cli. **When:** `tw upgrade` ran from a tailworks that npm installed (`npm install -g tailworks` or `npx tailworks`): npm owns the binaries' directory, so `tw upgrade` won't replace it (and `tw upgrade rollback` has no previous release to put back: npm keeps none); or `tw up --service` ran from npx's cache, which npm may clear at any time, so a login service can't point there. **Fix:** upgrade with npm: `tw backup && tw down && npm install -g tailworks@latest && tw up` (`tw upgrade --plan`, `tw version --check` and `tw doctor` print it; [upgrading.md](upgrading.md#installed-with-npm)); for a service, install globally (`npm install -g tailworks`), or run `tw up --service=false`. For `tw upgrade` itself, install the release tarball (`scripts/get.sh`). To go back on npm: `tw down`, `npm install -g tailworks@<previous>`, `tw restore <backup> --yes` if a migration ran, `tw up`.

### oauth_exchange_failed
401 · cli. **When:** Tailscale refused the OAuth client in `--oauth-client-file` (wrong secret, revoked client). **Fix:** generate a new client (Settings › Trust credentials), overwrite the file (0600), `tw up`.

### object_killed
410 · netd. **When:** a new request reached a killed object: its node's tombstone answered. **Fix:** as `killed`.

### object_quota_exceeded
429 · mgmt. **When:** the tenant's `maxObjects` is reached. **Fix:** `tw tenant set <tenant> --max-objects N`, or kill idle objects.

### peer_not_visible
403 · netd. **When:** the destination is a tailnet name or address that is not in this node's netmap: a grant is missing, or the name does not exist (also how one tenant's objects see another tenant's nodes), or, for an object on the same host, that object's packet filter does not accept this caller. A tailnet name is never looked up in host DNS. An internet name goes out the internet path instead ([`internet_egress_unavailable`](#internet_egress_unavailable) when that is off). **Fix:** add a grant from the object's tag to the destination's tag (`tw deploy` prints it), or check the name.

### plan_feature_missing
501 · netd (SDK). **When:** the tailnet's plan or control server lacks a feature tailworks would use. **Fix:** see the message for the feature; [COMPAT.md](COMPAT.md) lists what is built.

### policy_apply_failed
500 · mgmt, cli. **When:** `tw up --apply-policy` or `tw tenant add --apply-policy` could not write the policy snippet (validation failed, or the ETag changed); or `tw policy --apply` / `tw deploy --apply-policy`: the control plane rejected the merged policy (for example a grant to a tag no `tagOwners` entry defines), or tailworks refused the snippet (RT-06). Nothing was written. **Fix:** add the printed snippet in the admin console, after fixing what the message names.

### policy_conflict
409 · mgmt, cli. **When:** `tw policy --apply` / `tw deploy --apply-policy` / `tw up --apply-policy`: the tailnet policy changed between tailworks' read and its `If-Match` write, and again on the one retry. Nothing was written. **Fix:** run it again; if someone keeps editing the policy, wait for them, or add the printed snippet yourself.

### policy_missing_tag_owners
403 · mgmt, cli. **When:** the control plane refused to mint a key for tailworks' own tags: `tag:tw` does not own `tag:tw-mgmt` / `tag:tw-host`. Nothing was minted. **Fix:** add the printed `tagOwners` entries inside the policy's existing `tagOwners` section (replacing any line for the same tag; a second `tagOwners` key makes the policy fail to parse) and re-run `tw up`, or run `tw up --apply-policy` to have tailworks write it (the OAuth client needs the Policy File write scope; `tag:tw` itself must already be in `tagOwners`).

### policy_unavailable
503 · mgmt. **When:** mgmt cannot read the tenant policy: the OAuth client has no `policy_file:read`, so spawn checks and deploy preflight cannot run. **Fix:** give the client Policy File › Read; until then `tw deploy` says `preflight skipped`.

### policy_write_denied
403 · mgmt, cli. **When:** `tw policy --apply`: the tenant's OAuth client may not write the tailnet policy (it has Policy File › Read, or no Policy File scope). Nothing was written. (`tw deploy --apply-policy` says so in one line and fails with `acl_denied` and the snippet instead.) **Fix:** generate a client with the Policy File write scope and `tw tenant set <tenant> --oauth-client-file <file>`, or add the snippet `tw policy` prints in the admin console.

### precondition_failed
412 · mgmt. **When:** `If-Match` did not match the record's current `ETag`. **Fix:** re-read, then retry with the new ETag.

### prefix_mismatch
1 · cli. **When:** the `tw` running `tw upgrade` (or `tw upgrade rollback`) lives in another install (or in none, like an extracted tarball or a dev build) than the one that runs the state dir, so `tw upgrade` won't guess which to replace. The message names both and where the state dir's install came from (tw.json, the service, the running supervisor, or the state dir itself). **Fix:** `tw upgrade --prefix <dir>` to name the install to replace, or run the `tw` installed there: `<dir>/bin/tw upgrade` ([upgrading.md](upgrading.md#a-tw-up-machine)).


### preview_conflict
409 · mgmt, cli. **When:** `tw deploy --preview` would run the preview as `<workload>--preview-<alias>`, and a workload of that name exists that is not a preview of this workload. **Fix:** pick another `--preview-alias`, or undeploy that workload.

### preview_failed
409 · mgmt, cli. **When:** `tw promote` named a preview its host reported `failed`. **Fix:** `tw logs <workload>--preview-<alias>`, fix the app, and deploy a new preview.

### preview_unsupported
400 · mgmt, cli. **When:** `tw deploy --preview` of a bundle that binds queues (queues are shared by the whole tenant, so a preview would send to and consume the workload's messages), or of a workload that is itself a preview or a replica. **Fix:** deploy it as a workload of its own (`--name`); preview the workload itself, not its preview or replica.

### profile_unsupported
400 · mgmt, cli. **When:** the tenant's control-plane profile can't serve what was asked. On the Headscale profile (ADR-015): `tailnet.service` (Headscale has no Tailscale Services), public routes or `publicIngress: allow` (no Funnel), or an OAuth client (`--oauth-client-file`: Headscale mints with its API key). **Fix:** remove `tailnet.service` and reach the workload at its own workload node's MagicDNS name; serve public traffic some other way. See [docs/headscale.md](headscale.md).

### public_name_in_use
409 · mgmt. **When:** `tw expose release <name>` (`DELETE /v1/tenants/{id}/public-names/{name}`) while the workload still serves it: its latest deployment declares `"public"` routes, or its public node is live. **Fix:** `tw undeploy <workload>`, or remove `"public"` from its `tailworks.jsonc` and redeploy, then `tw expose release <name>` ([public-ingress](public-ingress.md#name-ownership)).

### public_name_owned
409 · mgmt, cli. **When:** a deploy (or `--dry-run`) declares `"public"` routes under a workload name that another tenant on the same tailnet owns as its public name. The first tenant to deploy public routes under a name keeps `https://<name>.<tailnet>.ts.net/` across undeploys, until it releases the name, so its public traffic (webhooks, OAuth callbacks) can't move to someone else. **Fix:** deploy under another workload name, or ask the operator to have the owning tenant release the name (`tw expose release <name> --tenant <owner>`; `tw expose names --tenant <owner>` lists its names) ([public-ingress](public-ingress.md#name-ownership)).

### public_unreachable
1 · cli. **When:** `tw deploy --wait-public` waited for the workload's public URL and it didn't answer from the public internet in time. The deploy itself succeeded. **Fix:** a new public name's certificate and DNS can take minutes; retry with a longer `--wait-public`, check the tailnet has HTTPS certificates on, and see [docs/public-ingress.md](public-ingress.md).

### push_unauthorized
403 · hostd. **When:** something other than mgmt's node pushed to hostd's push listener (port 7474). **Fix:** only `tag:tw-mgmt` may push; check the grant and who is on that tag.

### python_unavailable
503 · hostd. **When:** a Python Worker's deployment, when the host can't provide the pinned Pyodide bundles: fetching one failed (an offline host, or no route to `pyodide-capnp-bin.edgeworker.net`), or a fetched bundle's SHA-256 didn't match its pin. **Fix:** give the host internet access for the first Python deployment, or copy the bundles into its `-pyodide-dir` (default `<data-dir>/pyodide`), see [python.md](python.md#offline-hosts); a digest mismatch means the download was corrupted or the file changed upstream: retry, and report it if it persists.

### r2_backend_unavailable
503 · hostd. **When:** the host's R2 backend (its `-r2-backend` directory or S3-compatible service) failed a read, write or delete of an R2 object's bytes. In app code it surfaces as an R2 error (code 10001) whose message starts `tw r2: r2_backend_unavailable:`. **Fix:** the host's operator checks the backend (`tw logs --component hostd`): disk space, the S3 endpoint, its credentials. Retry the call.

### r2_blob_missing
500 · hostd. **When:** a bucket's index names an object whose bytes are not in this host's R2 backend: the workload moved from another host and the backend is `fs` (bytes stay on the old host's disk), or the backend lost them. Surfaces as an R2 error (code 10001) whose message starts `tw r2: r2_blob_missing:`. **Fix:** run hosts that share workloads on one S3-compatible backend (`-r2-backend s3`, [r2.md](r2.md)); write the object again.

### r2_too_large
400 · hostd. **When:** an R2 object or part over 5 GiB − 5 MiB. Surfaces as R2's "exceeds the maximum allowed object size" (code 100100). **Fix:** use a multipart upload, parts up to that size each.

### rate_limited
429 · mgmt. **When:** a tailnet caller of the mgmt API is over its request rate (each WhoIs node has its own token bucket; hosts get a larger one), or mgmt is already serving its maximum of concurrent requests from non-host callers. `Retry-After` says when to retry. mgmt's own node and the local socket are never limited. **Fix:** retry after `Retry-After` and slow the client down; the operator raises or turns off the limits with `tw-mgmt -api-rps`, `-api-host-rps`, `-api-inflight` ([limits.md](limits.md#management-api)).

### release_bad_signature
1 · cli. **When:** `tw upgrade` on a `tw up` machine, or `tw upgrade import <version>`, refused a release because its `SHA256SUMS.sig` doesn't verify: it is signed by a key that isn't pinned in this `tw` (or in `TW_RELEASE_SIGNERS`), a file changed after signing, a file isn't in the signed `SHA256SUMS`, or this `tw` build pins no release key. Nothing in the release ran. **Fix:** download the release again from GitHub (`tw upgrade [<version>]`); check it by hand as in [INSTALL.md](INSTALL.md), "Verify a release". For a build you made yourself, add `--allow-unsigned` (`tw upgrade import` has none: import a build of your own by its tarballs, `tw upgrade import <tarball>…`).

### release_unsigned
1 · cli. **When:** `tw upgrade` on a `tw up` machine refused a release (a download or `--from`) that has no `SHA256SUMS.sig` or no `SHA256SUMS` inside it; or `tw upgrade import <version>` found no `SHA256SUMS.sig` in the release. Nothing in the release ran. **Fix:** upgrade to a published release (`tw upgrade [<version>]`). For a build you made yourself (`make release`), add `--allow-unsigned` (or set `TW_ALLOW_UNSIGNED=1`); for a fleet, import its tarballs: `tw upgrade import <tarball>…`.

### remote_unsupported
400 · cli. **When:** a command that runs only on the host that runs `tw up` (`tw logs --component`, `tw tenant add`, `tw backup`, `tw restore`, `tw upgrade rollback`) was run on a linked developer machine. **Fix:** run it on the host. A workload's log works from a linked machine: `tw logs <workload>`.

### queue_limit
400 · mgmt, hostd. **When:** a queue send is too big: a message over 128 KB, a `sendBatch` over 100 messages or 256 KB, a `delaySeconds` over 43200, or a queue over 1 GiB of kept messages. In a Worker, `send()` throws `Error: [queue_limit] … (400)`. **Fix:** send smaller messages (store large payloads elsewhere and send a reference), or let consumers drain the queue.

### queue_unavailable
503 · hostd. **When:** a queue producer's send (or `metrics()`) couldn't reach mgmt through hostd. `send()` throws `Error: [queue_unavailable] … (503)`; nothing was enqueued. **Fix:** retry; check `tw status` for mgmt and the host.

### replica_conflict
409 · mgmt, cli. **When:** a deploy names a workload in a way that clashes with replica names (`tailworks.jsonc` `"replicas"`: replica *n* of `web` is the workload `web--r<n>`). One of: the name is a live replica (`web--r2`); a replica the deploy would create has the name of a separate workload; or a workload that is not a replica is being given a name ending in `--r<n>` with n ≥ 2 (also as a preview: `--preview-alias pr7--r2`). Those names belong to replicas: a replica shares its workload's stores, secrets and R2 objects, and tailworks tells one by its name. **Fix:** deploy the workload itself (`web`), not its replica; rename the workload or pick another `--preview-alias`; or undeploy the workload that holds the replica's name. A workload deployed under such a name by a release before this rule keeps deploying until it is undeployed; rename it when you can ([services.md](services.md#replicas-one-stateless-workload-on-several-hosts)).

### reserved_tag
400 · mgmt. **When:** a bundle or namespace asks for one of tailworks' own tags (`tag:tw`, `tag:tw-mgmt`, `tag:tw-host`). **Fix:** use your app's tags (`tag:tw-wl-<name>`, class tags) in `tailworks.jsonc`.

### restore_failed
500 · cli. **When:** `tw restore` could not save the current state first, copy the backup in, or run the restore fence. **Fix:** the message names the step. If the copy failed, `tw restore <the before-restore backup it printed> --yes` puts the old state back; then fix the cause and restore again.

### rpc_not_forwardable
Thrown in the Worker · platform. **When:** a service binding RPC call (`env.<binding>.method(...)`) passed, or the method returned, a value Cloudflare passes by reference: a function, an `RpcTarget` or RPC stub, a `ReadableStream`/`WritableStream`, a `Request`, `Response` or `Headers`, or a value with a cycle. On tailworks the call crosses the tailnet, so only structured-clone values can. **Fix:** pass plain data; send a body with `env.<binding>.fetch()` instead of a stream ([wrangler.md](wrangler.md#service-bindings)).

### sandbox_layout_unsafe
409 · hostd, cli. **When:** on Linux with the Landlock sandbox, tailworks' state dir, run dir, a tenant's credential file (`--oauth-client-file`, `--api-key-file`), a `-sandbox-deny` path or the home dir resolves (after symlinks) under a directory every sandboxed workload may read: `/usr`, `/lib`, `/lib64`, `/lib32`, `/bin`, `/sbin`, `/etc`, `/opt`, `/nix/store` or `/run/current-system`. Landlock has no deny rules, so hostd's deny list can't protect a path there: a Node app, or workerd after a V8 escape, could read the custody key, node keys or the mint credential. `tw up`, `tw tenant add|set`, `tw-hostd` at start and the sandbox launcher refuse the layout; `tw doctor` reports it under `sandbox`. **Fix:** keep the state dir (`~/.tailworks`, or `tw up --state-dir /var/lib/tailworks`) and every credential file (for example `~/.config/tailworks/oauth.env`) outside those trees, and give the tailworks user a home dir outside them; or deploy with `"tier": "microvm"`. See [security-model.md](security-model.md#what-the-process-tier-sandbox-confines).

### sandbox_unavailable
409 · hostd, cli. **When:** a second tenant's process-tier workload on a host that must not be shared: no process sandbox (Linux without Landlock, or `-sandbox none`), or the tailworks user can reach a container engine socket, is in the docker/podman/lxd/incus group, or can reach the system tailscaled while a workload could use it. **Fix:** run tailworks as a dedicated user without those sockets and groups, enable Landlock, use TCP listen mode for Node apps, deploy with `"tier": "microvm"`; or (tenants NOT isolated) `tw up --insecure-shared-host`.

### service_conflict
409 · cli. **When:** the systemd unit / LaunchAgent already runs tailworks for another state dir. **Fix:** `tw down --service --state-dir <that dir>`, then `tw up --service` again.

### service_failed
500 · cli. **When:** `systemctl --user` or `launchctl` failed while installing, starting or removing the service. **Fix:** the detail has the command and its output; `systemctl --user status tw` / `launchctl print gui/$(id -u)`.

### service_not_found
404 · hostd, mgmt, platform. **When:** a service binding's target doesn't exist: the deployment declares no such binding, no workload of that name is deployed in the tenant (a binding can't name another tenant's workload), its entry node failed, or its main module doesn't export the binding's `entrypoint`. **Fix:** deploy the target workload in the same tenant (`tw ls`), or fix `services[].service` / `entrypoint` in `wrangler.jsonc` and redeploy.

### service_running
409 · cli. **When:** tailworks runs as a service (installed by `tw up --service`) and you ran a plain `tw up`/`tw down` that would fight it. **Fix:** `tw down --service`, then `tw up` again; or manage it with `systemctl --user`.

### service_unsupported
1 · cli. **When:** `--service` needs systemd (Linux, with a reachable user manager) or launchd (macOS) and neither applies. **Fix:** run `tw up` without `--service`, or fix the user session (`loginctl enable-linger`).

### shared_owner_tag
409 · mgmt, cli. **When:** a tenant was created (or its owner tag changed) on a tailnet where another tenant uses `tag:tw` as its owner tag, or the same owner tag: either tenant's OAuth client could mint the other's tags. **Fix:** give each tenant on the tailnet its own owner tag: `tw tenant set <existing> --owner-tag tag:tw-t-<team> --oauth-client-file <a client scoped to it>`, then `tw tenant add … --owner-tag …` ([teams.md](teams.md)).

### socket_limit
429 · hostd. **When:** an object (or, for the Worker and plain-DO objects, the workload node) already has 256 open `connect()` sockets on this host; the new one is refused (stock `connect()` sees a lost connection, `ctx.tailnet.connect()` this code). **Fix:** close sockets you're done with, and reuse connections: a driver pool with a small `max`, or one connection kept in a Durable Object ([limits.md](limits.md)).

### spawn_budget_exceeded
429 · mgmt, netd (SDK). **When:** too many live descendants (`maxLive`) or too deep a chain (`maxDepth`) under `tailnet.spawn` in `tailworks.jsonc`. **Fix:** kill finished sub-agents (`ttl` helps). The budget's ceiling is 64 live / depth 3 and cannot be raised; if `tailworks.jsonc` lowered it, raise it back toward that.

### state_conflict
409 · cli. **When:** the state dir is bound to another mint mode, tailnet, `--local-dev` setting or mgmt hostname than this command asks for; the API key file already serves another tenant; mgmt already has this host ID bound to another node; `tw link` on a machine that runs tailworks itself; `tw custody migrate` on an install already on setec, or into a setec that holds another value under one of the install's names. **Fix:** the detail names the binding; use another `--state-dir`, or `tw down --purge` and start over.

### stop_timeout
500 · cli. **When:** `tw down` waited 3 min for the supervisor to stop and killed it. **Fix:** `tw status`; `tw logs --component supervisor`.

### storage_quota_exceeded
429 · mgmt, hostd. **When:** a write would take the tenant past one of its storage quotas (`missing: {quota, limit, used, requested}`): a KV, D1 or R2 write that grows its store past `maxStoreBytes` (the journals of all the tenant's stores together), a queue send past `maxQueueBytes` (all its queued messages), or a new store past `maxStores`. A write that doesn't grow its store (an overwrite with something no bigger, a delete) is never refused, so a full tenant can free space. The quotas are unlimited unless an operator set them. **Fix:** delete data the tenant no longer needs, consume or purge its queues (`tw queues purge`), or raise the quota: `tw tenant set <tenant> --max-store-bytes 20GiB` (`--max-queue-bytes`, `--max-stores`; `-` = unlimited). `tw tenant ls` shows each tenant's usage.

### store_conflict
409 · mgmt. **When:** a platform store journal append named a head (`after`) or incarnation that is no longer current: another writer (a replica, an admin) appended first, or the store was deleted. **Fix:** nothing for app code: the store object catches up and retries on its own. A client of the REST API reads the journal from its `after` and retries.

### store_degraded
409 · hostd, mgmt. **When:** a platform store's object couldn't apply a journal entry while catching up (a D1 statement that fails on replay, which only non-deterministic SQL can do; an entry written by a newer tailworks; a corrupt entry). It stopped there instead of skipping the entry: it serves reads from what it applied before it, refuses writes, and tries the entry again on later syncs. A Worker's D1 call fails with `D1_ERROR: [store_degraded] …` naming the entry and SQLite's error. The host reports it to mgmt: `tw d1 ls` (`tw kv namespace ls`, `tw r2 bucket ls`), `tw status` and the console show the failing entry, and it is audited (`store_replay_failed`). **Fix:** an entry from a newer tailworks: upgrade tw-hostd. Otherwise, for D1, `tw d1 export <db> --output backup.sql` (it exports what applied, headed by a comment naming the failing entry), then either skip the entry with `tw d1 repair <db> --skip <seq> --yes` (the writes after it are kept), or rebuild: `tw d1 rm <db>`, `tw d1 create <db>` and `tw d1 execute <db> --file backup.sql` ([d1.md](d1.md), "Recovery").

### store_not_degraded
409 · mgmt. **When:** `tw d1 repair --skip <seq>` (`POST …/stores/{kind}/{name}/repair`) named an entry mgmt doesn't record the store as degraded at: the store isn't degraded, it healed, or it is degraded at another entry. A repair deletes exactly the entry a host reported it can't apply, nothing else. **Fix:** `tw d1 ls` shows the entry a degraded database stopped at; use that seq. If the host's report hasn't reached mgmt yet, a write to the database (or a read after a second) reports it again.

### store_limit
400 · mgmt, hostd. **When:** a platform store write is too big: an entry over 2 MiB, an append over 4 MiB or 128 entries, a compaction key over 1 KiB, or a store over 1 GiB. **Fix:** write less at once, or delete data you no longer need.

### store_unavailable
503 · hostd, mgmt. **When:** a platform store couldn't reach mgmt's journal (through hostd). Writes fail; reads answer from the host's copy (after waiting at most 2 s for mgmt) if the store has caught up with the journal on this host before, and fail if it never has (a new host) or if they need the journal (a D1 `first-primary` session or a bookmark ahead of the host's copy). hostd also returns it when mgmt doesn't answer a journal read within 10 s. From the mgmt API it also means its database is unreachable. **Fix:** retry; check `tw status` for mgmt and the host.

### superseded
409 · mgmt. **When:** undeploy of a deployment that is not the latest for its workload. **Fix:** `tw undeploy <workload>` acts on the latest.

### supervisor_failed
500 · cli. **When:** the supervisor exited at start, or the systemd unit started but no supervisor came up. **Fix:** `tw logs --component supervisor`, then `tw up`.

### systemd_failed
500 · cli. **When:** `systemctl` failed during `tw host setup`. **Fix:** `journalctl -u tw-vmhelper`.

### tag_not_owned
400 · mgmt, cli. **When:** a tenant with its own owner tag deployed or spawned with a tag that, in the tailnet policy's `tagOwners`, its owner tag doesn't own, or another tenant's owner tag on the same tailnet also owns; `tw policy` lists them as `tagOwnerProblems`. **Fix:** own the tag by the tenant's owner tag only (the printed `snippet`), or use a tag of your own team's ([teams.md](teams.md)).

### tailnet_https_unavailable
412 · hostd, netd. **When:** a deploy of an app with tailworks.jsonc `"tailnet": {"https": true}` ([tailworks-jsonc.md](tailworks-jsonc.md#tailnethttps)), and the tailnet issues no HTTPS certificates to the workload's node: HTTPS Certificates (or MagicDNS) is off for the tailnet, or the control server is Headscale, which issues none. `tw deploy` shows it as the detail of `deploy_failed`; the deployment that was active keeps serving. **Fix:** turn on MagicDNS and HTTPS Certificates in the admin console (DNS page) and deploy again, or remove `tailnet.https` and serve `http://` only.

### tailnet_required
400 · cli. **When:** the tailnet could not be determined from the OAuth client (it can't list devices, and mgmt's node hasn't joined yet), or the Headscale profile was used without `--tailnet` (its `dns.base_domain`). Tailworks never bootstraps from the machine's own Tailscale login. **Fix:** `tw up --tailnet <name> …`.

### tenant_forbidden
403 · mgmt, cli. **When:** a tenant admin (`github.com/rosskukulinski/tailworks/cap/tenant-admin`) asked for a tenant or action that no single grant value names, called an admin-only route (hosts, tenant settings, releases, upgrades, status), or holds the capability but every value is invalid (missing `tenants` or `actions`, a wildcard, an unknown action or field). **Fix:** add the printed grant (`snippet`) to the tailnet policy, or ask an admin; `tw whoami` lists what your values allow and why any grant nothing ([teams.md](teams.md)).

### tenant_has_live_objects
409 · mgmt, cli. **When:** `tw tenant rm` of a tenant with live objects. **Fix:** `tw tenant rm <tenant> --force` (kills and purges first).

### types_stale
1 · cli. **When:** `tw types --check` found the app's `worker-configuration.d.ts` (or `--out`) missing, or different from what `tw types` writes for the current wrangler config, tailworks.jsonc and secret names. **Fix:** run `tw types` and commit the file.

### unhealthy
503 · cli. **When:** `tw status` finds tailworks not healthy: mgmt not answering, or no live host. **Fix:** `tw logs --component mgmt|hostd`.

### uninstall_blocked
409 · cli. **When:** `tw uninstall` refused before changing anything: the microVM tier's helper is installed (`/etc/tailworks/setup.json`; undoing it needs root and this `tw` binary); tailworks is set up in the state dir but mgmt isn't running or healthy (the purge needs it); other hosts are registered with this mgmt (they would be orphaned); or the state dir is your home directory or contains it. **Fix:** the one it names: `sudo <prefix>/bin/tw host setup --undo`; `tw up` (or `tw uninstall --force`, then delete the objects' devices in the admin console › Machines); `tw host drain <host>` and `tw host rm <host>` for each other host; `--state-dir` naming tailworks' own state dir. Then `tw uninstall` again ([INSTALL.md](INSTALL.md#uninstall)).

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
404 · cli, mgmt. **When:** `tw logs`, `tw undeploy` or `tw host vmcheck` named a workload with no deployment (`tw ls <workload>`: no deployment and no objects either); `tw d1 execute|export|migrations` named a workload that isn't running (a D1 database is reached through the running deployment that binds it). **Fix:** `tw ls`; `tw deploy`.

### unsupported
1 · cli. **When:** `tw host setup` on an OS without the microVM tier (it needs Linux with KVM). **Fix:** nothing to set up; every workload runs in the process tier here.

### update_check_failed
1 · cli. **When:** `tw version --check` could not find the latest release: GitHub was unreachable, answered with an error (rate limit), or the repository has no published release. **Fix:** retry later, or look at the releases page; `TW_REPO` names another repository, as for `get.sh`.

### upgrade_aborted
409 · cli. **When:** `tw upgrade fleet` was following a fleet upgrade run that was aborted (`tw upgrade fleet abort`). **Fix:** `tw upgrade fleet status <run>` shows where it stopped; `tw upgrade fleet --plan`, then `tw upgrade fleet` to start a new run.


### upgrade_blocked
409 · mgmt, cli. **When:** `tw upgrade fleet` can't start a fleet upgrade run because its plan has blockers: a release missing from the catalog, a host's tarball not imported, parts of the fleet more than one release line behind, or an intermediate release with contract migrations. **Fix:** `tw upgrade fleet --plan` prints each blocker and its fix (a missing release or tarball: `tw upgrade import <version>`; [upgrading.md](upgrading.md#a-fleet-tw-mgmt-replicas-and-remote-hosts)).

### upgrade_failed
1 · cli. **When:** `tw upgrade` on a `tw up` machine failed at a step the message names: the store backup, `tw down`, `install.sh` (the old release is still installed), or the health check of the new release. Or `tw upgrade rollback` failed at a step: the copy of the current state it keeps first, the restore, `install.sh --rollback`, or the health check of the previous release. **Fix:** if the message says it rolled back (the old release runs with the store restored), `tw logs --component mgmt` shows why the new release failed. If it did not roll back (`--no-rollback`, or a contract migration ran): `tw logs --component mgmt` and `tw doctor`; to go back, `tw upgrade rollback <backup> --yes` with the backup the message names ([upgrading.md](upgrading.md#roll-back)). If the automatic rollback failed too, the message says which applies: `tw upgrade rollback <backup> --yes` while the failed release is still installed, else `tw down`, `tw restore <backup> --yes`, `tw up`. A failed `tw upgrade rollback` names the state it kept first: `tw restore <it> --yes` puts that back.

### upgrade_paused
409 · cli. **When:** `tw upgrade fleet` was following a fleet upgrade run that paused: a step failed (the reason is in the message) or someone ran `tw upgrade fleet pause`. **Fix:** fix the cause, then `tw upgrade fleet resume <run>`, or `tw upgrade fleet abort <run>`.


### upgrade_state
409 · mgmt, cli. **When:** `tw upgrade fleet pause|resume|abort` on a run that isn't running (pause), paused (resume) or active (abort). **Fix:** `tw upgrade fleet status` shows the run's state.

### upstream_unavailable
503 · netd, hostd. **When:** the platform upstream (workerd or node behind netd) is not answering; or the host has had no successful heartbeat to mgmt within the fence window (30 s) after a stall or a cut-off, so a workload with Durable Objects refuses requests and egress until mgmt confirms its lease ([locks.md](locks.md)). **Fix:** it is restarting or crashed; `tw logs <workload>`. After a stall, retry: the host serves again after its next heartbeat, or the workload has moved to another host.

### usermod_failed
1 · cli. **When:** `tw host setup --microvm` could not add the user to the `kvm` group. **Fix:** `sudo usermod -aG kvm <user>` and log in again.

### version_skew
409 · mgmt, cli. **When:** two tailworks components are outside the supported version window: a host newer than tw-mgmt, or more than one release line behind it; a CLI more than one line behind mgmt (in `--json` mode); mgmt replicas more than one line apart; a tw-mgmt older than the store's minimum (a newer tw-mgmt ran a contract migration); or an R2 bucket whose index has entries written by a newer tailworks (`tw r2 object ls`). **Fix:** upgrade in order, mgmt first, then hosts ([upgrading.md](upgrading.md)); a store contracted by a newer tw-mgmt needs that version or newer, or the pre-upgrade backup restored.

### workerd_broken
1 · cli. **When:** `tw up` found workerd, but `workerd --version` does not run (a missing glibc on Linux; an unsigned or quarantined binary on macOS). `tw up` checks this before it mints mgmt's node key, since the host agent could not start any workload. **Fix:** the fix line names the cause; `tw doctor` runs the full check. Reinstall tailworks (`install.sh`), or point `--workerd <path>` / `$TW_WORKERD` at a workerd that runs.

### wrangler_required
1 · cli. **When:** `tw deploy --wrangler` / `tw dev --wrangler` found no wrangler CLI (`$TW_WRANGLER`, the project's `node_modules/.bin/wrangler`, or `wrangler` on PATH). **Fix:** `npm install --save-dev wrangler`, or drop `--wrangler` to bundle with tailworks' built-in esbuild ([wrangler.md](wrangler.md)).
