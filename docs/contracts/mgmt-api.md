<!-- GENERATED from api/openapi.yaml by `make api` (api/render). Do not edit; edit the spec. -->

# tailworks management API (1.0.0-pre)

_tw-mgmt ⇄ tw-hostd ⇄ tailworks CLI contract (/v1)_

This document is the **single source of truth** for `/v1`. Go server
interfaces and types (`mgmt/api/v1`), the hostd/CLI client
(`hostd/mgmtapi/v1`) and `docs/contracts/mgmt-api.md` are generated from
it (`make api`); none of them is hand-edited. `/v0` is gone: tailworks is
pre-release and `/v1` is a breaking replacement with no compatibility shim.

## Transport and authentication (tsnet-only in normal operation)

mgmt serves the API on **its own tsnet node** `tw-mgmt` (`tag:tw-mgmt`),
on `:80` and, when the tailnet has HTTPS, on `:443` with the node's
certificate (tailnet-only). mgmt serves nothing over Funnel.

Every tailnet request is authenticated by **Tailscale WhoIs on the
connection**, never by a bearer token or API key:

- **host** — the peer node carries `tag:tw-host`. hostd runs its own
  tsnet node `tw-host-<hostID>`; the node is bound to the `hostID` it
  registered with (`PUT /v1/hosts/{id}`), and may only act on that host's
  records and on the namespaces it holds a lease for.
- **admin** — the peer's WhoIs `CapMap` holds the app capability
  `github.com/rosskukulinski/tailworks/cap/admin` toward `tag:tw-mgmt` (granted by the base policy
  snippet to `autogroup:admin`; tenants may grant it to users, groups or
  tags such as a CI node). `tw-mgmt -admins <logins>` is an override.
- **tenant admin** — the peer holds `github.com/rosskukulinski/tailworks/cap/tenant-admin` (and not
  `github.com/rosskukulinski/tailworks/cap/admin`). Each value names tenants and actions:
  `{"tenants": ["payments"], "actions": ["read", "deploy"]}`, both
  required. The actions are `read`, `deploy`, `kill`, `secrets` and
  `data`. An operation is open to tenant admins only if its `x-tw-role`
  has a `tenant:<action>` part, and only for a tenant that one single
  value names with that action. Lists are limited to those tenants, and
  another tenant's deployment or object is `404`. Hosts, releases,
  upgrades and tenant settings stay with admins
  (ADR-043).
- everyone else: `403 not_admin` with the grant `snippet` to add.

Each operation declares its role in `x-tw-role` (`admin`, `host`,
`admin|host`, `any`, plus `tenant:<action>` where tenant admins may call
it). The security scheme `tailnetWhoIs` documents this;
it is **not** a token scheme and no header is sent.

The unix socket + host token (RT-03) survives **only** behind
`--local-dev` (`tw dev`, hermetic tests). It is the `localHostToken`
scheme; `tw doctor` reports `transport: tsnet` in normal operation and
flags any local-socket use.

mgmt → hostd pushes (kill, assignments) go to the host's tsnet node and are
hostd's API, not this document's.

## Conventions

- JSON everywhere. Timestamps are unix **milliseconds** unless the field
  says otherwise (`AuditRecord.ts` is RFC 3339).
- **Pagination.** Every list returns `{"items": [...], "nextCursor":
  "..." | null}`. `?limit=` defaults to 50; 1–500 is accepted and anything
  else is `400 bad_request`. `?cursor=` is opaque; it encodes the
  stable sort key `(createdAt, id)` (audit: `(ts, chain, seq)`), so a page
  is stable under concurrent inserts. A cursor that does not decode is
  `400 bad_cursor`. Filters are query parameters and must be repeated
  unchanged with the cursor.
- **ETags.** Every single-resource `GET` sets a strong `ETag`
  (`"<sha256-prefix of the canonical JSON>"`). `PATCH`/`DELETE` accept
  `If-Match`; a stale value is `412 precondition_failed`. `If-None-Match`
  on `GET` answers `304`.
- **PATCH** bodies are partial JSON following **JSON Merge Patch (RFC
  7396)**: absent keys are untouched, `null` clears a field. Media type
  `application/merge-patch+json` (plain `application/json` is accepted).
- **DELETE** takes options as query parameters, never a body.
- **Async creates** answer `202 Accepted` with a `Location` header to poll.
- **Tombstones.** A killed object stays readable as `410 Gone` whose body
  is the typed error plus the kill record (`TombstoneError`).
- **Errors** are the typed error catalog (docs/errors.md) shape
  `{code, message, missing?, snippet?, doc_url, trace_id}` and also set
  `Tw-Error: <code>`; `identity_pending` sets `Retry-After: 1`.
- **Body limits.** Requests from the `host` role are capped at **1 MiB**
  except `POST /v1/audit` (batches; 64 MiB). Admin uploads (`POST
  /v1/deployments` bundles) keep the 64 MiB limit. Over the cap is
  `400 bad_request`.
- **Audit.** Every mutating operation is marked `x-tw-audit: true` and
  writes to mgmt's hash chain with the caller's actor (`deployedBy`,
  `by`): the WhoIs login for users, `<tag> (<stableNodeID>)` for tagged
  admin nodes, `host:<id>` for hosts, `Tw-Actor` or `local` on the
  local-dev socket.

## Decisions recorded here

- **Preflight is `POST /v1/deployments?dryRun=true`** (decided by Ross,
  2026-11-08): the request body is exactly a real deploy's; mgmt parses
  `tailworks.jsonc` from the bundle as a deploy would (`tailnet.tags`,
  `tailnet.objects.*.tags` → `srcTags`; `tailnet.connects`), checks grants
  and tagOwners against the policy cache and answers `200 PreflightResult`
  (missing grants with HuJSON snippets). Nothing is created, no lease is
  taken, and the only audit record is an optional `kind: preflight`.
  There is no separate preflight resource.
- **Audit verification is `GET /v1/audit/verification`**: the noun form.
  It is a read-only report, so a resource name fits, and it needs no
  custom-method (`:verb`) syntax that routers, generators and linters
  treat specially. No reason was found to prefer `:verify`.
- **Credentials and key grants are `POST`** (`…/credentials`,
  `…/keys`): each call *creates* a single-use grant (a fresh auth key, a
  fenced key release) and is audited, so a `GET` would be wrong.
- The only other RPC-shaped operation is a diagnostic that produces a
  report: `POST /v1/device-delete-runs` (execute the scheduled device
  deletes now). It is documented as such.

## Route table

| Method | Path | Role | Audited | Operation | Summary |
|---|---|---|---|---|---|
| `GET` | `/v1/audit` | admin\|tenant:read | no | `ListAudit` | Query audit records |
| `POST` | `/v1/audit` | host | yes | `AppendAudit` | Append a batch of host audit records |
| `GET` | `/v1/audit/verification` | admin | no | `GetAuditVerification` | Verify every audit chain |
| `GET` | `/v1/certificates/{stableNodeID}` | host\|admin | no | `GetCertificate` | Look up a node certificate by stable node ID |
| `GET` | `/v1/container-reservations` | admin\|host | no | `ListContainerReservations` | List container reservations |
| `POST` | `/v1/container-reservations` | host | yes | `CreateContainerReservation` | Reserve quota for a container before it starts |
| `DELETE` | `/v1/container-reservations/{id}` | host\|admin | yes | `DeleteContainerReservation` | Release a container reservation (the container stopped) |
| `GET` | `/v1/deployments` | admin\|tenant:read | no | `ListDeployments` | List deployments |
| `POST` | `/v1/deployments` | admin\|tenant:deploy | yes | `CreateDeployment` | Deploy a workload (async), or preflight it with dryRun |
| `GET` | `/v1/deployments/{id}` | admin\|host\|tenant:read | no | `GetDeployment` | Read a deployment and its status |
| `DELETE` | `/v1/deployments/{id}` | admin\|tenant:deploy | yes | `DeleteDeployment` | Undeploy |
| `GET` | `/v1/deployments/{id}/bundle` | host\|admin\|tenant:read | no | `GetDeploymentBundle` | Download the raw bundle |
| `POST` | `/v1/device-delete-runs` | admin | yes | `CreateDeviceDeleteRun` | Run the scheduled device deletes now |
| `GET` | `/v1/healthz` | any | no | `GetHealthz` | Liveness |
| `GET` | `/v1/hosts` | admin | no | `ListHosts` | List hosts |
| `POST` | `/v1/hosts` | admin | yes | `CreateHost` | Add a host and mint its single-use join key |
| `GET` | `/v1/hosts/{id}` | admin\|host | no | `GetHost` | Read a host |
| `PUT` | `/v1/hosts/{id}` | host | yes | `RegisterHost` | Register or re-register a host (idempotent) |
| `DELETE` | `/v1/hosts/{id}` | admin | yes | `DeleteHost` | Drain and remove a host |
| `GET` | `/v1/hosts/{id}/assignments` | host\|admin | no | `ListHostAssignments` | List the deployments assigned to a host |
| `PUT` | `/v1/hosts/{id}/cordon` | admin | yes | `CordonHost` | Cordon a host (no new placements) |
| `DELETE` | `/v1/hosts/{id}/cordon` | admin | yes | `UncordonHost` | Uncordon a host |
| `POST` | `/v1/hosts/{id}/drain` | admin | yes | `DrainHost` | Cordon a host and move its workloads to other hosts |
| `PUT` | `/v1/hosts/{id}/paths` | host | no | `ReportHostPaths` | Report measured tailnet paths (smart placement) |
| `GET` | `/v1/hosts/{id}/sandbox-deny` | host\|admin | no | `GetHostSandboxDeny` | Every tenant's credential file paths, for the host's sandbox deny list |
| `PUT` | `/v1/hosts/{id}/status` | host | no | `ReportHostStatus` | Heartbeat (replace the host's reported status) |
| `GET` | `/v1/images/aliases` | admin\|host\|tenant:read | no | `ListImageAliases` | The image alias map |
| `GET` | `/v1/images/public` | admin\|host\|tenant:read | no | `ListPublicImages` | List public base images |
| `POST` | `/v1/images/public` | admin | yes | `CreatePublicImage` | Mark an image as a public base (mirrors it now) |
| `DELETE` | `/v1/images/public/{id}` | admin | yes | `DeletePublicImage` | Unmark a public base image |
| `GET` | `/v1/objects` | admin\|host\|tenant:read | no | `ListObjects` | List objects (with lineage) |
| `POST` | `/v1/objects` | host | yes | `CreateObject` | Create an object (async mint) |
| `GET` | `/v1/objects/{id}` | admin\|host\|tenant:read | no | `GetObject` | Read an object (lineage, children, kill record) |
| `PATCH` | `/v1/objects/{id}` | host\|admin\|tenant:kill | yes | `UpdateObject` | Update an object (JSON Merge Patch) |
| `DELETE` | `/v1/objects/{id}` | admin\|host\|tenant:kill | yes | `DeleteObject` | Kill an object |
| `GET` | `/v1/objects/{id}/certificate` | host\|admin | no | `GetObjectCertificate` | Read the object's node certificate |
| `POST` | `/v1/objects/{id}/credentials` | host | yes | `CreateObjectCredential` | Mint a single-use auth key for the object's node |
| `GET` | `/v1/releases` | admin | no | `ListReleases` | The release catalog |
| `POST` | `/v1/releases` | admin | yes | `ImportReleases` | Import catalog entries (from a release's share/tailworks/catalog.json) |
| `GET` | `/v1/releases/{version}/artifacts/{name}` | admin\|host | no | `GetReleaseArtifact` | Download a release tarball (hosts upgrading themselves) |
| `PUT` | `/v1/releases/{version}/artifacts/{name}` | admin | yes | `PutReleaseArtifact` | Upload a release tarball (or its SHA256SUMS) |
| `GET` | `/v1/status` | admin | no | `GetStatus` | mgmt status |
| `GET` | `/v1/tenants` | admin\|host\|tenant:read | no | `ListTenants` | List tenants |
| `POST` | `/v1/tenants` | admin | yes | `CreateTenant` | Create a tenant |
| `GET` | `/v1/tenants/{id}` | admin\|host\|tenant:read | no | `GetTenant` | Read a tenant |
| `PATCH` | `/v1/tenants/{id}` | admin | yes | `UpdateTenant` | Update a tenant (JSON Merge Patch) |
| `DELETE` | `/v1/tenants/{id}` | admin | yes | `DeleteTenant` | Delete a tenant |
| `GET` | `/v1/tenants/{id}/approvals` | admin\|approver | no | `ListApprovals` | The Workflow approvals waiting in a tenant (`tw approvals`, the admin UI) |
| `GET` | `/v1/tenants/{id}/notifications` | admin\|tenant:read | no | `GetTenantNotifications` | Read the tenant's notification webhook (`tw notify`), without secrets |
| `PUT` | `/v1/tenants/{id}/notifications` | admin\|tenant:deploy | yes | `PutTenantNotifications` | Set the tenant's notification webhook (`tw notify set`) |
| `DELETE` | `/v1/tenants/{id}/notifications` | admin\|tenant:deploy | yes | `DeleteTenantNotifications` | Stop the tenant's notifications (`tw notify rm`) |
| `POST` | `/v1/tenants/{id}/notifications/test` | admin\|tenant:deploy | yes | `TestTenantNotifications` | Send a test notification now (`tw notify test`) |
| `DELETE` | `/v1/tenants/{id}/objects` | admin | yes | `PurgeTenantObjects` | Purge terminal objects of a tenant |
| `GET` | `/v1/tenants/{id}/public-names` | admin\|tenant:read | no | `ListPublicNames` | List the public names a tenant owns |
| `DELETE` | `/v1/tenants/{id}/public-names/{name}` | admin\|tenant:deploy | yes | `ReleasePublicName` | Release a public name the tenant owns |
| `GET` | `/v1/tenants/{id}/queues` | admin\|tenant:read | no | `ListQueues` | List a tenant's queues |
| `GET` | `/v1/tenants/{id}/queues/{queue}` | admin\|host\|tenant:read | no | `GetQueue` | Get one queue and its backlog |
| `PUT` | `/v1/tenants/{id}/queues/{queue}` | admin\|tenant:data | yes | `PutQueue` | Create a queue (idempotent) |
| `DELETE` | `/v1/tenants/{id}/queues/{queue}` | admin\|tenant:data | yes | `DeleteQueue` | Delete a queue and its messages |
| `GET` | `/v1/tenants/{id}/queues/{queue}/messages` | admin\|tenant:data | no | `PeekQueueMessages` | Read messages without claiming them |
| `POST` | `/v1/tenants/{id}/queues/{queue}/messages` | admin\|host\|tenant:data | no | `SendQueueMessages` | Send messages to a queue |
| `POST` | `/v1/tenants/{id}/queues/{queue}/messages/ack` | admin\|host\|tenant:data | no | `AckQueueMessages` | Settle claimed messages (ack, retry, extend) |
| `POST` | `/v1/tenants/{id}/queues/{queue}/messages/pull` | admin\|host\|tenant:data | no | `PullQueueMessages` | Claim a batch of messages (push consumers and HTTP pull) |
| `POST` | `/v1/tenants/{id}/queues/{queue}/purge` | admin\|tenant:data | yes | `PurgeQueue` | Delete every message of a queue, keeping the queue |
| `GET` | `/v1/tenants/{id}/telemetry` | admin\|tenant:read | no | `GetTenantTelemetry` | Read the tenant's OTLP export (`tw telemetry`), without header values |
| `PUT` | `/v1/tenants/{id}/telemetry` | admin\|tenant:deploy | yes | `PutTenantTelemetry` | Set the tenant's OTLP export (`tw telemetry set`) |
| `DELETE` | `/v1/tenants/{id}/telemetry` | admin\|tenant:deploy | yes | `DeleteTenantTelemetry` | Stop the tenant's OTLP export (`tw telemetry rm`) |
| `POST` | `/v1/tenants/{id}/workloads/{workload}/approvals/{workflow}/{instance}` | admin\|approver | yes | `DecideApproval` | Approve or reject a waiting Workflow instance (`tw approve`, the admin UI) |
| `GET` | `/v1/tenants/{id}/workloads/{workload}/d1/{name}/export` | admin\|tenant:data | no | `ExportD1` | Export a D1 database as SQL (`tw d1 export`) |
| `POST` | `/v1/tenants/{id}/workloads/{workload}/d1/{name}/query` | admin\|tenant:data | yes | `QueryD1` | Run SQL on a D1 database (`tw d1 execute`, `tw d1 migrations apply`) |
| `GET` | `/v1/tenants/{id}/workloads/{workload}/deployments` | admin\|tenant:read | no | `ListWorkloadDeployments` | A workload's deployment history, newest first (`tw deployments`) |
| `GET` | `/v1/tenants/{id}/workloads/{workload}/logs` | admin\|tenant:read | no | `GetWorkloadLogs` | Read (or follow) a workload's workerd / node log (`tw logs` from a linked machine) |
| `GET` | `/v1/tenants/{id}/workloads/{workload}/previews` | admin\|tenant:read | no | `ListWorkloadPreviews` | A workload's running previews (`tw previews`) |
| `POST` | `/v1/tenants/{id}/workloads/{workload}/promotions` | admin\|tenant:deploy | yes | `CreatePromotion` | Promote a preview to the workload (`tw promote`), or preflight it with dryRun |
| `GET` | `/v1/tenants/{id}/workloads/{workload}/public` | admin\|tenant:read | no | `GetWorkloadPublic` | Read a workload's public-ingress exposure (`tw expose`) |
| `PATCH` | `/v1/tenants/{id}/workloads/{workload}/public` | admin\|tenant:deploy | yes | `UpdateWorkloadPublic` | Expose or unexpose a workload's public routes (JSON Merge Patch) |
| `POST` | `/v1/tenants/{id}/workloads/{workload}/rollbacks` | admin\|tenant:deploy | yes | `CreateRollback` | Roll a workload back to a stored bundle (`tw rollback`), or preflight it with dryRun |
| `POST` | `/v1/tenants/{id}/workloads/{workload}/secret-grants` | host | yes | `CreateWorkloadSecretGrant` | Release a workload's secret values to the lease holder |
| `GET` | `/v1/tenants/{id}/workloads/{workload}/secrets` | admin\|tenant:read | no | `ListWorkloadSecrets` | List a workload's secrets (names and times, never values) |
| `PUT` | `/v1/tenants/{id}/workloads/{workload}/secrets/{name}` | admin\|tenant:secrets | yes | `PutWorkloadSecret` | Create or replace a workload secret (`tw secret put`) |
| `DELETE` | `/v1/tenants/{id}/workloads/{workload}/secrets/{name}` | admin\|tenant:secrets | yes | `DeleteWorkloadSecret` | Delete a workload secret (`tw secret rm`) |
| `GET` | `/v1/tenants/{id}/workloads/{workload}/stores` | admin\|tenant:read | no | `ListWorkloadStores` | List a workload's stores |
| `GET` | `/v1/tenants/{id}/workloads/{workload}/stores/kv/{name}/keys` | admin\|tenant:data | no | `ListKVKeys` | List a KV namespace's keys |
| `GET` | `/v1/tenants/{id}/workloads/{workload}/stores/kv/{name}/value` | admin\|tenant:data | no | `GetKVValue` | Get one KV value |
| `PUT` | `/v1/tenants/{id}/workloads/{workload}/stores/kv/{name}/value` | admin\|tenant:data | yes | `PutKVValue` | Write one KV value |
| `DELETE` | `/v1/tenants/{id}/workloads/{workload}/stores/kv/{name}/value` | admin\|tenant:data | yes | `DeleteKVValue` | Delete one KV value |
| `GET` | `/v1/tenants/{id}/workloads/{workload}/stores/{kind}/{name}` | admin\|tenant:read | no | `GetWorkloadStore` | Get one store |
| `PUT` | `/v1/tenants/{id}/workloads/{workload}/stores/{kind}/{name}` | admin\|tenant:data | yes | `PutWorkloadStore` | Create a store (idempotent) |
| `DELETE` | `/v1/tenants/{id}/workloads/{workload}/stores/{kind}/{name}` | admin\|tenant:data | yes | `DeleteWorkloadStore` | Delete a store and its data |
| `GET` | `/v1/tenants/{id}/workloads/{workload}/stores/{kind}/{name}/journal` | admin\|host\|tenant:data | no | `ReadStoreJournal` | Read a store's journal after a sequence number |
| `POST` | `/v1/tenants/{id}/workloads/{workload}/stores/{kind}/{name}/journal` | admin\|host\|tenant:data | no | `AppendStoreJournal` | Append entries to a store's journal (conditional on its head) |
| `POST` | `/v1/tenants/{id}/workloads/{workload}/telemetry-grants` | host | yes | `CreateTelemetryGrant` | Release the tenant's OTLP export config to a workload's lease holder |
| `GET` | `/v1/upgrades` | admin | no | `ListUpgrades` | Upgrade runs, newest first |
| `POST` | `/v1/upgrades` | admin | yes | `CreateUpgrade` | Plan an upgrade, or start an upgrade run |
| `GET` | `/v1/upgrades/{id}` | admin | no | `GetUpgrade` | One upgrade run |
| `POST` | `/v1/upgrades/{id}/actions` | admin | yes | `ActOnUpgrade` | Pause, resume or abort an upgrade run |
| `POST` | `/v1/volumes/{id}/keys` | host | yes | `CreateVolumeKeyGrant` | Release a volume's dm-crypt data key to the lease holder |
| `GET` | `/v1/whoami` | any | no | `WhoAmI` | Who the caller is, as mgmt sees it |
| `GET` | `/mcp` | admin\|mcp | no | `MCPStream` | Not supported (stateless server; no server-to-client stream) |
| `POST` | `/mcp` | admin\|mcp | yes | `MCPRequest` | One MCP JSON-RPC request (initialize, ping, tools/list, tools/call) or notification |
| `GET` | `/v2/` | admin\|host\|tenant:read | no | `RegistryBase` | OCI API version check |
| `POST` | `/v2/{name}/blobs/uploads/` | admin\|tenant:deploy | no | `RegistryStartUpload` | Start a blob upload (or a monolithic upload, or a mount) |
| `GET` | `/v2/{name}/blobs/uploads/{uuid}` | admin\|tenant:deploy | no | `RegistryUploadStatus` | Upload progress |
| `PUT` | `/v2/{name}/blobs/uploads/{uuid}` | admin\|tenant:deploy | no | `RegistryFinishUpload` | Finish an upload (`?digest=` required; optional final chunk as the body) |
| `PATCH` | `/v2/{name}/blobs/uploads/{uuid}` | admin\|tenant:deploy | no | `RegistryUploadChunk` | Upload a chunk |
| `DELETE` | `/v2/{name}/blobs/uploads/{uuid}` | admin\|tenant:deploy | no | `RegistryCancelUpload` | Cancel an upload |
| `GET` | `/v2/{name}/blobs/{digest}` | admin\|host\|tenant:read | no | `RegistryGetBlob` | Pull a blob (Range supported) |
| `GET` | `/v2/{name}/manifests/{reference}` | admin\|host\|tenant:read | no | `RegistryGetManifest` | Pull a manifest or index (by tag or digest) |
| `PUT` | `/v2/{name}/manifests/{reference}` | admin\|tenant:deploy | yes | `RegistryPutManifest` | Push a manifest or index (by tag or digest) |
| `GET` | `/v2/{name}/tags/list` | admin\|host\|tenant:read | no | `RegistryListTags` | List a repository's tags (`?n=` / `?last=` paging) |

## Tenants

Customer tailnets. Admin only.

### `GET /v1/tenants` — List tenants

`ListTenants` · role **admin\|host\|tenant:read** · audited: no

Hosts may list tenants too (they deny each tenant's `apiKeyFile` and
`oauthClientFile` path to their workload sandboxes), but a host sees
**only the tenants with a deployment assigned to it** (a namespace
lease it holds); admins see every tenant.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `limit` | query | integer | no | Page size, 1–500 (default 50). Out-of-range values are `400 bad_request`. |
| `cursor` | query | string | no | Opaque page cursor from the previous page's `nextCursor`. Filters must be repeated unchanged. |

| Status | Body | Description |
|---|---|---|
| 200 | `TenantPage` | One page of tenants, ordered by `(createdAt, tenantID)`. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/tenants` — Create a tenant

`CreateTenant` · role **admin** · audited: yes

**Request** `application/json`: `TenantCreate`

| Status | Body | Description |
|---|---|---|
| 201 | `Tenant` | Created. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 409 | `Error` | The request conflicts with the resource's current state. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}` — Read a tenant

`GetTenant` · role **admin\|host\|tenant:read** · audited: no

Includes `oboPublicKey` (hostd pins it). A host may read only
tenants with a deployment assigned
to it (any other tenant is `404 not_found` for it).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `If-None-Match` | header | string | no | Answer `304` when the `ETag` still matches. |

| Status | Body | Description |
|---|---|---|
| 200 | `Tenant` | The tenant. |
| 304 | `Error` | The `If-None-Match` validator still matches. No body. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `PATCH /v1/tenants/{id}` — Update a tenant (JSON Merge Patch)

`UpdateTenant` · role **admin** · audited: yes

Updatable: `mintMode`, `apiKeyFile`, `oauthClientID`,
`oauthClientFile`, `registrationsPerMinute`, `maxObjects`, `pool`,
`tier`. Switching mode or changing a client ID / credential file drops
cached tokens. A tenant left in a removed mint mode (`apikey`, `wif`)
can't mint until it is switched: `{mintMode: oauth, oauthClientFile: …}`.
`tailnet` and `controlURL` are immutable (`400 immutable_field`).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `If-Match` | header | string | no | Only apply if the resource's current `ETag` matches; otherwise `412 precondition_failed`. |

**Request** `application/json` or `application/merge-patch+json`: `TenantPatch`

| Status | Body | Description |
|---|---|---|
| 200 | `Tenant` | The updated tenant. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 412 | `Error` | `If-Match` did not match the current `ETag`. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `DELETE /v1/tenants/{id}` — Delete a tenant

`DeleteTenant` · role **admin** · audited: yes

`409 tenant_has_live_objects` while any object of the tenant is live,
unless `force=true`: then every live object is killed (cascade, device
deletes through the tenant credential), killed/failed objects are
purged, deployments and leases are released, and the whole cascade is
audited (`kind: tenant_delete`, with the counts of what was removed).
Then everything the tenant owns is removed: its custody entries (store
and queue data keys, workload secrets, volume keys, auth keys,
notification and telemetry settings, OBO roots), its KV, D1 and R2
journals, queue messages, objects, deployments and the rest of its
rows, and the tenant row last. Every host removes the tenant's files
(`tenants/<id>/`, R2 blobs) at its next heartbeat (`purgeTenants`),
audited `tenant_purge_host`. Only the audit records stay. The delete
is idempotent: if it fails part way, run it again to finish.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `If-Match` | header | string | no | Only apply if the resource's current `ETag` matches; otherwise `412 precondition_failed`. |
| `force` | query | boolean | no | Cascade-kill live objects and purge before deleting. |

| Status | Body | Description |
|---|---|---|
| 202 | `TenantDeleteResult` | Accepted; the cascade runs. Poll `GET /v1/tenants/{id}` until `404`. |
| 204 | — | Deleted (nothing was live). |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | Live objects exist and `force` was not set. |
| 412 | `Error` | `If-Match` did not match the current `ETag`. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}/approvals` — The Workflow approvals waiting in a tenant (`tw approvals`, the admin UI)

`ListApprovals` · role **admin\|approver** · audited: no

A Workflow instance that waits for an event of type `approval` or
`approval-*` (the Agents SDK's `waitForApproval`) is listed here
(ADR-040, docs/approvals.md). mgmt asks every live
host over the mgmt → hostd channel and merges the answers, oldest
first; a host that does not answer is named in `unreachable`.
Admins see every approval; a tailnet caller holding only
`github.com/rosskukulinski/tailworks/cap/approve` (toward `tag:tw-mgmt`) sees the ones its
grants let it decide. A read, so not audited.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | query | string | no | Only this workload's approvals. |
| `all` | query | boolean | no | Include decided (and timed-out) approvals the hosts still keep. |

| Status | Body | Description |
|---|---|---|
| 200 | `ApprovalList` | The approvals. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}/notifications` — Read the tenant's notification webhook (`tw notify`), without secrets

`GetTenantNotifications` · role **admin\|tenant:read** · audited: no

ADR-047. The webhook URL is shown as its origin
only (scheme and host): a chat webhook's path or query is its
credential. Header names only. A tenant that sends nothing has no
`webhook`. `404 unknown_tenant`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |

| Status | Body | Description |
|---|---|---|
| 200 | `NotificationConfig` | The tenant's notification config. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `PUT /v1/tenants/{id}/notifications` — Set the tenant's notification webhook (`tw notify set`)

`PutTenantNotifications` · role **admin\|tenant:deploy** · audited: yes

ADR-047. When one of `events` happens in the
tenant, mgmt POSTs a JSON `NotificationEvent` to `url` (header
`Tw-Event`), with a deep link to the admin UI. `approval_requested`
fires when a Workflow starts waiting for an approval (when mgmt
receives the host's audit record, usually within seconds). mgmt
dials the URL itself: a tailnet name, a `100.x` address or a
`*.ts.net` name over mgmt's tailnet node, anything else over the
host's network; loopback and link-local addresses are refused.
Delivery is tried 3 times; a final failure is audited
(`notification_failed`). The config is stored encrypted in mgmt
custody (custody name `tw/<tenant>/notifications`); the URL's path
and query and the header values are never logged, audited or
returned. Audited (`kind: notification_put`, the URL's host and
header names).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |

**Request** `application/json`: `NotificationsPut`

| Status | Body | Description |
|---|---|---|
| 200 | `NotificationConfig` | The new config (origin and header names only). |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `DELETE /v1/tenants/{id}/notifications` — Stop the tenant's notifications (`tw notify rm`)

`DeleteTenantNotifications` · role **admin\|tenant:deploy** · audited: yes

Idempotent. Audited (`kind: notification_delete`).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |

| Status | Body | Description |
|---|---|---|
| 204 | — | Deleted (or there was nothing to delete). |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/tenants/{id}/notifications/test` — Send a test notification now (`tw notify test`)

`TestTenantNotifications` · role **admin\|tenant:deploy** · audited: yes

POSTs a `NotificationEvent` with `event: test` to the configured
webhook once, synchronously, and reports the outcome. Not retried.
Audited (`kind: notification_test`, the origin and the outcome).
`404 not_found` when no webhook is set.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |

| Status | Body | Description |
|---|---|---|
| 200 | `NotificationTestResult` | The delivery attempt. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `DELETE /v1/tenants/{id}/objects` — Purge terminal objects of a tenant

`PurgeTenantObjects` · role **admin** · audited: yes

Forgets the tenant's objects in the given terminal `state`s (default:
both `killed` and `failed`) so the same `(workload, namespace, name)`
can be created again. Pending device deletes for them run first. Live
objects are never touched (there is no `force` here; use `DELETE
/v1/objects/{id}` to kill). Each removal is audited (`kind: purge`).
Replaces `POST /v0/tenants/{id}/purge`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `state` | query | array of string: `killed` \| `failed` \| `transferred` | no | Terminal states to purge. Repeatable. Any other value is `400 bad_request`. |

| Status | Body | Description |
|---|---|---|
| 200 | `PurgeResult` | Purged. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}/public-names` — List the public names a tenant owns

`ListPublicNames` · role **admin\|tenant:read** · audited: no

ADR-029, red-team H10. A public workload is served at
`https://<workload>.<tailnet>.ts.net/`, so its name is unique per
tailnet, not per tenant. The first tenant to deploy public routes
under a name owns it on that tailnet, across undeploys and
redeploys, until it releases it (`DELETE …/public-names/{name}`) or
the tenant is deleted. A deploy (or dry run) by another tenant on the
same tailnet is refused with `409 public_name_owned`. Ordered by
`(claimedAt, name)`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `limit` | query | integer | no | Page size, 1–500 (default 50). Out-of-range values are `400 bad_request`. |
| `cursor` | query | string | no | Opaque page cursor from the previous page's `nextCursor`. Filters must be repeated unchanged. |

| Status | Body | Description |
|---|---|---|
| 200 | `PublicNamePage` | One page of the tenant's public names, ordered by `(claimedAt, name)`. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `DELETE /v1/tenants/{id}/public-names/{name}` — Release a public name the tenant owns

`ReleasePublicName` · role **admin\|tenant:deploy** · audited: yes

Red-team H10. Gives up the tenant's claim on a public name, so any
tenant on the tailnet can deploy public routes under it next. Only
a name nothing serves can be released: `409 public_name_in_use`
while the workload's latest deployment declares public routes or
its public node is live (undeploy it, or remove `"public"` from its
`tailworks.jsonc` and redeploy, first). `404 not_found` when the tenant
does not own the name. Audited (`kind: public_name_release`).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `name` | path | string | yes | A public name (the workload name of a public workload; a DNS label). |

| Status | Body | Description |
|---|---|---|
| 204 | — | Released. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The request conflicts with the resource's current state. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/tenants/{id}/workloads/{workload}/approvals/{workflow}/{instance}` — Approve or reject a waiting Workflow instance (`tw approve`, the admin UI)

`DecideApproval` · role **admin\|approver** · audited: yes

The decision is made by the caller's own tailnet identity: mgmt's
WhoIs of the connection (user, node, tags). mgmt sends it, with
the caller's `github.com/rosskukulinski/tailworks/cap/approve` grants toward `tag:tw-mgmt`, to
the host holding the workload's lease; that host checks the grants
(`cap_missing` otherwise: the workload, Workflow, step, decision and
numeric limits a grant names), delivers the event (the Workflow
sees `approver`, never one from the request) and audits the
decision (`approval_decision`, or `approval_denied`). Being a tailworks
admin does not let anyone approve: the grant does. The operator
console and the local socket have no personal identity, so they
get `403 no_identity`; use `tw link` from your own machine.
`409 approval_not_pending` when nothing waits for this decision.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `workflow` | path | string | yes | The Workflow's name (wrangler `workflows[].name`). |
| `instance` | path | string | yes | The Workflow instance ID. |

**Request** `application/json`: `ApprovalDecisionRequest`

| Status | Body | Description |
|---|---|---|
| 200 | `ApprovalDecisionResponse` | Delivered; the approval as decided. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The request conflicts with the resource's current state. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}/workloads/{workload}/logs` — Read (or follow) a workload's workerd / node log (`tw logs` from a linked machine)

`GetWorkloadLogs` · role **admin\|tenant:read** · audited: no

The app's log is a file on the host that runs it. mgmt finds the
host holding the workload's namespace lease and reads the log from
that host's hostd over the mgmt → hostd channel (the push port);
hostd resolves the file from its own record of the tenant's
workload, never from a path the caller sends. The response is
`text/plain`: the last `n` lines, then (with `follow=true`) new
lines as they are written, streamed until the client disconnects
or the host ends the stream (at most an hour; follow again to
continue).

A workload with `replicas` reads every replica's host; each line
is then prefixed with `[<hostID>] `, and `host` selects one
replica. A host that does not answer contributes one
`[<hostID>] tw: …` line instead (`502 host_unavailable` when no
host answers). `404 unknown_workload` when the workload has no
running replica (on `host`); `404 no_logs` when the host has no
log file for it yet. A read, so not audited; mgmt logs the actor.
Only the workload's own app log: the daemons' logs
(`--component`) stay on the host.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `n` | query | integer | no | Lines from the end of the log (0 with `follow` for new lines only). |
| `follow` | query | boolean | no | Keep the stream open and send new lines as they are written. |
| `host` | query | string | no | Only the replica on this host (a host ID). |
| `source` | query | string: `console` \| `requests` | no | `console`: the app's raw output (workerd's stdout, the node tier's `app.log`, the microVM console). `requests`: the workload's structured request records (ADR-045), as JSON Lines (`application/x-ndjson`), one `RequestRecord` per line; `n` and the filters below then count records, not lines. |
| `object` | query | string | no | `source=requests`: only records of this object (object ID, `Class/name`, `Class` or name). |
| `status` | query | string | no | `source=requests`: only this HTTP status (`404`) or class (`5xx`). |
| `outcome` | query | string | no | `source=requests`: only this workerd outcome (`ok`, `exception`, `canceled`, …). |
| `caller` | query | string | no | `source=requests`: only callers whose login or node name contains this (`public` for Funnel requests). |
| `trace` | query | string | no | `source=requests`: only this W3C trace ID (or a prefix of it) or audit trace ID. |
| `errors` | query | boolean | no | `source=requests`: only errors (status ≥ 400, an outcome other than `ok`, or an exception). |

| Status | Body | Description |
|---|---|---|
| 200 | `RequestRecord` | The log lines (UTF-8 as the app wrote them; a stream with `follow`), or request records with `source=requests`. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}/workloads/{workload}/public` — Read a workload's public-ingress exposure (`tw expose`)

`GetWorkloadPublic` · role **admin\|tenant:read** · audited: no

ADR-029. A workload whose `tailworks.jsonc` declares `public` routes is
served over Funnel from its public node
(`https://<workload>.<tailnet>.ts.net/`) while `exposed` is true and
the tenant's `publicIngress` is `allow`. A workload that was never
toggled is exposed.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |

| Status | Body | Description |
|---|---|---|
| 200 | `WorkloadPublic` | The workload's exposure. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `PATCH /v1/tenants/{id}/workloads/{workload}/public` — Expose or unexpose a workload's public routes (JSON Merge Patch)

`UpdateWorkloadPublic` · role **admin\|tenant:deploy** · audited: yes

`tw expose on|off` (ADR-029 D1): an operator override that survives
redeploys. `exposed: false` makes the host close the public node's
Funnel listener and reset its flows (the node and its certificate
stay); `true` reopens it. The lease holder is pushed at once.
`404 unknown_workload` when the tenant has no deployment of the
workload. Audited (`kind: expose_on` / `expose_off`).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `If-Match` | header | string | no | Only apply if the resource's current `ETag` matches; otherwise `412 precondition_failed`. |

**Request** `application/json` or `application/merge-patch+json`: `WorkloadPublicPatch`

| Status | Body | Description |
|---|---|---|
| 200 | `WorkloadPublic` | The updated exposure. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 412 | `Error` | `If-Match` did not match the current `ETag`. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

## Hosts

tw-hostd instances. A host writes its own record; admins read.

### `GET /v1/hosts` — List hosts

`ListHosts` · role **admin** · audited: no

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `limit` | query | integer | no | Page size, 1–500 (default 50). Out-of-range values are `400 bad_request`. |
| `cursor` | query | string | no | Opaque page cursor from the previous page's `nextCursor`. Filters must be repeated unchanged. |
| `live` | query | boolean | no | Only hosts that heartbeated within the host lease TTL (10 s). |

| Status | Body | Description |
|---|---|---|
| 200 | `HostPage` | One page of hosts, ordered by `(createdAt, hostID)`. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/hosts` — Add a host and mint its single-use join key

`CreateHost` · role **admin** · audited: yes

`tw host add`: creates the host record (status `pending`), mints a
**single-use, pre-authorized auth key for `tag:tw-host`** (10 min)
and a one-time `registrationSecret` (32 random bytes, base64url; only
its SHA-256 is stored) that the host must present on its first
`PUT /v1/hosts/{id}`, so the join key alone cannot claim the host ID
through the operator tenant's credential (its OAuth client, or the
Headscale API key). `tw-hostd join --token <key>` joins the operator tailnet
with it as `tw-host-<hostID>`, then `PUT /v1/hosts/{id}` binds that
node to the record. Calling it again for a host that has not bound a
node yet mints a fresh key (`200`); a bound host is `409 exists`
(delete it first). The key is never logged. Audited
(`kind: host_add`).

**Request** `application/json`: `HostCreate`

| Status | Body | Description |
|---|---|---|
| 200 | `HostJoin` | The pending host exists; a fresh join key. |
| 201 | `HostJoin` | Created; the join key. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | Unknown operator tenant. |
| 409 | `Error` | The request conflicts with the resource's current state. |
| 502 | `Error` | The control plane refused the mint (`bootstrap_failed`). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/hosts/{id}` — Read a host

`GetHost` · role **admin\|host** · audited: no

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Host ID (one safe path element). |
| `If-None-Match` | header | string | no | Answer `304` when the `ETag` still matches. |

| Status | Body | Description |
|---|---|---|
| 200 | `Host` | The host. |
| 304 | `Error` | The `If-None-Match` validator still matches. No body. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `PUT /v1/hosts/{id}` — Register or re-register a host (idempotent)

`RegisterHost` · role **host** · audited: yes

The host's own record. Over the tailnet the WhoIs node (`tag:tw-host`)
is bound to this `hostID` on first registration, which must carry the
`registrationSecret` from `POST /v1/hosts` (missing or wrong:
`403 host_identity_mismatch`; the secret is cleared once the node is
bound). A `PUT` for a host ID the admin never created is
`403 host_not_registered` (no implicit creation over the tailnet;
only the `--local-dev` socket keeps it). A different node using the
same ID is `403 host_identity_mismatch`. Re-registering with the same
node needs no secret and replaces capacity, tiers and `push`;
`auditPubKey` is **pinned on first bind** (a later `PUT` may repeat
it or omit it; a different key is `403 host_identity_mismatch` and
rotating it is `tw host rm` + `tw host add`). Hosts re-register on
every restart.

A host that was created through `tw host add` exists before its first
`PUT` (status `pending`); the `PUT` activates it.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Host ID (one safe path element). |

**Request** `application/json`: `HostRegistration`

| Status | Body | Description |
|---|---|---|
| 200 | `Host` | Re-registered. |
| 201 | `Host` | First registration of this host ID. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | Another node owns this host ID, or the caller is not a `tag:tw-host` node. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `DELETE /v1/hosts/{id}` — Drain and remove a host

`DeleteHost` · role **admin** · audited: yes

`409 host_holds_leases` while the host holds any namespace lease,
unless `drain=true`: then its deployments are marked `draining`, its
objects are killed (cascade), leases are released and the record goes.
Pushes to the host stop. The host's tsnet device is deleted through the
operator credential when mgmt has one; otherwise it is reported in
`devicesLeft`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Host ID (one safe path element). |
| `If-Match` | header | string | no | Only apply if the resource's current `ETag` matches; otherwise `412 precondition_failed`. |
| `drain` | query | boolean | no | Kill the host's objects and release its leases first. |

| Status | Body | Description |
|---|---|---|
| 202 | `HostDeleteResult` | Accepted; the drain runs. Poll `GET /v1/hosts/{id}` until `404`. |
| 204 | — | Removed (it held nothing). |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The host holds leases and `drain` was not set. |
| 412 | `Error` | `If-Match` did not match the current `ETag`. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/hosts/{id}/assignments` — List the deployments assigned to a host

`ListHostAssignments` · role **host\|admin** · audited: no

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Host ID (one safe path element). |
| `limit` | query | integer | no | Page size, 1–500 (default 50). Out-of-range values are `400 bad_request`. |
| `cursor` | query | string | no | Opaque page cursor from the previous page's `nextCursor`. Filters must be repeated unchanged. |

| Status | Body | Description |
|---|---|---|
| 200 | `AssignmentPage` | One page of assignments, ordered by `(createdAt, deploymentID)`. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `PUT /v1/hosts/{id}/cordon` — Cordon a host (no new placements)

`CordonHost` · role **admin** · audited: yes

The host keeps running what it runs, keeps heartbeating and keeps its leases; placement
skips it (`cordoned` in a decision). Idempotent; audited `host_cordon`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Host ID (one safe path element). |

| Status | Body | Description |
|---|---|---|
| 200 | `Host` | Cordoned. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `DELETE /v1/hosts/{id}/cordon` — Uncordon a host

`UncordonHost` · role **admin** · audited: yes

Placement may choose the host again. Idempotent; audited `host_uncordon`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Host ID (one safe path element). |

| Status | Body | Description |
|---|---|---|
| 200 | `Host` | Uncordoned. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/hosts/{id}/drain` — Cordon a host and move its workloads to other hosts

`DrainHost` · role **admin** · audited: yes

Cordons the host, then re-places each workload assigned to it on another eligible host
(`tw deploy --replace` with this host excluded; the lease moves at the next epoch):

- a stateless workload (no Durable Objects) moves;
- a workload with Durable Objects keeps its storage on this host's disk, so it is refused
  (`refused`, `move_unconfirmed`) unless `allowLocalStateLoss` is set; then it moves
  and its objects start empty on the new host;
- a workload with no other eligible host is refused (`no_host`, `feature_unavailable`).

`dryRun` reports what would happen and changes nothing (the host is not cordoned).
Audited `host_drain`. The host record is kept: `tw host uncordon` takes it back, and
`DELETE /v1/hosts/{id}` removes it.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Host ID (one safe path element). |

**Request** `application/json`: `HostDrainRequest`

| Status | Body | Description |
|---|---|---|
| 200 | `HostDrainResult` | The drain's outcome per workload. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `PUT /v1/hosts/{id}/paths` — Report measured tailnet paths (smart placement)

`ReportHostPaths` · role **host** · audited: no

Smart placement (ADR-033). The host reports, per tenant, the path
from that tenant's nodes on this host to each target its workloads
declare (`placementTargets` in its assignments): `direct` WireGuard,
`peer-relay`, `derp`, or `unresolved` / `unreachable`, with the RTT.
Each listed tenant's stored rows for this host are **replaced**.
`ageMs` is how long ago the host measured (its monotonic clock);
mgmt stamps `measuredAt` with the store clock, and placement ignores
measurements older than 5 min. A host may report only for tenants it
runs a workload of (`403` otherwise). Bounds: 64 tenants, 32 targets
each. Hosts send it at most once a minute, and only while they have
targets. Not audited (telemetry); the placement decision it feeds is
recorded with the deploy.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Host ID (one safe path element). |

**Request** `application/json`: `HostPathReport`

| Status | Body | Description |
|---|---|---|
| 200 | object | Stored. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | The host never registered (`host_not_registered`). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/hosts/{id}/sandbox-deny` — Every tenant's credential file paths, for the host's sandbox deny list

`GetHostSandboxDeny` · role **host\|admin** · audited: no

The paths of every tenant's `apiKeyFile` and `oauthClientFile`
(sorted, deduplicated), and nothing else: no tenant ID, tailnet or
name. A process-tier host denies them all to every workload sandbox
it starts, including the files of tenants that have no deployment
on it yet. `GET /v1/tenants` shows a host only its own tenants, which
is not enough: a workload started before another tenant's first
deploy would otherwise be able to read that tenant's key file. A
host may read only its own `{id}`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Host ID (one safe path element). |

| Status | Body | Description |
|---|---|---|
| 200 | object | The paths. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `PUT /v1/hosts/{id}/status` — Heartbeat (replace the host's reported status)

`ReportHostStatus` · role **host** · audited: no

Every 3 s. Renews the host lease (TTL 10 s) and every namespace lease
in `leases` (TTL 15 s; a renew succeeds while `(holder, epoch)` still
matches). The reply lists the leases that are no longer held
(`revoke`) and the objects killed in the last 10 min in namespaces
this host holds (`revokeObjects`; they repeat until the window passes,
so the host's kill must be idempotent). Replaces
`POST /v0/hosts/{id}/heartbeat`. Not audited (3 s cadence).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Host ID (one safe path element). |

**Request** `application/json`: `HostStatus`

| Status | Body | Description |
|---|---|---|
| 200 | `HostStatusResponse` | Renewed. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | The host never registered (`host_not_registered`). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

## Deployments

Deploys of a workload bundle to a host.

### `GET /v1/deployments` — List deployments

`ListDeployments` · role **admin\|tenant:read** · audited: no

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `limit` | query | integer | no | Page size, 1–500 (default 50). Out-of-range values are `400 bad_request`. |
| `cursor` | query | string | no | Opaque page cursor from the previous page's `nextCursor`. Filters must be repeated unchanged. |
| `tenant` | query | string | no |  |
| `workload` | query | string | no |  |
| `host` | query | string | no |  |
| `latest` | query | boolean | no | Only the newest deployment of each `(tenant, workload)` (what `tw ls` shows). |

| Status | Body | Description |
|---|---|---|
| 200 | `DeploymentPage` | One page of deployments, ordered by `(createdAt, deploymentID)`. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/deployments` — Deploy a workload (async), or preflight it with dryRun

`CreateDeployment` · role **admin\|tenant:deploy** · audited: yes

mgmt reads `tailworks.jsonc` from the bundle (a tar, optionally gzipped;
the shallowest `tailworks.jsonc`): `tailnet.tags` (the `_workload` entry
node), `tailnet.objects.<Class>.{tags, agent, ttl, node}`,
`tailnet.spawn.{maxLive ≤ 64, maxDepth ≤ 3}`, `tailnet.connects`,
`tier`, `runtime`, `placement.{mode, targets, aggregate}` (or
wrangler's `placement`). A bundle with neither `tailnet.tags` nor
`tailnet.objects` is `400 bad_bundle`. mgmt also validates
`wrangler.jsonc` `triggers.crons` (cron triggers: at most 10, no
duplicates, each a valid five-field UTC expression that can fire); an
invalid one is `400 bad_bundle`.
Every tag must match `^tag:[a-zA-Z0-9][a-zA-Z0-9._-]*$`
(`400 bad_request`), and tailworks' own infrastructure tags (`tag:tw`,
`tag:tw-mgmt`, `tag:tw-host`) are refused with `400 reserved_tag`;
both deploy and preflight enforce this.

**Deploy** (`dryRun` absent or false): picks a host, acquires the
`(tenant, workload)` lease at `epoch+1`, stores the bundle, pushes
the assignment to the host and answers `202` with
`Location: /v1/deployments/{id}`. Poll it until `status` is `active`
or `failed`.

**Placement** (ADR-033; the answer's `placement` says why):
1. Eligible hosts are live, in the tenant's pool, offer the
   workload's tier (`microvm` needs a host with KVM; `503 no_host`
   otherwise), and have free capacity.
2. `host` pins one (it must still be eligible; a pinned host without
   the tier is `409 tier_unavailable`).
3. Otherwise the current live lease holder is kept (placement is
   sticky; a better host appearing never moves a running workload).
   A holder without the tier the workload now asks for is
   `409 tier_unavailable` (or, with `replace`, the workload moves).
4. Otherwise, with smart placement (the default when the workload
   declares `tailnet.connects` or `placement.targets`): the best
   measured path to the targets wins (direct > peer relay > DERP >
   unmeasured, then RTT; `placement.aggregate` "worst" (default) or
   "sum" over several targets). Hosts without a fresh measurement are
   asked to probe (≤ 2 s). Free capacity breaks ties, then host ID.

`replace: true` (`tw deploy --replace`) re-places a running workload:
it moves only if an eligible host is clearly better (a better path
class, or ≥ 2 ms and ≥ 20 % lower RTT), and the lease moves to it at
`epoch+1` in one conditional write. A stateful workload's move also
needs `forceTakeover` (`409 move_unconfirmed`): its Durable Object
storage stays on the old host.

**Preflight** (`dryRun=true`): the same body and parser, but mgmt
only checks the policy: every src tag must be in `tagOwners`, and
each `tailnet.connects` entry that is a tag (or `*`) needs a grant
with an `ip` part whose `src` matches every src tag. Names are
reported `unchecked`. Nothing is written, no lease is taken, and the
answer is `200 PreflightResult`. `503 policy_unavailable` when mgmt
cannot read the policy (an OAuth client without `policy_file:read`);
the CLI then prints `preflight skipped` and deploys.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `dryRun` | query | boolean | no | Preflight only; see the description. |

**Request** `application/json`: `DeploymentCreate`

| Status | Body | Description |
|---|---|---|
| 200 | `PreflightResult` | `dryRun=true`: the preflight report. Nothing was deployed. |
| 202 | `Deployment` | Accepted; the host is starting the workload. |
| 400 | `Error` | Bad body or bundle. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | Unknown tenant. |
| 503 | `Error` | No live host can take the workload, or the policy cannot be read (dryRun). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/deployments/{id}` — Read a deployment and its status

`GetDeployment` · role **admin\|host\|tenant:read** · audited: no

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Deployment ID (`dp_…`). |
| `If-None-Match` | header | string | no | Answer `304` when the `ETag` still matches. |

| Status | Body | Description |
|---|---|---|
| 200 | `Deployment` | The deployment, with the host-reported status and the entry node. |
| 304 | `Error` | The `If-None-Match` validator still matches. No body. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `DELETE /v1/deployments/{id}` — Undeploy

`DeleteDeployment` · role **admin\|tenant:deploy** · audited: yes

Kills every object of the deployment (cascade; pushed to the host, then
device deletes), releases the `(tenant, workload)` lease, marks the
deployment `stopped` and audits it (`kind: undeploy`). The record stays
readable (`status: stopped`) for history; a later deploy of the same
workload supersedes it. Only the latest deployment of a workload can be
undeployed (`409 superseded` otherwise).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Deployment ID (`dp_…`). |
| `If-Match` | header | string | no | Only apply if the resource's current `ETag` matches; otherwise `412 precondition_failed`. |
| `reason` | query | string | no |  |

| Status | Body | Description |
|---|---|---|
| 202 | `KillResult` | Accepted; the kills are in flight. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The request conflicts with the resource's current state. |
| 412 | `Error` | `If-Match` did not match the current `ETag`. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/deployments/{id}/bundle` — Download the raw bundle

`GetDeploymentBundle` · role **host\|admin\|tenant:read** · audited: no

Hosts fetch the bundle named by `Assignment.bundleURL`. Only the lease holder (or an admin) may read it.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Deployment ID (`dp_…`). |

| Status | Body | Description |
|---|---|---|
| 200 | string (binary) | The bundle bytes (tar, optionally gzipped) exactly as uploaded. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}/workloads/{workload}/deployments` — A workload's deployment history, newest first (`tw deployments`)

`ListWorkloadDeployments` · role **admin\|tenant:read** · audited: no

ADR-046. Every deployment of the workload, newest first:
who deployed it, when, the bundle's SHA-256, its status (the current
one is `assigned`, `starting`, `active` or `failed`; older ones are
`superseded` or `stopped`), the host's last report (`hostStatus`)
and whether mgmt still holds the bundle (`bundleRetained`). mgmt
keeps the bundles of the newest `bundleRetention` deployments of
each workload (tw-mgmt `-bundle-retention`) and prunes older ones;
their records stay, without the bundle, so they can't be rolled back
to. `rollbackOf` names the deployment a rollback redeployed. A
workload with no deployment is `404 unknown_workload`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `limit` | query | integer | no | Page size, 1–500 (default 50). Out-of-range values are `400 bad_request`. |
| `cursor` | query | string | no | Opaque page cursor from the previous page's `nextCursor`. Filters must be repeated unchanged. |

| Status | Body | Description |
|---|---|---|
| 200 | `DeploymentHistoryPage` | One page of the workload's deployments, ordered by `(createdAt, deploymentID)` descending. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}/workloads/{workload}/previews` — A workload's running previews (`tw previews`)

`ListWorkloadPreviews` · role **admin\|tenant:read** · audited: no

ADR-048. The previews of the workload that are not
stopped, newest first. A preview is created by
`POST /v1/deployments` with `preview: true`: it runs as a workload
of its own, `<workload>--preview-<alias>`, with its own lease,
Durable Object storage and stores, and never joins the workload's
Tailscale Service or public routes. A workload without previews
answers an empty list.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |

| Status | Body | Description |
|---|---|---|
| 200 | `PreviewList` | The running previews. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/tenants/{id}/workloads/{workload}/promotions` — Promote a preview to the workload (`tw promote`), or preflight it with dryRun

`CreatePromotion` · role **admin\|tenant:deploy** · audited: yes

ADR-048. Deploys the bundle the preview runs to the
workload itself, as a **new** deployment, through the same code
path as `POST /v1/deployments`: the same validation, placement
(sticky), lease at `epoch+1`, host fencing and refusals, and the
workload's own `replicas`, Tailscale Service and public routes.
Container images the preview's deploy built are reused by digest.
The preview's data does not move: the workload keeps its own
Durable Object storage, stores and secrets.

Then the preview is undeployed, unless `keepPreview`. An unknown
preview, or one that is stopped, is `404 unknown_preview`; a preview
whose host reported it `failed` is `409 preview_failed`.

Audited as `kind: promote` (in place of `deploy`), with
`promotedFrom` (the preview's deployment) and `preview`.
`dryRun=true` preflights the preview's bundle against the current
policy.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `dryRun` | query | boolean | no | Preflight only (`200 PreflightResult`); nothing is deployed. |

**Request** `application/json`: `PromotionCreate`

| Status | Body | Description |
|---|---|---|
| 200 | `PreflightResult` | `dryRun=true`: the preflight report of the preview's bundle. Nothing was deployed. |
| 202 | `Promotion` | Accepted; the host is starting the promoted deployment. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The request conflicts with the resource's current state. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/tenants/{id}/workloads/{workload}/rollbacks` — Roll a workload back to a stored bundle (`tw rollback`), or preflight it with dryRun

`CreateRollback` · role **admin\|tenant:deploy** · audited: yes

ADR-046. Redeploys the bundle of an earlier deployment
as a **new** deployment, through the same code path as
`POST /v1/deployments`: the same validation, placement, lease at
`epoch+1` and host fencing, and the same `409 lease_held`,
`move_unconfirmed` and `takeover_unconfirmed` refusals. Placement
is sticky as for any deploy (the workload stays on its host unless
`replace` or `host` say otherwise), so Durable Object storage stays
where it is. Container images the original deploy built and pushed
to tailworks' registry are reused by digest.

Without `deploymentID`, the target is the newest earlier deployment
that ran (the host reported it `active` and it did not fail), whose
bundle is retained and differs from the current one's;
`409 no_rollback_target` if there is none. A target whose bundle was
pruned is `409 bundle_pruned`. Naming the current deployment is
`409 no_rollback_target`.

A rollback changes code and configuration only. Durable Object
storage, D1 databases (migrations are forward-only), KV, R2 and
queues keep their current data, and the workload gets the secrets
it has **now**: secrets are not versioned with bundles.

Audited as `kind: rollback` (in place of `deploy`), with
`rollbackOf`. `dryRun=true` preflights the stored bundle against the
current policy, like `POST /v1/deployments?dryRun=true`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `dryRun` | query | boolean | no | Preflight only (`200 PreflightResult`); nothing is deployed. |

**Request** `application/json`: `RollbackCreate`

| Status | Body | Description |
|---|---|---|
| 200 | `PreflightResult` | `dryRun=true`: the preflight report of the target's bundle. Nothing was deployed. |
| 202 | `Rollback` | Accepted; the host is starting the rolled-back deployment. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The request conflicts with the resource's current state. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

## Objects

DO instances, agents and sub-agents, each with its own tailnet node.

### `GET /v1/objects` — List objects (with lineage)

`ListObjects` · role **admin\|host\|tenant:read** · audited: no

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `limit` | query | integer | no | Page size, 1–500 (default 50). Out-of-range values are `400 bad_request`. |
| `cursor` | query | string | no | Opaque page cursor from the previous page's `nextCursor`. Filters must be repeated unchanged. |
| `tenant` | query | string | no |  |
| `workload` | query | string | no |  |
| `namespace` | query | string | no | DO class name, or `_workload`. |
| `state` | query | array of `ObjectStatus` | no | Repeatable. Default is every state, tombstones included. |
| `parent` | query | string | no | Direct children of this object ID. |
| `root` | query | string | no | The whole tree under this root object ID. |

| Status | Body | Description |
|---|---|---|
| 200 | `ObjectPage` | One page of objects, ordered by `(createdAt, objectID)`. Hosts see only objects whose `(tenant, workload)` lease they hold; other objects are filtered out, never listed. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/objects` — Create an object (async mint)

`CreateObject` · role **host** · audited: yes

hostd calls this on first `create()`/`spawn()`. The identity is
`(tenant, workload, namespace = DO class, objectName)`; the object ID
`<class-slug>-<b32(hmac)[:10]>` is also the node hostname.

- Idempotent by identity: an existing live object is returned with
  `200`; a new one is `202` with `Location: /v1/objects/{id}` and
  `status: pending`. A killed identity is `410 killed`
  (`TombstoneError`); an ID collision with another identity is
  `409 identity_conflict`.
- `namespace: "_workload"` is the workload entry node; `workload`
  must equal `objectName` (`400`). An unknown `(workload, class)` is
  `404 unknown_namespace`; without `workload`, several workloads
  declaring the class is `409 namespace_ambiguous`.
- `parent` makes it a spawn: `caps ⊆ parent` (receiver merge
  semantics over the policy; `403 delegation_invalid` with
  `missing: {src, dst, cap, field}`), every child tag owned by the
  parent's (`tagOwners`), and the spawn budget (`maxLive`,
  `maxDepth`; `429 spawn_budget_exceeded`). A spawn with a `ttl` gets
  an ephemeral key.
- The mint is queued behind the tenant throttle
  (`registrationsPerMinute`) and quota (`maxObjects`;
  `429 object_quota_exceeded`).
- `namespace: "_pool"` creates a pre-joined pool node for the class in
  `objectName` (`<Class>/<suffix>`; ADR-031): minted, released and
  made ready like any object, but serving nothing until a create
  names it in `poolObjectID`. Then that create answers
  `status: assigned` and mints nothing.
Spawn checks are unchanged from v0.
- A host may create objects only in workloads whose `(tenant,
  workload)` lease it holds (`403 forbidden`).

**Request** `application/json`: `ObjectCreate`

| Status | Body | Description |
|---|---|---|
| 200 | `Object` | The object already existed (idempotent create). |
| 202 | `Object` | Accepted; the mint is queued. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | Delegation refused, or the host does not hold the namespace. |
| 404 | `Error` | Unknown tenant or namespace. |
| 409 | `Error` | `identity_conflict` or `namespace_ambiguous`. |
| 410 | `TombstoneError` | The object was killed. The body is the typed error plus the kill record; it stays until purged. |
| 429 | `Error` | Spawn budget or tenant quota. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/objects/{id}` — Read an object (lineage, children, kill record)

`GetObject` · role **admin\|host\|tenant:read** · audited: no

A host may read only objects whose lease it holds (`403 forbidden` otherwise); peers verify other nodes through `GET /v1/certificates/{stableNodeID}` instead.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Object ID (`<class-slug>-<b32(hmac)[:10]>`, also the node hostname). |
| `If-None-Match` | header | string | no | Answer `304` when the `ETag` still matches. |

| Status | Body | Description |
|---|---|---|
| 200 | `Object` | The object. |
| 304 | `Error` | The `If-None-Match` validator still matches. No body. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 410 | `TombstoneError` | The object was killed. The body is the typed error plus the kill record; it stays until purged. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `PATCH /v1/objects/{id}` — Update an object (JSON Merge Patch)

`UpdateObject` · role **host\|admin\|tenant:kill** · audited: yes

Two callers, two subsets:

- **host** (lease holder): `{status: "ready", nodeID, stableNodeID,
  magicDNS, epoch}` reports the node joined. `epoch` is **required**
  for the ready group (`400 bad_request` without it) and the commit is
  always fenced on `(lease, caller host, epoch)` (`409 lease_lost`);
  a host that does not hold the object's lease is `403 forbidden`.
  mgmt then signs the **node certificate**
  (`{v, tenant, objectID, stableNodeID, tags, lim?, parent?, root,
  iat, exp}`, 24 h, the tenant's OBO root key) and stores it; the
  `ready` audit detail adds `cert: true` and `lim`. If the object was
  killed during the join the answer is `410 killed` and mgmt deletes
  the device that just appeared (only when the fence held: a caller
  without the lease cannot make mgmt delete devices). Replaces
  `POST /v0/objects/{id}/ready`.
- **admin**: `{ttl}` sets or clears (`null`) the TTL; the reaper
  applies it.

Any other field, or a host sending admin fields (and vice versa), is
`400 immutable_field`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Object ID (`<class-slug>-<b32(hmac)[:10]>`, also the node hostname). |
| `If-Match` | header | string | no | Only apply if the resource's current `ETag` matches; otherwise `412 precondition_failed`. |

**Request** `application/json` or `application/merge-patch+json`: `ObjectPatch`

| Status | Body | Description |
|---|---|---|
| 200 | `Object` | The updated object (`status: ready` carries `cert`). |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | `lease_lost` (the caller does not hold the namespace lease at this epoch) or `identity_conflict` (an `assigned` object reported ready on another node than the pool node it was given). |
| 410 | `TombstoneError` | The object was killed. The body is the typed error plus the kill record; it stays until purged. |
| 412 | `Error` | `If-Match` did not match the current `ETag`. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `DELETE /v1/objects/{id}` — Kill an object

`DeleteObject` · role **admin\|host\|tenant:kill** · audited: yes

Marks the object (and with `cascade=true` every descendant) killed,
pushes the kill to the lease-holding host (tier 1, ≤ 2 s per push;
the heartbeat's `revokeObjects` is the fallback), then deletes the
device(s) through the tenant credential (tier 2; deferred by
`-device-delete-delay` so the host's tombstone answers
`410 object_killed` while peers still have the node). Audited
(`kind: kill`, `detail.by`, `pushed`, `deviceDeleteIn`).

Hosts may kill only objects in namespaces they hold (`403`).
A later `GET` is `410 Gone` with the kill record. Replaces
`POST /v0/objects/{id}/kill`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Object ID (`<class-slug>-<b32(hmac)[:10]>`, also the node hostname). |
| `If-Match` | header | string | no | Only apply if the resource's current `ETag` matches; otherwise `412 precondition_failed`. |
| `cascade` | query | boolean | no | Also kill the subtree below this object. |
| `reason` | query | string | no |  |

| Status | Body | Description |
|---|---|---|
| 202 | `KillResult` | Accepted; tier 1 is done or pushed, device deletes are scheduled. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 410 | `TombstoneError` | The object was killed. The body is the typed error plus the kill record; it stays until purged. |
| 412 | `Error` | `If-Match` did not match the current `ETag`. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/objects/{id}/certificate` — Read the object's node certificate

`GetObjectCertificate` · role **host\|admin** · audited: no

The certificate mgmt signed at `ready` (re-issued on fetch when missing
or within 1 h of expiry). The receiving netd verifies it against the
tenant's `oboPublicKey` and narrows the caller's caps to `cert.lim`;
a certificate that fails verification, names another node or cannot
be fetched makes netd withhold the caller's caps. `410 killed` for
tombstones. Replaces `GET /v0/certs/{nodeID}`; see also
`GET /v1/certificates/{stableNodeID}` for lookup by node.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Object ID (`<class-slug>-<b32(hmac)[:10]>`, also the node hostname). |

| Status | Body | Description |
|---|---|---|
| 200 | `NodeCertificate` | The certificate. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The object has no node yet (`not_ready`). |
| 410 | `TombstoneError` | The object was killed. The body is the typed error plus the kill record; it stays until purged. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/objects/{id}/credentials` — Mint a single-use auth key for the object's node

`CreateObjectCredential` · role **host** · audited: yes

Creates one single-use, tagged auth key (≤ 5 min) and releases it
**only** to the host that holds the namespace lease at `epoch`
(`409 lease_lost` otherwise). Long-polls a pending mint for `wait`
seconds (default 10, max 30; `503 identity_pending` + `Retry-After: 1`
after that). An expired unused key is revoked and re-minted
automatically. `Cache-Control: no-store`.

`reason=rejoin_rejected` is the **re-mint**: the saved node state was
rejected by control, so mgmt deletes the old device, clears the node
certificate and mints again under the same hostname. Replaces
`GET /v0/objects/{id}/credential` and `POST …/remint`. Audited
(`kind: mint` / `remint`). The key is never logged.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Object ID (`<class-slug>-<b32(hmac)[:10]>`, also the node hostname). |
| `epoch` | query | integer (int64) | yes | The namespace lease epoch the caller believes it holds. Fenced in the store (`409 lease_lost`). |
| `wait` | query | integer | no | Seconds to wait for a pending mint. |
| `reason` | query | string: `rejoin_rejected` | no | `rejoin_rejected` requests a re-mint; omitted for a first join. |

| Status | Body | Description |
|---|---|---|
| 201 | `Credential` | The key. Single use; it expires at `expiresAt`. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | `lease_lost` (wrong epoch or holder) or `already_joined` (the node exists; rejoin from saved state, or re-mint with `reason`). |
| 410 | `TombstoneError` | The object was killed. The body is the typed error plus the kill record; it stays until purged. |
| 502 | `Error` | The control plane refused the mint (`bootstrap_failed`); the object is `failed`. |
| 503 | `Error` | Still minting (`identity_pending`). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

## Credentials

Single-use auth keys and node certificates.

### `GET /v1/certificates/{stableNodeID}` — Look up a node certificate by stable node ID

`GetCertificate` · role **host\|admin** · audited: no

The receiver-side lookup: "is the peer that just connected a tailworks
object, and what is its `lim`?" `404 not_found` when no live object
of the tenant has that node (the receiver then applies the WhoIs caps
unchanged: the peer is not a tailworks object); `410 killed` for a tombstone.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `stableNodeID` | path | string | yes |  |
| `tenant` | query | string | yes | The receiving object's tenant (certificates are per tenant). |

| Status | Body | Description |
|---|---|---|
| 200 | `NodeCertificate` | The certificate. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 410 | `TombstoneError` | The object was killed. The body is the typed error plus the kill record; it stays until purged. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

## Secrets

Worker secrets (Cloudflare-compatible `env.NAME`), encrypted in mgmt custody and released only to the workload's lease holder.

### `POST /v1/tenants/{id}/workloads/{workload}/secret-grants` — Release a workload's secret values to the lease holder

`CreateWorkloadSecretGrant` · role **host** · audited: yes

hostd calls this when it starts a deployment of the workload. As
with a volume key grant, the values are released only to the host
holding the workload's lease at `epoch` (`409 lease_lost`), with
`Cache-Control: no-store`, and are never logged. A workload with no
secrets gets an empty map. Audited (`kind: secret_release`, names
only) when there is at least one secret.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `epoch` | query | integer (int64) | yes | The namespace lease epoch the caller believes it holds. Fenced in the store (`409 lease_lost`). |

| Status | Body | Description |
|---|---|---|
| 201 | `SecretGrant` | The grant. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The caller does not hold the namespace lease at this epoch. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}/workloads/{workload}/secrets` — List a workload's secrets (names and times, never values)

`ListWorkloadSecrets` · role **admin\|tenant:read** · audited: no

`tw secret ls`. No admin operation returns a value; only the lease
holder gets them (`POST …/secret-grants`). Ordered by name. A
workload with no secrets (or never deployed) is an empty page.
`404 unknown_tenant`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `limit` | query | integer | no | Page size, 1–500 (default 50). Out-of-range values are `400 bad_request`. |
| `cursor` | query | string | no | Opaque page cursor from the previous page's `nextCursor`. Filters must be repeated unchanged. |

| Status | Body | Description |
|---|---|---|
| 200 | `SecretPage` | One page of secrets, ordered by name. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `PUT /v1/tenants/{id}/workloads/{workload}/secrets/{name}` — Create or replace a workload secret (`tw secret put`)

`PutWorkloadSecret` · role **admin\|tenant:secrets** · audited: yes

ADR-034. Stores the value encrypted at rest in mgmt's
custody (custody name `tw/<tenant>/workloads/<workload>/secrets`).
The workload need not be deployed yet. The value reaches the
workload as the text binding `env.<name>` (runtime `node`: the
environment variable `<name>`) on its **next deploy**; running
instances keep the value they started with. At most 128 secrets per
workload and 5 KiB per value. Names are JS identifiers that are also
environment variable names; `TW_…` and `__TW…` are reserved. The
value is never logged, audited or returned. Audited
(`kind: secret_put`, name only).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `name` | path | string | yes | Secret name: a JS identifier that is also an environment variable name; `TW_…` and `__TW…` are reserved. |

**Request** `application/json`: `SecretPut`

| Status | Body | Description |
|---|---|---|
| 200 | `Secret` | Replaced. |
| 201 | `Secret` | Created. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `DELETE /v1/tenants/{id}/workloads/{workload}/secrets/{name}` — Delete a workload secret (`tw secret rm`)

`DeleteWorkloadSecret` · role **admin\|tenant:secrets** · audited: yes

Takes effect on the workload's next deploy. `404 unknown_secret`
when the workload has no secret of that name. Audited
(`kind: secret_delete`).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `name` | path | string | yes | Secret name: a JS identifier that is also an environment variable name; `TW_…` and `__TW…` are reserved. |

| Status | Body | Description |
|---|---|---|
| 204 | — | Deleted. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

## Stores

Platform storage objects (ADR-044) behind storage bindings such as Workers KV. Each store is served by a platform-owned SQLite Durable Object on the workload's host; its writes go first to a fenced, encrypted journal here, so a store survives hostd restarts and moves to another host.

### `GET /v1/tenants/{id}/workloads/{workload}/d1/{name}/export` — Export a D1 database as SQL (`tw d1 export`)

`ExportD1` · role **admin\|tenant:data** · audited: no

The database's schema and rows as SQL text, in the shape of
`wrangler d1 export` (`PRAGMA defer_foreign_keys=TRUE;`, each
table and its `INSERT`s, then indexes, triggers and views), read
from the store object of the running workload after it has caught
up with the journal. At most 16 MiB (`400 store_limit` beyond).
Rows of virtual tables are not exported. A database that could
not apply a journal entry (degraded) exports what it applied,
headed by a `-- tailworks: degraded export` comment naming the
entry. A read: logged, not audited.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `name` | path | string | yes | The store's name within the workload and kind: for KV, the binding's `id` (or the binding name when it has none). |

| Status | Body | Description |
|---|---|---|
| 200 | string | The SQL. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/tenants/{id}/workloads/{workload}/d1/{name}/query` — Run SQL on a D1 database (`tw d1 execute`, `tw d1 migrations apply`)

`QueryD1` · role **admin\|tenant:data** · audited: yes

A D1 database (ADR-038) is the workload's platform store of kind
`d1` named after the binding's `database_name`. Its data is served
by the store object in the workload's workerd, so mgmt sends the
statements to the host holding the workload's lease (the mgmt →
hostd channel, as for logs), which hands them to its platform
worker. They run as one batch: atomic, after the store object has
caught up with the journal, and a batch that writes is appended to
the journal before it is applied, like every write through the
binding.

`200` with one result per statement (D1's shape, rows and
columns). A failing statement fails the whole batch (`400
d1_error`, with SQLite's message; nothing is applied). `404
unknown_workload` when the workload isn't running (the database is
only reachable through a running deployment that binds it); `404
unknown_store` when its deployment binds no such database. `409
store_degraded` when the store object could not apply a journal
entry (ADR-038): it refuses writes until then. A batch
that changed the database is audited (`kind: d1_write`, with the
statement count, never the SQL); a read is logged.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `name` | path | string | yes | The store's name within the workload and kind: for KV, the binding's `id` (or the binding name when it has none). |

**Request** `application/json`: `D1Query`

| Status | Body | Description |
|---|---|---|
| 200 | array of `D1Result` | One result per statement. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}/workloads/{workload}/stores` — List a workload's stores

`ListWorkloadStores` · role **admin\|tenant:read** · audited: no

Every store of the workload (all kinds), ordered by `(kind, name)`.
A store exists once something was written to it (a binding's first
write, or `PUT …/stores/{kind}/{name}`). Sizes are of the journal
mgmt keeps, after compaction.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `limit` | query | integer | no | Page size, 1–500 (default 50). Out-of-range values are `400 bad_request`. |
| `cursor` | query | string | no | Opaque page cursor from the previous page's `nextCursor`. Filters must be repeated unchanged. |
| `kind` | query | `StoreKind` | no | Only stores of this kind. |

| Status | Body | Description |
|---|---|---|
| 200 | `StorePage` | One page of stores. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}/workloads/{workload}/stores/kv/{name}/keys` — List a KV namespace's keys

`ListKVKeys` · role **admin\|tenant:data** · audited: no

Workers KV's `list()` for operators (`tw kv key list`,
ADR-037): live keys in UTF-8 byte order, with their
expiration and metadata, `limit` at a time (default and maximum
1000). Pass the answer's `cursor` to get the next page. mgmt reads
the namespace's journal (its own copy), so the answer includes
every acknowledged write. `404 unknown_store` if the namespace
does not exist.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `name` | path | string | yes | The store's name within the workload and kind: for KV, the binding's `id` (or the binding name when it has none). |
| `prefix` | query | string | no | Only keys that start with this. |
| `cursor` | query | string | no | The `cursor` of the previous page. |
| `limit` | query | integer | no |  |

| Status | Body | Description |
|---|---|---|
| 200 | `KVKeyPage` | One page of keys. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}/workloads/{workload}/stores/kv/{name}/value` — Get one KV value

`GetKVValue` · role **admin\|tenant:data** · audited: no

`tw kv key get`. `404 not_found` if the key does not exist or has
expired, `404 unknown_store` if the namespace does not.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `name` | path | string | yes | The store's name within the workload and kind: for KV, the binding's `id` (or the binding name when it has none). |
| `key` | query | string | yes | The KV key (any UTF-8, 1 to 512 bytes; not `.` or `..`). |

| Status | Body | Description |
|---|---|---|
| 200 | `KVValue` | The value. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `PUT /v1/tenants/{id}/workloads/{workload}/stores/kv/{name}/value` — Write one KV value

`PutKVValue` · role **admin\|tenant:data** · audited: yes

`tw kv key put`: a journal append, like a binding's `put()`, so
running workloads see it on their next sync (within 60 s, or a
read's `cacheTtl`). Cloudflare's rules apply: `expirationTtl` and
`expiration` at least 60 s ahead, metadata at most 1024 bytes of
JSON, values at most 2 MiB − 4 KiB (`400 bad_request` /
`400 store_limit`). The namespace must exist (`404
unknown_store`; `tw kv namespace create`). Audited (`kind:
kv_put`) with the namespace and who wrote, not the key: key names
can be sensitive, and mgmt keeps them only sealed and HMACed.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `name` | path | string | yes | The store's name within the workload and kind: for KV, the binding's `id` (or the binding name when it has none). |
| `key` | query | string | yes | The KV key (any UTF-8, 1 to 512 bytes; not `.` or `..`). |

**Request** `application/json`: `KVPut`

| Status | Body | Description |
|---|---|---|
| 200 | `KVKey` | Written. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | `lease_lost`: the host does not hold the workload's lease at this epoch. `store_conflict`: the journal's head (or incarnation) is not the one the writer gave; catch up and retry. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `DELETE /v1/tenants/{id}/workloads/{workload}/stores/kv/{name}/value` — Delete one KV value

`DeleteKVValue` · role **admin\|tenant:data** · audited: yes

`tw kv key delete`. Deleting a key that does not exist succeeds and writes nothing. Audited (`kind: kv_delete`, the namespace only) when it deletes.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `name` | path | string | yes | The store's name within the workload and kind: for KV, the binding's `id` (or the binding name when it has none). |
| `key` | query | string | yes | The KV key (any UTF-8, 1 to 512 bytes; not `.` or `..`). |

| Status | Body | Description |
|---|---|---|
| 204 | — | Deleted (or there was nothing to delete). |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | `lease_lost`: the host does not hold the workload's lease at this epoch. `store_conflict`: the journal's head (or incarnation) is not the one the writer gave; catch up and retry. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}/workloads/{workload}/stores/{kind}/{name}` — Get one store

`GetWorkloadStore` · role **admin\|tenant:read** · audited: no

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `kind` | path | `StoreKind` | yes | The store kind (the binding type, e.g. `kv`). |
| `name` | path | string | yes | The store's name within the workload and kind: for KV, the binding's `id` (or the binding name when it has none). |

| Status | Body | Description |
|---|---|---|
| 200 | `Store` | The store. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `PUT /v1/tenants/{id}/workloads/{workload}/stores/{kind}/{name}` — Create a store (idempotent)

`PutWorkloadStore` · role **admin\|tenant:data** · audited: yes

Creates an empty store (`201`), or answers the existing one
(`200`). A binding's first write creates its store too, so this is
only needed to create one before the first deploy. Audited
(`kind: store_create`) when it creates.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `kind` | path | `StoreKind` | yes | The store kind (the binding type, e.g. `kv`). |
| `name` | path | string | yes | The store's name within the workload and kind: for KV, the binding's `id` (or the binding name when it has none). |

| Status | Body | Description |
|---|---|---|
| 200 | `Store` | The store already existed. |
| 201 | `Store` | Created. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `DELETE /v1/tenants/{id}/workloads/{workload}/stores/{kind}/{name}` — Delete a store and its data

`DeleteWorkloadStore` · role **admin\|tenant:data** · audited: yes

Deletes the journal and the store's data key (custody), so the
sealed entries can't be read again even from a backup of the
database. A running workload's store object notices on its next
sync (a new incarnation) and empties itself; a later write starts
a new, empty store. `404 unknown_store` if there is none. Audited
(`kind: store_delete`).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `kind` | path | `StoreKind` | yes | The store kind (the binding type, e.g. `kv`). |
| `name` | path | string | yes | The store's name within the workload and kind: for KV, the binding's `id` (or the binding name when it has none). |

| Status | Body | Description |
|---|---|---|
| 204 | — | Deleted. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}/workloads/{workload}/stores/{kind}/{name}/journal` — Read a store's journal after a sequence number

`ReadStoreJournal` · role **admin\|host\|tenant:data** · audited: no

The entries with `seq > after`, in order, for a store object
catching up (after a restart, on a new host, or when another
writer appended). Entries superseded by a later entry with the same
`ckey` are gone (compaction), so `seq` has gaps; the page is still
a complete catch-up from `after`. An unknown store is an empty page
with `head` 0 and no `incarnation`.

A host must give `epoch`: the data is released only to the holder
of the workload's lease at that epoch (`409 lease_lost`, as for
secrets). An admin reads without it (`tw kv key get`).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `kind` | path | `StoreKind` | yes | The store kind (the binding type, e.g. `kv`). |
| `name` | path | string | yes | The store's name within the workload and kind: for KV, the binding's `id` (or the binding name when it has none). |
| `epoch` | query | integer (int64) | no | Hosts: the workload's lease epoch the caller holds (required; `409 lease_lost`). Admins leave it out. |
| `after` | query | integer (int64) | no | Return entries with a larger `seq` (default 0). |
| `limit` | query | integer | no | At most this many entries (default and maximum 1000; a page also stops at about 4 MiB of data). |

| Status | Body | Description |
|---|---|---|
| 200 | `StoreJournal` | A page of entries. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The caller does not hold the namespace lease at this epoch. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/tenants/{id}/workloads/{workload}/stores/{kind}/{name}/journal` — Append entries to a store's journal (conditional on its head)

`AppendStoreJournal` · role **admin\|host\|tenant:data** · audited: no

The write path of a store object: it appends here **before** it
applies the change locally, so an acknowledged write is never only
on one host's disk. The append succeeds only if the journal's head
is still `after` (and `incarnation` matches when given); otherwise
`409 store_conflict`, and the writer catches up
(`GET …/journal?after=`) and retries. That makes the journal
linearizable across writers (a workload's replicas, an admin's
`tw kv key put`). An entry with a `ckey` supersedes every earlier
entry with the same `ckey`, which mgmt deletes in the same
transaction. The first append creates the store.

Entries are sealed at rest with the store's data key (AES-256-GCM,
custody); `ckey` is stored as an HMAC under a key derived from it.

A host must give `epoch` (lease holder at that epoch, `409
lease_lost`): a host that lost the workload can't write. Not
audited per append (stores are high volume); creation is
(`kind: store_create`). Limits: 128 entries and 4 MiB of data per
append, 2 MiB per entry, 1 GiB per store (`400 store_limit`).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `kind` | path | `StoreKind` | yes | The store kind (the binding type, e.g. `kv`). |
| `name` | path | string | yes | The store's name within the workload and kind: for KV, the binding's `id` (or the binding name when it has none). |
| `epoch` | query | integer (int64) | no | Hosts: the workload's lease epoch the caller holds (required; `409 lease_lost`). Admins leave it out. |

**Request** `application/json`: `StoreAppend`

| Status | Body | Description |
|---|---|---|
| 201 | `StoreAppendResult` | Appended. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | `lease_lost`: the host does not hold the workload's lease at this epoch. `store_conflict`: the journal's head (or incarnation) is not the one the writer gave; catch up and retry. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

## Telemetry

A tenant's OTLP export (ADR-045). The config, collector credentials included, lives in mgmt custody and is released only to a workload's lease holder.

### `GET /v1/tenants/{id}/telemetry` — Read the tenant's OTLP export (`tw telemetry`), without header values

`GetTenantTelemetry` · role **admin\|tenant:read** · audited: no

ADR-045. Header names only: no admin operation returns a
header value. A tenant that exports nothing has no `otlp`.
`404 unknown_tenant`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |

| Status | Body | Description |
|---|---|---|
| 200 | `TelemetryConfig` | The tenant's telemetry config. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `PUT /v1/tenants/{id}/telemetry` — Set the tenant's OTLP export (`tw telemetry set`)

`PutTenantTelemetry` · role **admin\|tenant:deploy** · audited: yes

Every workload of the tenant exports its request records (as log
records), its metrics and its spans with OTLP/HTTP (JSON encoding)
to `<endpoint>/v1/{logs,metrics,traces}`. The collector is dialed
**from the workload's own tailnet node**, so it must be a tailnet
peer the tailnet policy lets that workload reach (a public
internet endpoint is refused like any other egress). The config
is stored encrypted in mgmt custody (custody name
`tw/<tenant>/telemetry`); header values (collector credentials)
are never logged, audited or returned. Hosts pick a change up
within a minute. Audited (`kind: telemetry_put`, endpoint and
header names).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |

**Request** `application/json`: `TelemetryPut`

| Status | Body | Description |
|---|---|---|
| 200 | `TelemetryConfig` | The new config (header names only). |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `DELETE /v1/tenants/{id}/telemetry` — Stop the tenant's OTLP export (`tw telemetry rm`)

`DeleteTenantTelemetry` · role **admin\|tenant:deploy** · audited: yes

Hosts stop exporting within a minute. Records stay on the hosts
(`tw logs`) and in `/metrics`. Idempotent. Audited
(`kind: telemetry_delete`).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |

| Status | Body | Description |
|---|---|---|
| 204 | — | Deleted (or there was nothing to delete). |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/tenants/{id}/workloads/{workload}/telemetry-grants` — Release the tenant's OTLP export config to a workload's lease holder

`CreateTelemetryGrant` · role **host** · audited: yes

hostd calls this when it starts a workload, and every minute while
it runs it, to export the workload's logs, metrics and spans
(ADR-045). As with secrets (ADR-034), the config, header
values included, is released only to the host holding the
workload's lease at `epoch` (`409 lease_lost`), with
`Cache-Control: no-store`, and is never logged. `otlp` is absent
when the tenant exports nothing. Audited (`kind:
telemetry_release`, endpoint and header names) the first time a
host gets each version of the config for a workload (hosts re-read
it every minute); a refusal always is.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `workload` | path | string | yes | Workload name (a DNS label). |
| `epoch` | query | integer (int64) | yes | The namespace lease epoch the caller believes it holds. Fenced in the store (`409 lease_lost`). |

| Status | Body | Description |
|---|---|---|
| 201 | `TelemetryGrant` | The grant. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The caller does not hold the namespace lease at this epoch. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

## Queues

Cloudflare-style Queues (ADR-039). A queue belongs to the tenant; its messages are sealed rows here. Producers send through their host, push consumers' hosts claim batches with a visibility timeout and settle them, and admins can pull, peek, purge and delete.

### `GET /v1/tenants/{id}/queues` — List a tenant's queues

`ListQueues` · role **admin\|tenant:read** · audited: no

Every queue of the tenant, by name, with its backlog. A queue
exists once something sent to or pulled from it, or after
`PUT /v1/tenants/{id}/queues/{queue}`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `limit` | query | integer | no | Page size, 1–500 (default 50). Out-of-range values are `400 bad_request`. |
| `cursor` | query | string | no | Opaque page cursor from the previous page's `nextCursor`. Filters must be repeated unchanged. |

| Status | Body | Description |
|---|---|---|
| 200 | `QueuePage` | One page of queues. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}/queues/{queue}` — Get one queue and its backlog

`GetQueue` · role **admin\|host\|tenant:read** · audited: no

The queue's counters: messages kept (`backlog`), of which claimed by
a consumer and not settled (`inFlight`) and not yet visible
(`delayed`), their bytes, and the oldest message's send time. A
host reads it for a producer binding's `metrics()` (with
`workload` and `epoch`, as for a send).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `queue` | path | string | yes | The queue's name: lowercase letters, digits and dashes, at most 63 (Cloudflare's rule). |
| `workload` | query | string | no | Hosts: the workload the call acts for (its producer binding or consumer). Admins leave it out. |
| `epoch` | query | integer (int64) | no | Hosts: the workload's lease epoch the caller holds (required with `workload`; `409 lease_lost`). |

| Status | Body | Description |
|---|---|---|
| 200 | `Queue` | The queue. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The caller does not hold the namespace lease at this epoch. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `PUT /v1/tenants/{id}/queues/{queue}` — Create a queue (idempotent)

`PutQueue` · role **admin\|tenant:data** · audited: yes

Creates an empty queue (`201`) or answers the existing one (`200`).
A queue is also created by its first send or pull, and a
dead-letter queue by the first message moved to it. Audited
(`kind: queue_create`) when it creates.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `queue` | path | string | yes | The queue's name: lowercase letters, digits and dashes, at most 63 (Cloudflare's rule). |

| Status | Body | Description |
|---|---|---|
| 200 | `Queue` | The queue already existed. |
| 201 | `Queue` | Created. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `DELETE /v1/tenants/{id}/queues/{queue}` — Delete a queue and its messages

`DeleteQueue` · role **admin\|tenant:data** · audited: yes

Deletes every message and the queue's data key (custody), so the
sealed bodies can't be read again even from a database backup.
Consumers holding claimed messages find their lease IDs stale. A
later send creates a new, empty queue. `404 unknown_queue` if there
is none. Audited (`kind: queue_delete`).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `queue` | path | string | yes | The queue's name: lowercase letters, digits and dashes, at most 63 (Cloudflare's rule). |

| Status | Body | Description |
|---|---|---|
| 204 | — | Deleted. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/tenants/{id}/queues/{queue}/messages` — Read messages without claiming them

`PeekQueueMessages` · role **admin\|tenant:data** · audited: no

The oldest messages, bodies included, without changing them (no
lease, no attempt counted). This is how an operator inspects a
dead-letter queue (`tw queues messages <queue>`).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `queue` | path | string | yes | The queue's name: lowercase letters, digits and dashes, at most 63 (Cloudflare's rule). |
| `limit` | query | integer | no | At most this many messages (default 20, maximum 100). |

| Status | Body | Description |
|---|---|---|
| 200 | `QueueMessageList` | The messages, oldest first. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/tenants/{id}/queues/{queue}/messages` — Send messages to a queue

`SendQueueMessages` · role **admin\|host\|tenant:data** · audited: no

A producer's `send()` / `sendBatch()` (through its host), or an
operator's `tw queues send`. The messages are durable in mgmt's
database when this answers. Each gets a random ID. A host must give
`workload` and `epoch` (the lease it holds, `409 lease_lost`), and
the workload's latest deployment must bind the queue as a producer
(`403 forbidden` otherwise). Not audited (volume). Limits: 100
messages and 256 KB per call, 128 KB per message, `delaySeconds` ≤
43200 (`400 queue_limit`); 1 GiB kept per queue.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `queue` | path | string | yes | The queue's name: lowercase letters, digits and dashes, at most 63 (Cloudflare's rule). |
| `workload` | query | string | no | Hosts: the workload the call acts for (its producer binding or consumer). Admins leave it out. |
| `epoch` | query | integer (int64) | no | Hosts: the workload's lease epoch the caller holds (required with `workload`; `409 lease_lost`). |

**Request** `application/json`: `QueueSend`

| Status | Body | Description |
|---|---|---|
| 201 | `QueueSendResult` | Sent. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The caller does not hold the namespace lease at this epoch. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/tenants/{id}/queues/{queue}/messages/ack` — Settle claimed messages (ack, retry, extend)

`AckQueueMessages` · role **admin\|host\|tenant:data** · audited: no

`acks` deletes messages; `release` hands them back uncounted (the
consumer never ran them); `retries` makes them visible again after
`delaySeconds` (default: the consumer's `retry_delay`), or moves
them to the dead-letter queue (or drops them) once they have been
delivered more than `maxRetries` times; `extend` keeps them hidden
for another `visibilityTimeoutMs` (a long handler). A lease ID that
is no longer current (its timeout passed and the message was
claimed again, or settled) changes nothing and is counted in
`stale`. Host rules as for `…/pull`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `queue` | path | string | yes | The queue's name: lowercase letters, digits and dashes, at most 63 (Cloudflare's rule). |
| `workload` | query | string | no | Hosts: the workload the call acts for (its producer binding or consumer). Admins leave it out. |
| `epoch` | query | integer (int64) | no | Hosts: the workload's lease epoch the caller holds (required with `workload`; `409 lease_lost`). |

**Request** `application/json`: `QueueAck`

| Status | Body | Description |
|---|---|---|
| 200 | `QueueAckResult` | Settled. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The caller does not hold the namespace lease at this epoch. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/tenants/{id}/queues/{queue}/messages/pull` — Claim a batch of messages (push consumers and HTTP pull)

`PullQueueMessages` · role **admin\|host\|tenant:data** · audited: no

Claims up to `batchSize` visible messages, oldest first: each is
hidden for `visibilityTimeoutMs`, gets a new `leaseId` and counts an
attempt. Settle them with `…/messages/ack` before the timeout, or
they become visible again (at-least-once). If none is visible, waits
up to `waitMs` for one. A message that already failed more than
`maxRetries` times and whose last lease expired is moved to the
dead-letter queue (or dropped) instead of being delivered again.

A host (a push consumer's hostd) must give `workload` and `epoch`;
the workload's latest deployment must declare a consumer of the
queue, and its `max_retries` and `dead_letter_queue` apply (the
body's are ignored). An admin is an HTTP pull consumer and gives
them in the body.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `queue` | path | string | yes | The queue's name: lowercase letters, digits and dashes, at most 63 (Cloudflare's rule). |
| `workload` | query | string | no | Hosts: the workload the call acts for (its producer binding or consumer). Admins leave it out. |
| `epoch` | query | integer (int64) | no | Hosts: the workload's lease epoch the caller holds (required with `workload`; `409 lease_lost`). |

**Request** `application/json`: `QueuePull`

| Status | Body | Description |
|---|---|---|
| 200 | `QueuePullResult` | The claimed messages (possibly none). |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The caller does not hold the namespace lease at this epoch. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/tenants/{id}/queues/{queue}/purge` — Delete every message of a queue, keeping the queue

`PurgeQueue` · role **admin\|tenant:data** · audited: yes

Deletes all messages, in flight or not. Audited
(`kind: queue_purge`, with the count).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Tenant ID (`tn_…` when generated; one safe path element). |
| `queue` | path | string | yes | The queue's name: lowercase letters, digits and dashes, at most 63 (Cloudflare's rule). |

| Status | Body | Description |
|---|---|---|
| 200 | `QueuePurgeResult` | Purged. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

## Audit

Hash-chained, append-only audit log.

### `GET /v1/audit` — Query audit records

`ListAudit` · role **admin\|tenant:read** · audited: no

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `limit` | query | integer | no | Page size, 1–500 (default 50). Out-of-range values are `400 bad_request`. |
| `cursor` | query | string | no | Opaque page cursor from the previous page's `nextCursor`. Filters must be repeated unchanged. |
| `object` | query | string | no | Records about this object ID. |
| `human` | query | string | no | Records whose `actingFor` is this principal (the human at the head of a chain). |
| `trace` | query | string | no |  |
| `tenant` | query | string | no |  |
| `chain` | query | string | no | `mgmt` or `host:<hostID>`. |
| `kind` | query | array of `AuditKind` | no |  |
| `since` | query | string | no | Records at or after this time (RFC 3339 or unix ms). |
| `until` | query | string | no |  |
| `order` | query | string: `asc` \| `desc` | no | `asc` (default): oldest first. `desc`: newest first, as the admin UI shows activity. A cursor continues in the order of the page that returned it, so repeat `order` with the cursor. |

| Status | Body | Description |
|---|---|---|
| 200 | `AuditPage` | One page, ordered by `(ts, chain, seq)` (reversed with `order=desc`), with per-chain verification of the records returned. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/audit` — Append a batch of host audit records

`AppendAudit` · role **host** · audited: yes

Append-only. Each host has its own chain `host:<hostID>`; every
record carries `prevHash`, `hash` and `hostSig`
(`hash = hex(sha256(canonical body without hash/hostSig))`,
`hostSig = base64(ed25519.Sign(hostKey, utf8(hash)))`, verified
against the registered `auditPubKey`). `kind` and `hash` are required
on every record. Provenance (RT-02): `source: tenant` is allowed only
with `kind: tenant_note` and vice versa; `source: mgmt` is refused;
a missing source means `host`. The store refuses UPDATE/DELETE on
audit rows. Mutating by nature; it *is* the audit.

**Request** `application/json`: `AuditBatch`

| Status | Body | Description |
|---|---|---|
| 201 | `AuditAppendResult` | Appended; the chain status after the append. |
| 400 | `Error` | A record is malformed, breaks the chain, or claims a forbidden source. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/audit/verification` — Verify every audit chain

`GetAuditVerification` · role **admin** · audited: no

Recomputes every hash and link of every chain and checks each chain head; detects modified, removed, reordered and truncated records.

| Status | Body | Description |
|---|---|---|
| 200 | `AuditVerification` | The verification report (`ok` is the AND over chains). |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

## Upgrades

Releases and upgrades (ADR-041). tw-mgmt keeps a release catalog (the
releases it was built with, plus imported ones), plans upgrades from it and the fleet's
reported versions and features, and runs fleet upgrades as durable upgrade runs.

### `GET /v1/releases` — The release catalog

`ListReleases` · role **admin** · audited: no

Every release this tw-mgmt knows: the ones built into it (its own and every earlier
release) and imported ones (`POST /v1/releases`), ascending by version, with each one's
notes, contract migrations, protocols and workerd pin. `current` is this replica's version.

| Status | Body | Description |
|---|---|---|
| 200 | `ReleaseCatalog` | The catalog. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/releases` — Import catalog entries (from a release's share/tailworks/catalog.json)

`ImportReleases` · role **admin** · audited: yes

Adds or replaces imported entries. Entries built into this tw-mgmt are never replaced.
`tw release import <tarball>` reads the tarball's `share/tailworks/catalog.json`, posts it here,
then uploads the tarball (`PUT /v1/releases/{version}/artifacts/{name}`). Audited
`release_import`.

**Request** `application/json`: `ReleaseImport`

| Status | Body | Description |
|---|---|---|
| 200 | `ReleaseCatalog` | The catalog after the import. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/releases/{version}/artifacts/{name}` — Download a release tarball (hosts upgrading themselves)

`GetReleaseArtifact` · role **admin\|host** · audited: no

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `version` | path | string | yes |  |
| `name` | path | string | yes | `tailworks-<version>-<os>-<arch>.tar.gz` or `SHA256SUMS`. |

| Status | Body | Description |
|---|---|---|
| 200 | string (binary) | The file. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `PUT /v1/releases/{version}/artifacts/{name}` — Upload a release tarball (or its SHA256SUMS)

`PutReleaseArtifact` · role **admin** · audited: yes

Upload `SHA256SUMS` first: a tarball is accepted only if its SHA-256 matches its line there.
Stored in tw-mgmt's release directory (`-release-dir`, default `<state-dir>/releases`);
replicas that don't share that directory each need the upload. Audited `release_artifact`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `version` | path | string | yes |  |
| `name` | path | string | yes | `tailworks-<version>-<os>-<arch>.tar.gz` or `SHA256SUMS`. |

**Request** `application/octet-stream`: string (binary)

| Status | Body | Description |
|---|---|---|
| 201 | `ReleaseArtifact` | Stored. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/upgrades` — Upgrade runs, newest first

`ListUpgrades` · role **admin** · audited: no

| Status | Body | Description |
|---|---|---|
| 200 | object | The runs. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/upgrades` — Plan an upgrade, or start an upgrade run

`CreateUpgrade` · role **admin** · audited: yes

Plans the upgrade to `to` from the release catalog and the fleet's reported versions and
features. With `dryRun` the plan is returned and nothing is stored. Otherwise a durable
upgrade run is created (`201`) and driven by whichever replica holds its lease: wait for
every mgmt replica to report `to`, apply deferred contract migrations, then each host in
turn (canary first, then a soak): cordon, drain what can move, push the upgrade to the host
(it downloads its tarball from `GET /v1/releases/{version}/artifacts/{name}`, installs it
and restarts), wait for it to re-register at `to` and settle, uncordon. A failed gate
pauses the run. Only one run may be active (`409 exists`). Audited `upgrade_start`.

**Request** `application/json`: `UpgradeRequest`

| Status | Body | Description |
|---|---|---|
| 200 | `UpgradePlan` | The plan (`dryRun`). |
| 201 | `UpgradeRun` | The run. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 409 | `Error` | The request conflicts with the resource's current state. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/upgrades/{id}` — One upgrade run

`GetUpgrade` · role **admin** · audited: no

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes |  |

| Status | Body | Description |
|---|---|---|
| 200 | `UpgradeRun` | The run. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/upgrades/{id}/actions` — Pause, resume or abort an upgrade run

`ActOnUpgrade` · role **admin** · audited: yes

`pause` stops before the next step; `resume` continues a paused run (after the operator
fixed what paused it); `abort` ends it (hosts already upgraded stay upgraded; a cordoned
host in progress is uncordoned). Audited `upgrade_pause`, `upgrade_resume`, `upgrade_abort`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes |  |

**Request** `application/json`: object

| Status | Body | Description |
|---|---|---|
| 200 | `UpgradeRun` | The run. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The request conflicts with the resource's current state. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

## Volumes

dm-crypt data keys for DO volumes (microVM tier).

### `POST /v1/volumes/{id}/keys` — Release a volume's dm-crypt data key to the lease holder

`CreateVolumeKeyGrant` · role **host** · audited: yes

Creates a key grant: the 256-bit data key of the `(tenant, workload)`
DO volume (custody name `tw/<tenant>/volumes/<workload>/dek`,
created on first use) is released only to the host holding the
workload's lease at `epoch` (`409 lease_lost`). Audited
(`kind: volume_key`). `Cache-Control: no-store`. The key is never
logged. Replaces `GET /v0/volumes/key`.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Volume ID `<tenantID>:<workload>` (one volume per workload; `:` is a legal path character). |
| `epoch` | query | integer (int64) | yes | The namespace lease epoch the caller believes it holds. Fenced in the store (`409 lease_lost`). |

| Status | Body | Description |
|---|---|---|
| 201 | `VolumeKeyGrant` | The grant. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| 409 | `Error` | The caller does not hold the namespace lease at this epoch. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

## Containers

Container and code-sandbox reservations against tenant quotas (microVM tier, ADR-032).

### `GET /v1/container-reservations` — List container reservations

`ListContainerReservations` · role **admin\|host** · audited: no

Hosts see their own reservations only.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `limit` | query | integer | no | Page size, 1–500 (default 50). Out-of-range values are `400 bad_request`. |
| `cursor` | query | string | no | Opaque page cursor from the previous page's `nextCursor`. Filters must be repeated unchanged. |
| `tenant` | query | string | no |  |
| `host` | query | string | no |  |

| Status | Body | Description |
|---|---|---|
| 200 | `ContainerReservationPage` | One page of reservations, ordered by `(createdAt, reservationID)`. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/container-reservations` — Reserve quota for a container before it starts

`CreateContainerReservation` · role **host** · audited: yes

hostd calls this synchronously at `ctx.container.start()`, after its
own capacity check (`503 capacity_exhausted` is hostd's, never
mgmt's) and before it boots the container's VM. Only the host holding
the workload's lease at `epoch` may reserve (`409 lease_lost`).

The key is `(tenantID, workload, class, object)`: one container per
DO instance (ADR-032 D2). Reserving a key that already holds a
reservation is **idempotent** (`200`, the same `reservationID`; a
larger container re-checks the quota for the difference), so a
restart never needs new headroom, and a host that took the workload
over adopts the key. A new key is checked atomically, in the store,
against the tenant's `maxContainers` (running), `maxContainerMemMiB`
and `maxContainerVCPU`, and the class's `max_instances` from the
deployment: over any of them is `429 container_quota_exceeded`
(`missing: {quota, limit, used, requested}`). Containers are not
objects: they count against neither `maxObjects` nor a host's
`capacityNodes`.

hostd **fails closed** when mgmt is unreachable: no reservation, no
start, except a restart of a container whose reservation it already
holds. Released by `DELETE` on stop; a reservation that no heartbeat
reports (`HostStatus.containers`) for 30 s after its creation is
released by mgmt, and one whose host stopped reporting it for 2 min no
longer counts, both on the store clock. Audited
(`kind: container_reserve`).

**Request** `application/json`: `ContainerReservationCreate`

| Status | Body | Description |
|---|---|---|
| 200 | `ContainerReservation` | The key already held a reservation (restart, or a takeover by the lease holder); it is returned, renewed. |
| 201 | `ContainerReservation` | Reserved. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | Unknown tenant (`unknown_tenant`). |
| 409 | `Error` | The caller does not hold the namespace lease at this epoch. |
| 429 | `Error` | Over a container quota (`container_quota_exceeded`). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `DELETE /v1/container-reservations/{id}` — Release a container reservation (the container stopped)

`DeleteContainerReservation` · role **host\|admin** · audited: yes

Only the holding host (or an admin) may release it (`403 forbidden`).
Idempotent from the host's side: `404` means it is already gone.
Audited (`kind: container_release`).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Container reservation ID (`cr_…`). |

| Status | Body | Description |
|---|---|---|
| 204 | — | Released. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

## Images

Container images (ADR-032 D3) — public base images shared across tenants, and the arm64 alias map.

### `GET /v1/images/aliases` — The image alias map

`ListImageAliases` · role **admin\|host\|tenant:read** · audited: no

Upstream refs swapped for another image on one arch (ADR-032 D3: Cloudflare publishes
`cloudflare/sandbox` for linux/amd64 only; arm64 hosts get tailworks' rebuild from the Apache-2.0
source, in `public/`). Deploys apply it when pinning; `tw deploy` applies it as BuildKit named
contexts when it builds a Dockerfile, so app source never changes.

| Status | Body | Description |
|---|---|---|
| 200 | `ImageAliasList` | The aliases. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/images/public` — List public base images

`ListPublicImages` · role **admin\|host\|tenant:read** · audited: no

Operator-marked public bases (ADR-032 D3): images every tenant's deploy may pin without a
per-tenant mirror, and every host caches once for all tenants (hostd's `public` cache
scope). Everything else is cached per tenant, so a cache hit never tells one tenant which
images another uses.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `limit` | query | integer | no | Page size, 1–500 (default 50). Out-of-range values are `400 bad_request`. |
| `cursor` | query | string | no | Opaque page cursor from the previous page's `nextCursor`. Filters must be repeated unchanged. |

| Status | Body | Description |
|---|---|---|
| 200 | `PublicImagePage` | One page of public images, ordered by `(createdAt, id)`. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `POST /v1/images/public` — Mark an image as a public base (mirrors it now)

`CreatePublicImage` · role **admin** · audited: yes

`tw image public add <ref>`. An upstream ref (`docker.io/cloudflare/sandbox:0.12.14`) is
mirrored now through mgmt's egress into `public/<registry>/<repository>` and pinned by digest
for each arch (default: every linux amd64/arm64 image it has); a `tw.local/<name>` ref names
an image already pushed to `public/<name>` (`tw image push`). Re-adding refreshes the
digest; deployments already pinned keep theirs. Audited (`kind: image_public`).

**Request** `application/json`: `PublicImageCreate`

| Status | Body | Description |
|---|---|---|
| 201 | `PublicImageList` | Marked; one entry per arch. |
| 400 | `Error` | Malformed request, body, filter or cursor. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 409 | `Error` | This mgmt has no registry, or does not mirror (`registry_unavailable`, `mirror_disabled`). |
| 502 | `Error` | The upstream image could not be mirrored (`image_unavailable`). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `DELETE /v1/images/public/{id}` — Unmark a public base image

`DeletePublicImage` · role **admin** · audited: yes

New deploys then mirror the ref into each tenant's namespace; pinned deployments keep pulling what they pinned. Audited (`kind: image_public`).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `id` | path | string | yes | Public image entry ID (`pi_…`). |

| Status | Body | Description |
|---|---|---|
| 204 | — | Unmarked. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| 404 | `Error` | No such resource (or none the caller may see). |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

## Mcp

tailworks' own MCP server (ADR-035), served by tw-mgmt at `/mcp` on the same listeners and
behind the same WhoIs gate as `/v1`. Its wire protocol is the external Model Context Protocol
(Streamable HTTP, stateless: one JSON-RPC request per POST, answered with `application/json`);
this entry documents the route and who may call it, and is excluded from code generation (the
server is hand-written: `mgmt/mcp.go`). Tools run the `/v1` operations in process.

- **admins** (`github.com/rosskukulinski/tailworks/cap/admin`) and holders of `github.com/rosskukulinski/tailworks/cap/mcp` get the read tools:
  `whoami`, `status`, `ls`, `logs`, `audit`. A caller with only `github.com/rosskukulinski/tailworks/cap/mcp` reaches
  `/mcp` and nothing else.
- `deploy`, `kill` and `undeploy` need a `github.com/rosskukulinski/tailworks/cap/mcp` grant naming them
  (`[{"tools": ["kill"]}]`, or `"*"`); being an admin is not enough.
- the local operator (the `tw mcp` bridge on the tailworks host) gets the read tools, or exactly
  the tools it lists in `Tw-MCP-Tools`; for tailnet callers that header only narrows.
- hosts and browsers are refused.

### `GET /mcp` — Not supported (stateless server; no server-to-client stream)

`MCPStream` · role **admin\|mcp** · audited: no

| Status | Body | Description |
|---|---|---|
| 405 | — | Use POST. |

### `POST /mcp` — One MCP JSON-RPC request (initialize, ping, tools/list, tools/call) or notification

`MCPRequest` · role **admin\|mcp** · audited: yes

The body is one JSON-RPC 2.0 message (no batches). A request is answered `200` with the
JSON-RPC response; a notification (no `id`) with `202` and no body. A tool the caller isn't
granted is a tool result with `isError: true` and the tailworks error `cap_missing`, whose
`snippet` is the grant (or the `tw mcp --allow` flag) that adds it. `kill`, `undeploy` and
`deploy` are audited like the `/v1` operations they run, with the caller as the actor.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `Tw-MCP-Tools` | header | string | no | Comma-separated tools this request may use (narrows; see the tag description). |

**Request** `application/json`: object

| Status | Body | Description |
|---|---|---|
| 200 | object | The JSON-RPC response. |
| 202 | — | A notification was accepted. |
| 401 | `Error` | No identity on the connection (WhoIs failed), or the local-dev socket was used without `Tw-Host-Token`. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |

## Registry

tailworks' OCI distribution registry (ADR-032 D3), served by tw-mgmt at `/v2/` on the same listeners
and behind the same WhoIs gate as `/v1`. Its wire protocol is the external OCI Distribution Spec v1.1
(push and pull subset); these entries document the routes and the roles tailworks enforces, and are
excluded from code generation (the registry is hand-written: `internal/registry`).

- `{name}` is a repository and may contain `/`: `<tenantID>/<path>` (one namespace per tenant) or
  `public/<path>` (operator base images and alias targets).
- **admins** push and pull every namespace; **hosts** only pull, and only `public/…` and the
  namespaces of tenants they hold a deployment of (`403 DENIED` otherwise).
- Blobs are content-addressed and stored once, but visible only through a repository they were
  pushed or mirrored into: another namespace answers `404 BLOB_UNKNOWN` exactly as for absent
  content, cross-namespace mounts are refused, and a manifest may only reference content already
  in its own repository.
- Errors are the spec's `{"errors": [{code, message}]}`; the body also carries tailworks' `code`
  (`forbidden`, `not_found`, `bad_request`) and `Tw-Error`.

### `GET /v2/` — OCI API version check

`RegistryBase` · role **admin\|host\|tenant:read** · audited: no

`200 {}` with `Docker-Distribution-API-Version: registry/2.0` (clients probe it before pushing or pulling). Authentication is WhoIs on the connection: there is no token challenge.

| Status | Body | Description |
|---|---|---|
| 200 | — | The registry speaks the distribution API and admits the caller. |
| 401 | `Error` | No identity on the connection (WhoIs failed), or the local-dev socket was used without `Tw-Host-Token`. |

### `POST /v2/{name}/blobs/uploads/` — Start a blob upload (or a monolithic upload, or a mount)

`RegistryStartUpload` · role **admin\|tenant:deploy** · audited: no

`202` with `Location: /v2/{name}/blobs/uploads/{uuid}`. With `?digest=` and a body: a
monolithic upload (`201`). With `?mount=<digest>&from=<repo>`: links the blob from `from`
(`201`) only when `from` is in the same namespace and has it; otherwise a fresh upload
(`202`), so another tenant's content is never borrowed. Upload sessions are files under
mgmt's registry dir (any replica sharing it can continue one).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes | A repository: `<tenantID>/<path>` or `public/<path>` (OCI name grammar; may contain `/`). |
| `digest` | query | string | no |  |
| `mount` | query | string | no |  |
| `from` | query | string | no |  |

| Status | Body | Description |
|---|---|---|
| 201 | — | Monolithic upload or mount done. |
| 202 | — | Upload started; `Location`, `Docker-Upload-UUID`, `Range: 0-0`. |
| 403 | — | `DENIED`. |

### `GET /v2/{name}/blobs/uploads/{uuid}` — Upload progress

`RegistryUploadStatus` · role **admin\|tenant:deploy** · audited: no

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes | A repository: `<tenantID>/<path>` or `public/<path>` (OCI name grammar; may contain `/`). |
| `uuid` | path | string | yes |  |

| Status | Body | Description |
|---|---|---|
| 204 | — | `Range: 0-<last byte>`. |
| 404 | — | `BLOB_UPLOAD_UNKNOWN` (also for another repository's session). |

### `PUT /v2/{name}/blobs/uploads/{uuid}` — Finish an upload (`?digest=` required; optional final chunk as the body)

`RegistryFinishUpload` · role **admin\|tenant:deploy** · audited: no

The whole upload must hash to `digest` (`400 DIGEST_INVALID`, and nothing is linked).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes | A repository: `<tenantID>/<path>` or `public/<path>` (OCI name grammar; may contain `/`). |
| `uuid` | path | string | yes |  |
| `digest` | query | string | yes |  |

| Status | Body | Description |
|---|---|---|
| 201 | — | Stored and linked into the repository; `Location`, `Docker-Content-Digest`. |
| 400 | — | `DIGEST_INVALID`. |

### `PATCH /v2/{name}/blobs/uploads/{uuid}` — Upload a chunk

`RegistryUploadChunk` · role **admin\|tenant:deploy** · audited: no

`Content-Range: <start>-<end>` is optional; when given, `start` must equal the bytes received so far (`416` otherwise).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes | A repository: `<tenantID>/<path>` or `public/<path>` (OCI name grammar; may contain `/`). |
| `uuid` | path | string | yes |  |

| Status | Body | Description |
|---|---|---|
| 202 | — | Accepted; `Range: 0-<last byte>`. |
| 416 | — | `BLOB_UPLOAD_INVALID`: out-of-order chunk. |

### `DELETE /v2/{name}/blobs/uploads/{uuid}` — Cancel an upload

`RegistryCancelUpload` · role **admin\|tenant:deploy** · audited: no

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes | A repository: `<tenantID>/<path>` or `public/<path>` (OCI name grammar; may contain `/`). |
| `uuid` | path | string | yes |  |

| Status | Body | Description |
|---|---|---|
| 204 | — | Cancelled. |

### `GET /v2/{name}/blobs/{digest}` — Pull a blob (Range supported)

`RegistryGetBlob` · role **admin\|host\|tenant:read** · audited: no

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes | A repository: `<tenantID>/<path>` or `public/<path>` (OCI name grammar; may contain `/`). |
| `digest` | path | string | yes |  |

| Status | Body | Description |
|---|---|---|
| 200 | — | The content; it hashes to `digest`. |
| 403 | — | `DENIED`. |
| 404 | — | `BLOB_UNKNOWN`: not in this repository (whether or not another namespace has it). |

### `GET /v2/{name}/manifests/{reference}` — Pull a manifest or index (by tag or digest)

`RegistryGetManifest` · role **admin\|host\|tenant:read** · audited: no

`Content-Type` is the manifest's media type; `Docker-Content-Digest` its digest. `HEAD` answers the same headers without the body.

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes | A repository: `<tenantID>/<path>` or `public/<path>` (OCI name grammar; may contain `/`). |
| `reference` | path | string | yes | A tag or a `sha256:` digest. |

| Status | Body | Description |
|---|---|---|
| 200 | — | The manifest bytes, exactly as pushed. |
| 403 | — | `DENIED`: a host pulling a tenant namespace it serves no deployment of. |
| 404 | — | `MANIFEST_UNKNOWN`. |

### `PUT /v2/{name}/manifests/{reference}` — Push a manifest or index (by tag or digest)

`RegistryPutManifest` · role **admin\|tenant:deploy** · audited: yes

Image manifests (OCI and Docker v2 schema 2) and indexes (multi-arch). Every blob (config,
layers) or child manifest it references must already be in **this** repository
(`400 MANIFEST_BLOB_UNKNOWN`). A digest reference must match the body's digest. Audited
(`kind: image_push`).

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes | A repository: `<tenantID>/<path>` or `public/<path>` (OCI name grammar; may contain `/`). |
| `reference` | path | string | yes | A tag or a `sha256:` digest. |

| Status | Body | Description |
|---|---|---|
| 201 | — | Stored; `Location` and `Docker-Content-Digest` name it. |
| 400 | — | `MANIFEST_INVALID`, `MANIFEST_BLOB_UNKNOWN`, `DIGEST_INVALID`, `TAG_INVALID`. |
| 403 | — | `DENIED` (hosts never push). |

### `GET /v2/{name}/tags/list` — List a repository's tags (`?n=` / `?last=` paging)

`RegistryListTags` · role **admin\|host\|tenant:read** · audited: no

| Parameter | In | Type | Required | Description |
|---|---|---|---|---|
| `name` | path | string | yes | A repository: `<tenantID>/<path>` or `public/<path>` (OCI name grammar; may contain `/`). |
| `n` | query | integer | no |  |
| `last` | query | string | no |  |

| Status | Body | Description |
|---|---|---|
| 200 | — | `{name, tags}`. |

## Operational

Health, status, identity, maintenance.

### `POST /v1/device-delete-runs` — Run the scheduled device deletes now

`CreateDeviceDeleteRun` · role **admin** · audited: yes

Kill tier 2 is deferred by `-device-delete-delay`; this creates a run
that executes every pending delete immediately (what `tw down` does
before stopping mgmt). Each delete is audited (`kind: device_delete`).
Replaces `POST /v0/flush`.

| Status | Body | Description |
|---|---|---|
| 201 | `DeviceDeleteRun` | The run's outcome. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/healthz` — Liveness

`GetHealthz` · role **any** · audited: no

Any authenticated principal (hosts included). On the local-dev socket the host token is still required (fail closed).

| Status | Body | Description |
|---|---|---|
| 200 | `Health` | Alive. |
| 401 | `Error` | No identity on the connection (WhoIs failed), or the local-dev socket was used without `Tw-Host-Token`. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/status` — mgmt status

`GetStatus` · role **admin** · audited: no

| Status | Body | Description |
|---|---|---|
| 200 | `Status` | Counters and the transport mgmt is serving on. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

### `GET /v1/whoami` — Who the caller is, as mgmt sees it

`WhoAmI` · role **any** · audited: no

`tw link` uses it to learn the endpoint and refuse non-admins early. Any authenticated principal, hosts included.

| Status | Body | Description |
|---|---|---|
| 200 | `WhoAmI` | The caller's principal and mgmt's self-description. |
| 403 | `Error` | The caller is authenticated but lacks the role, or a host acts outside its own records/namespaces. |
| default | `Error` | Unexpected error. The body is always the typed catalog shape; `Tw-Error` carries the code. |

## Components

### Security schemes

#### `localHostToken` (apiKey)

`--local-dev` only (`tw dev`, hermetic tests): the unix socket
`~/.tailworks/run/mgmt.sock` with the 256-bit token from
`~/.tailworks/hostd/mgmt.token` (RT-03). Every request needs it, `/v1/healthz`
included; a mgmt without a token answers `503 host_token_unconfigured`.
The host names itself with `Tw-Host-ID`; admins may set `Tw-Actor`.
Never used in normal operation; `tw doctor` flags it.

#### `tailnetWhoIs` (mutualTLS)

**Not a token.** The caller's identity is the Tailscale node behind the
connection, resolved by WhoIs on mgmt's tsnet listener. No header is
sent. Roles: `host` = the peer carries `tag:tw-host` (bound to a
hostID at registration); `admin` = the peer's WhoIs CapMap holds
`github.com/rosskukulinski/tailworks/cap/admin` toward `tag:tw-mgmt`. `mutualTLS` is the closest
OpenAPI scheme type (identity comes from the transport, not from a
credential in the request); the actual mechanism is WireGuard node
identity + WhoIs, and the `:80` listener is tailnet-only plaintext.

### Schemas

#### `Approval`

One approval wait of one Workflow instance, as the host running it records it.

| Field | Type | Required | Description |
|---|---|---|---|
| `approver` | `Approver` | no | The verified tailnet identity that decided an approval (WireGuard, never the request body). |
| `comment` | string | no |  |
| `decidedAt` | string (date-time) | no |  |
| `facts` | object | no | The instance's numeric params, which grant `limits` are checked against. |
| `host` | string | no | The host that runs the instance (set by mgmt). |
| `instance` | string | yes |  |
| `requestedAt` | string (date-time) | yes |  |
| `status` | string: `pending` \| `approved` \| `rejected` \| `timeout` | yes |  |
| `step` | string | yes | The `waitForEvent` step name (`wait-for-approval` for `waitForApproval`). |
| `tenantID` | string | yes |  |
| `timeout` | string | no | The wait's timeout as the Workflow gave it. |
| `type` | string | yes | The event type waited for: `approval` or `approval-*`. |
| `workflow` | string | yes |  |
| `workload` | string | yes |  |

#### `ApprovalDecisionRequest`

| Field | Type | Required | Description |
|---|---|---|---|
| `comment` | string | no | Shown to the Workflow (`comment`; a rejection's `reason`) and audited. |
| `decision` | string: `approve` \| `reject` | yes |  |
| `metadata` | object | no | Extra fields for the Workflow (`waitForApproval` returns them). An `approver` key is replaced by tailworks'. |
| `step` | string | no | Which wait, when the instance waits for several. |

#### `ApprovalDecisionResponse`

| Field | Type | Required | Description |
|---|---|---|---|
| `approval` | `Approval` | yes | One approval wait of one Workflow instance, as the host running it records it. |

#### `ApprovalList`

| Field | Type | Required | Description |
|---|---|---|---|
| `approvals` | array of `Approval` | yes |  |
| `unreachable` | array of string | no | Hosts that did not answer (their approvals are missing). |

#### `ApproveGrant`

One value of a `github.com/rosskukulinski/tailworks/cap/approve` grant. A missing field does not narrow; `"*"` in a list matches anything.

| Field | Type | Required | Description |
|---|---|---|---|
| `decisions` | array of string | no |  |
| `limits` | object | no |  |
| `steps` | array of string | no |  |
| `workflows` | array of string | no |  |
| `workloads` | array of string | no |  |

#### `Approver`

The verified tailnet identity that decided an approval (WireGuard, never the request body).

| Field | Type | Required | Description |
|---|---|---|---|
| `node` | string | yes | MagicDNS name. |
| `nodeId` | string | yes | Stable node ID. |
| `tags` | array of string | yes |  |
| `user` | string | no | Login (untagged nodes). |
| `via` | string: `tailnet` \| `mgmt` | yes | `tailnet`: sent to the workload's (or object's) own node; `mgmt`: `tw approve` or the admin UI. |

#### `Arch`

A Go GOARCH. Container images must match their host's.

Enum: `amd64`, `arm64`

#### `Assignment`

What a host must run; `bundleURL` is `GET /v1/deployments/{id}/bundle`.

| Field | Type | Required | Description |
|---|---|---|---|
| `bundleURL` | string (uri-reference) | yes |  |
| `containers` | array of `ContainerClass` | no | The workload's container classes as mgmt resolved them at deploy (ADR-032 D3): the image refs `ctx.container` sees and their per-arch pins. hostd starts a container only from the pin for its own arch, pulled by digest from tailworks' registry. |
| `controlURL` | string (uri) | no |  |
| `createdAt` | integer (int64) | yes |  |
| `deploymentID` | string | yes |  |
| `epoch` | integer (int64) | yes |  |
| `fence` | boolean | no | Stop serving this workload when no heartbeat to mgmt has succeeded for the host's fence window (30 s): it has Durable Objects that must not run on two hosts (ADR-028 §3). Stateless workloads and `"fence": "never"` classes are false and keep serving. Hosts treat a missing field as true. |
| `lease` | string | yes |  |
| `maxDepth` | integer | no |  |
| `maxLive` | integer | no |  |
| `namespaces` | array of `ClassConfig` | yes |  |
| `placementTargets` | array of string | no | Smart placement targets the host should measure paths to (`PUT /v1/hosts/{id}/paths`). |
| `public` | `PublicAssignment` | no | A workload's public ingress (ADR-029). The host runs one extra node for it (namespace `_public`, object name = the workload, hostname = the workload name, `tags`) whose Funnel listener serves `routes` while `exposed`. |
| `service` | string | no | The Tailscale Service the workload entry node advertises while the deployment is active and healthy; the host withdraws it on drain, undeploy, fencing, kill, or failed health checks. |
| `tailnet` | string | yes |  |
| `tenantID` | string | yes |  |
| `tier` | `Tier` | no |  |
| `workload` | string | yes |  |
| `workloadTags` | array of string | no |  |

#### `AssignmentPage`

Extends `Page`.

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | array of `Assignment` | no |  |

#### `AuditAppendResult`

| Field | Type | Required | Description |
|---|---|---|---|
| `accepted` | integer | yes |  |
| `chain` | `ChainStatus` | yes |  |

#### `AuditBatch`

| Field | Type | Required | Description |
|---|---|---|---|
| `records` | array of `AuditRecord` | yes |  |

#### `AuditKind`

Host chain (netd/hostd):
`allow`,
`deny`,
`egress`,
`spawn`,
`kill`,
`principal`,
`tenant_note` (source `tenant`, unverified),
`container_start` (an object's container, ADR-032),
`container_stop` (an object's container, ADR-032),
`audit_dropped` (records refused before they were chained, e.g. tenant notes over their rate during a flood; `detail.n` and `detail.byReason` count them, so the gap is visible and the chain still verifies).
mgmt chain:
`tenant_create`,
`tenant_update`,
`tenant_delete`,
`tenant_purge_host` (a host removed a deleted tenant's files),
`host_register`,
`host_delete`,
`host_cordon`,
`host_uncordon`,
`host_drain`,
`release_import`,
`release_artifact`,
`upgrade_start`,
`upgrade_step`,
`upgrade_pause`,
`upgrade_resume`,
`upgrade_abort`,
`deploy`,
`undeploy`,
`rollback` (a deploy of an earlier deployment's stored bundle, ADR-046),
`create`,
`mint`,
`remint`,
`ready`,
`kill`,
`device_delete`,
`purge`,
`volume_key`,
`secret_put` (a workload secret set; name only),
`secret_delete`,
`secret_release` (a workload's secrets released to its lease holder; names only),
`store_create` (a platform store created, by its first write or an admin),
`store_delete` (a platform store and its data key deleted),
`queue_create` (a queue created, by its first use or an admin),
`queue_delete` (a queue, its messages and its data key deleted),
`queue_purge` (every message of a queue deleted),
`d1_write` (an operator's SQL changed a D1 database; statement count only),
`kv_put` (an admin wrote a Workers KV key; the namespace only, never the key),
`kv_delete` (an admin deleted a Workers KV key; the namespace only),
`telemetry_put` (the tenant's OTLP export set; endpoint and header names only),
`telemetry_delete`,
`telemetry_release` (the OTLP export config released to a workload's lease holder; once per host, workload and version),
`notification_put` (the tenant's notification webhook set; its host and header names only),
`notification_delete`,
`notification_failed` (a notification was not delivered; the webhook's host and the error),
`notification_test` (`tw notify test`; the origin and the outcome),
`reap`,
`preflight` (optional; dryRun deploys),
`host_add`,
`credential`,
`obo_root`,
`object_update`,
`container_reserve`,
`container_release`,
`image_push` (a manifest pushed to the registry),
`image_pin` (a deploy resolved its container images to digests, mirroring external ones),
`image_public` (a public base image added or removed),
`transfer` (a `_pool` node went to an object, ADR-031),
`revoke` (an unused pooled key revoked).
Public ingress (ADR-029), host chain:
`public_request` (one per public request),
`public_rejected` (per-minute summary of requests the limits refused, and of `not_public`/`bad_request` path refusals),
`public_object_created` (a name admitted for a public create).
Public ingress, mgmt chain:
`public_name_claim` (a tenant took a public name on its tailnet, red-team H10),
`public_name_release` (a tenant gave a public name up).
Workflow approvals (ADR-040), host chain:
`approval_requested` (a Workflow instance waits for an approval),
`approval_decision` (approved or rejected, with the approver's identity),
`approval_denied` (a decision refused, e.g. `cap_missing`),
`approval_timeout` (the wait timed out: rejected by default).
Both chains:
`expose_on` / `expose_off` (mgmt: the operator's change; host: the listener opened/closed).

Enum: `allow`, `deny`, `egress`, `spawn`, `kill`, `principal`, `tenant_note`, `container_start`, `container_stop`, `audit_dropped`, `tenant_create`, `tenant_update`, `tenant_delete`, `tenant_purge_host`, `host_register`, `host_delete`, `host_cordon`, `host_uncordon`, `host_drain`, `release_import`, `release_artifact`, `upgrade_start`, `upgrade_step`, `upgrade_pause`, `upgrade_resume`, `upgrade_abort`, `deploy`, `undeploy`, `rollback`, `create`, `mint`, `remint`, `ready`, `device_delete`, `purge`, `volume_key`, `secret_put`, `secret_delete`, `secret_release`, `store_create`, `store_delete`, `queue_create`, `queue_delete`, `queue_purge`, `d1_write`, `kv_put`, `kv_delete`, `telemetry_put`, `telemetry_delete`, `telemetry_release`, `notification_put`, `notification_delete`, `notification_failed`, `notification_test`, `reap`, `preflight`, `host_add`, `credential`, `obo_root`, `object_update`, `container_reserve`, `container_release`, `image_push`, `image_pin`, `image_public`, `transfer`, `revoke`, `public_request`, `public_rejected`, `public_object_created`, `public_name_claim`, `public_name_release`, `approval_requested`, `approval_decision`, `approval_denied`, `approval_timeout`, `expose_on`, `expose_off`

#### `AuditPage`

Extends `Page`.

| Field | Type | Required | Description |
|---|---|---|---|
| `chains` | array of `ChainStatus` | yes | Verification status of every chain that contributed to this page. |
| `items` | array of `AuditRecord` | no |  |
| `verified` | boolean | yes | AND over `chains[].ok`. |

#### `AuditRecord`

One hash-chained record. Hash input is the record without `hash` and
`hostSig`, keys sorted at every level, no whitespace, numbers as
written, no HTML escaping. `seq` and `chain` are assigned by mgmt.

| Field | Type | Required | Description |
|---|---|---|---|
| `actingFor` | string | no |  |
| `chain` | string | no | (read-only) `mgmt` or `host:<hostID>`. |
| `code` | string | no |  |
| `detail` | object \| string | no | Structured detail (mgmt records) or free text (host `tenant_note`s). |
| `dst` | string | no |  |
| `grant` | string | no |  |
| `hash` | string | yes |  |
| `hostSig` | string | no | base64 ed25519 over utf8(hash); host chains only. |
| `kind` | `AuditKind` | yes | Host chain (netd/hostd): `allow`, `deny`, `egress`, `spawn`, `kill`, `principal`, `tenant_note` (source `tenant`, unverified), `container_start` (an object's container, ADR-032), `container_stop` (an object's container, ADR-032), `audit_dropped` (records refused before they were chained, e.g. tenant notes over their rate during a flood; `detail.n` and `detail.byReason` count them, so the gap is visible and the chain still verifies). mgmt chain: `tenant_create`, `tenant_update`, `tenant_delete`, `tenant_purge_host` (a host removed a deleted tenant's files), `host_register`, `host_delete`, `host_cordon`, `host_uncordon`, `host_drain`, `release_import`, `release_artifact`, `upgrade_start`, `upgrade_step`, `upgrade_pause`, `upgrade_resume`, `upgrade_abort`, `deploy`, `undeploy`, `rollback` (a deploy of an earlier deployment's stored bundle, ADR-046), `create`, `mint`, `remint`, `ready`, `kill`, `device_delete`, `purge`, `volume_key`, `secret_put` (a workload secret set; name only), `secret_delete`, `secret_release` (a workload's secrets released to its lease holder; names only), `store_create` (a platform store created, by its first write or an admin), `store_delete` (a platform store and its data key deleted), `queue_create` (a queue created, by its first use or an admin), `queue_delete` (a queue, its messages and its data key deleted), `queue_purge` (every message of a queue deleted), `d1_write` (an operator's SQL changed a D1 database; statement count only), `kv_put` (an admin wrote a Workers KV key; the namespace only, never the key), `kv_delete` (an admin deleted a Workers KV key; the namespace only), `telemetry_put` (the tenant's OTLP export set; endpoint and header names only), `telemetry_delete`, `telemetry_release` (the OTLP export config released to a workload's lease holder; once per host, workload and version), `notification_put` (the tenant's notification webhook set; its host and header names only), `notification_delete`, `notification_failed` (a notification was not delivered; the webhook's host and the error), `notification_test` (`tw notify test`; the origin and the outcome), `reap`, `preflight` (optional; dryRun deploys), `host_add`, `credential`, `obo_root`, `object_update`, `container_reserve`, `container_release`, `image_push` (a manifest pushed to the registry), `image_pin` (a deploy resolved its container images to digests, mirroring external ones), `image_public` (a public base image added or removed), `transfer` (a `_pool` node went to an object, ADR-031), `revoke` (an unused pooled key revoked). Public ingress (ADR-029), host chain: `public_request` (one per public request), `public_rejected` (per-minute summary of requests the limits refused, and of `not_public`/`bad_request` path refusals), `public_object_created` (a name admitted for a public create). Public ingress, mgmt chain: `public_name_claim` (a tenant took a public name on its tailnet, red-team H10), `public_name_release` (a tenant gave a public name up). Workflow approvals (ADR-040), host chain: `approval_requested` (a Workflow instance waits for an approval), `approval_decision` (approved or rejected, with the approver's identity), `approval_denied` (a decision refused, e.g. `cap_missing`), `approval_timeout` (the wait timed out: rejected by default). Both chains: `expose_on` / `expose_off` (mgmt: the operator's change; host: the listener opened/closed). |
| `objectID` | string | no |  |
| `prevHash` | string | yes | `""` for the first record of a chain. |
| `seq` | integer (int64) | no | (read-only) |
| `source` | `AuditSource` | no | `mgmt` for the mgmt chain; `host` for netd/hostd security records; `tenant` for a `tenant_note` written by tenant code (unverified content). |
| `tenantID` | string | no |  |
| `traceID` | string | no |  |
| `ts` | string (date-time) | yes |  |

#### `AuditSource`

`mgmt` for the mgmt chain; `host` for netd/hostd security records; `tenant` for a `tenant_note` written by tenant code (unverified content).

Enum: `mgmt`, `host`, `tenant`

#### `AuditVerification`

| Field | Type | Required | Description |
|---|---|---|---|
| `chains` | array of `ChainStatus` | yes |  |
| `ok` | boolean | yes |  |
| `verifiedAt` | integer (int64) | yes |  |

#### `Caps`

`{capName: value | [values]}`; see `caps ⊆ parent` semantics in `CreateObject`.

#### `ChainStatus`

| Field | Type | Required | Description |
|---|---|---|---|
| `brokenAt` | integer (int64) | no | seq of the first bad record |
| `chain` | string | yes |  |
| `error` | string | no |  |
| `ok` | boolean | yes |  |
| `records` | integer | yes |  |

#### `ClassConfig`

One DO namespace from `tailworks.jsonc` `tailnet.objects`.

| Field | Type | Required | Description |
|---|---|---|---|
| `agent` | boolean | no |  |
| `class` | string | yes |  |
| `previewOf` | string | no | `_workload` only, previews: the workload this deployment is a preview of (ADR-048), deployed as the workload `<workload>--preview-<alias>`. |
| `replicaOf` | string | no | `_workload` only, replicas 2..N: the workload this deployment is a replica of (`tailworks.jsonc` `replicas`). Replica i is deployed as the workload `<workload>--r<i>`. |
| `replicas` | integer | no | `_workload` only, the workload itself: its replica count when > 1. |
| `service` | string | no | `_workload` only: the Tailscale Service the workload entry node hosts (`tailworks.jsonc` `tailnet.service`). |
| `tags` | array of string | yes |  |
| `ttl` | string | no | `10m`-style duration or seconds. |

#### `ContainerClass`

A container class of a deployment (a wrangler `containers` entry plus tailworks.jsonc overrides) and its pinned images.

| Field | Type | Required | Description |
|---|---|---|---|
| `arch` | string: `amd64` \| `arm64` | no |  |
| `class` | string | yes |  |
| `custom` | `ContainerResources` | no | What one container reserves: vCPUs (fractional; Cloudflare's `lite` is 0.0625), guest memory and scratch disk (from its class's `instance_type`). |
| `image` | string | no | The default image reference `start()` runs. |
| `imageSources` | object | no | Named images tw deploy builds: name → the wrangler Dockerfile, as written. |
| `images` | object | no | Named images (`ctx.container.images`): name → reference. |
| `instanceType` | string | no |  |
| `maxInstances` | integer | no |  |
| `pins` | array of `ContainerImagePin` | no |  |
| `source` | string | no | The wrangler `image` as written (a ref, or a Dockerfile path). |

#### `ContainerImagePin`

One image reference of a container class, resolved once at deploy for one arch (ADR-032 D3):
hosts pull exactly `repository@digest` from tailworks' registry and nothing else. Retagging the
upstream ref later changes nothing until the next deploy.

| Field | Type | Required | Description |
|---|---|---|---|
| `arch` | string: `amd64` \| `arm64` | yes |  |
| `digest` | string | yes | The linux/`arch` image manifest (never an index). |
| `from` | string | no | What was resolved: `<upstream ref>@<digest>` (mirrored), `alias <ref>`, `public <ref>`, or the tw.local ref. |
| `name` | string | no | The class's image name (wrangler `images` key); absent for the default image. |
| `ref` | string | yes | The reference as `ctx.container` sees it (the wrangler image, or the `tw.local/<tenant>/…@sha256:…` ref `tw deploy` built). |
| `repository` | string | yes | `<tenantID>/…` (the tenant's builds and mirrors) or `public/…` (public bases, alias targets). |

#### `ContainerImages`

Images `tw deploy` built and pushed, by class: `{"<Class>": {"image": ref, "images": {name: ref}}}`
with `tw.local/<tenantID>/<repo>@sha256:…` refs (an index when built for several arches). They
replace the class's Dockerfile `image` / `images` entries; every ref is then pinned like any other.

#### `ContainerReservation`

| Field | Type | Required | Description |
|---|---|---|---|
| `class` | string | yes |  |
| `createdAt` | integer (int64) | yes | unix ms (store clock) |
| `host` | string | yes |  |
| `object` | string | yes |  |
| `renewedAt` | integer (int64) | yes | unix ms of the last reserve or heartbeat that reported it (store clock) |
| `reservationID` | string | yes |  |
| `resources` | `ContainerResources` | yes | What one container reserves: vCPUs (fractional; Cloudflare's `lite` is 0.0625), guest memory and scratch disk (from its class's `instance_type`). |
| `tenantID` | string | yes |  |
| `workload` | string | yes |  |

#### `ContainerReservationCreate`

| Field | Type | Required | Description |
|---|---|---|---|
| `class` | string | yes | The DO class (a wrangler `containers[].class_name`). |
| `epoch` | integer (int64) | yes | The workload's lease epoch the host holds. |
| `object` | string | yes | The DO instance (its name or hex ID): one container per instance. |
| `resources` | `ContainerResources` | yes | What one container reserves: vCPUs (fractional; Cloudflare's `lite` is 0.0625), guest memory and scratch disk (from its class's `instance_type`). |
| `tenantID` | string | yes |  |
| `workload` | string | yes |  |

#### `ContainerReservationPage`

Extends `Page`.

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | array of `ContainerReservation` | no |  |

#### `ContainerResources`

What one container reserves: vCPUs (fractional; Cloudflare's `lite` is 0.0625), guest memory and scratch disk (from its class's `instance_type`).

| Field | Type | Required | Description |
|---|---|---|---|
| `diskMiB` | integer | yes |  |
| `memMiB` | integer | yes |  |
| `vcpu` | number | yes |  |

#### `Credential`

A single-use auth key. Never logged; `String()` redacts it in Go.

| Field | Type | Required | Description |
|---|---|---|---|
| `authKey` | string (password) | yes | `tskey-auth-…`, single use. |
| `controlURL` | string (uri) | no |  |
| `ephemeral` | boolean | yes |  |
| `expiresAt` | integer (int64) | yes | unix ms |
| `hostname` | string | yes |  |
| `reminted` | boolean | no | True when `reason=rejoin_rejected` deleted the old device and re-minted. |
| `tags` | array of string | yes |  |

#### `D1Query`

| Field | Type | Required | Description |
|---|---|---|---|
| `queries` | array of object | yes |  |

#### `D1Result`

One statement's result, as D1's wire protocol has it.

| Field | Type | Required | Description |
|---|---|---|---|
| `error` | string | no |  |
| `meta` | object | no | `changes`, `last_row_id`, `changed_db`, `rows_read`, `rows_written`, `size_after`, `duration` (ms), `served_by`. |
| `results` | object | no |  |
| `success` | boolean | yes |  |

#### `Deployment`

| Field | Type | Required | Description |
|---|---|---|---|
| `containers` | array of `ContainerClass` | no | The bundle's container classes with the images pinned at deploy (ADR-032 D3): what hosts pull, by digest. |
| `createdAt` | integer (int64) | yes |  |
| `deployedBy` | string | no | The actor who deployed (see Conventions). |
| `deploymentID` | string | yes |  |
| `endpoint` | `Endpoint` | yes | The workload entry node (`_workload` object). |
| `epoch` | integer (int64) | yes |  |
| `error` | string | no |  |
| `host` | string | yes |  |
| `maxDepth` | integer | no |  |
| `maxLive` | integer | no |  |
| `namespaces` | array of `ClassConfig` | yes |  |
| `placement` | `PlacementDecision` | no | Why a deployment is on its host (smart placement, ADR-033). |
| `public` | `DeploymentPublic` | no | The deployment's public routes (ADR-029); absent when tailnet-only. |
| `replicas` | array of `ReplicaRef` | no | A workload with `tailworks.jsonc` `replicas` > 1 (stateless only): every copy, this deployment first, each on a distinct host. Undeploying the workload undeploys its replicas. |
| `status` | `DeploymentStatus` | yes | - `assigned`: mgmt placed it; the host has not reported yet. - `starting`: the host is starting it. - `active`: serving. - `crashloop`: it served, then its workerd kept exiting; the host keeps restarting it with exponential backoff (200 ms doubling to 5 min) and reports `active` again once it answers. `error` holds the attempt count, the next delay and the last log lines. - `draining`: being replaced or undeployed. - `stopped`: stopped (undeployed, fenced or unassigned). - `failed`: the deploy failed (bad bundle or config, or it never became healthy); the host does not retry it. `error` says why. - `superseded`: a newer deployment of the workload replaced it. |
| `tenantID` | string | yes |  |
| `tier` | `Tier` | no |  |
| `updatedAt` | integer (int64) | no |  |
| `workload` | string | yes |  |
| `workloadTags` | array of string | no |  |

#### `DeploymentCreate`

| Field | Type | Required | Description |
|---|---|---|---|
| `bundle` | string | yes | tar (optionally gzipped) of the built worker + `tailworks.jsonc` + `wrangler.jsonc`. |
| `containerImages` | `ContainerImages` | no | Images `tw deploy` built and pushed, by class: `{"<Class>": {"image": ref, "images": {name: ref}}}` with `tw.local/<tenantID>/<repo>@sha256:…` refs (an index when built for several arches). They replace the class's Dockerfile `image` / `images` entries; every ref is then pinned like any other. |
| `forceTakeover` | boolean | no | Confirm that the workload's current host, which has stopped heartbeating, is gone for good, so a stateful workload may move off it (ADR-028 §3). Without it such a move is refused with `takeover_unconfirmed`. Audited (`takeoverFrom`, `forced`). With `replace`, it also confirms moving a stateful workload off a live host (its DO storage stays there). |
| `host` | string | no | Pin a host. |
| `namespaces` | array of `ClassConfig` | no | Override the classes read from `tailworks.jsonc` (tests). |
| `preview` | boolean | no | Deploy as a preview (`tw deploy --preview`, ADR-048): a workload of its own, `<workload>--preview-<alias>`, with the same tags, its own lease, Durable Object storage and stores, the workload's secrets, and no Tailscale Service, public routes or replicas. The workload itself is not touched. A bundle with queue bindings is `400 preview_unsupported`. |
| `previewAlias` | string | no | The preview's alias (implies `preview`). Default: the first 8 hex digits of the bundle's SHA-256, so the same bundle is the same preview. |
| `replace` | boolean | no | Re-place a running workload (smart placement, ADR-033): move it to a clearly better eligible host, or to `host` when pinned. Audited (`movedFrom`, `replace`). |
| `tenantID` | string | yes |  |
| `workload` | string | yes |  |

#### `DeploymentHistoryPage`

Extends `Page`.

| Field | Type | Required | Description |
|---|---|---|---|
| `bundleRetention` | integer | yes | How many of each workload's newest bundles mgmt keeps (0 = every bundle). |
| `items` | array of `DeploymentRecord` | no |  |

#### `DeploymentPage`

Extends `Page`.

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | array of `Deployment` | no |  |

#### `DeploymentPublic`

The deployment's public routes (ADR-029); absent when tailnet-only.

| Field | Type | Required | Description |
|---|---|---|---|
| `allowed` | boolean | yes | The tenant's `publicIngress` is `allow`; false: declared but not served. |
| `exposed` | boolean | yes | `tw expose` setting. |
| `routes` | array of string | yes |  |
| `url` | string (uri) | yes |  |

#### `DeploymentRecord`

One entry of a workload's deployment history (ADR-046).

| Field | Type | Required | Description |
|---|---|---|---|
| `activeAt` | integer (int64) | no | When the host first reported it `active` (unix ms). Absent if it never was, or the record predates history. |
| `bundleBytes` | integer (int64) | no | Size of the retained bundle (0 once pruned). |
| `bundleRetained` | boolean | yes | mgmt still holds the bundle, so it can be rolled back to. |
| `bundleSHA256` | string | yes | Hex SHA-256 of the bundle as uploaded (empty: deployed without a bundle). |
| `createdAt` | integer (int64) | yes |  |
| `deployedBy` | string | no | The actor who deployed (see Conventions). |
| `deploymentID` | string | yes |  |
| `epoch` | integer (int64) | yes |  |
| `error` | string | no | The host's last error for this deployment. |
| `host` | string | yes |  |
| `hostStatus` | string | no | The last status the host reported (or mgmt set) for this deployment, kept after it is superseded: `active` means it was serving when it was replaced. |
| `rollbackOf` | string | no | Set on a rollback: the deployment whose bundle it redeployed. |
| `status` | `DeploymentStatus` | yes | - `assigned`: mgmt placed it; the host has not reported yet. - `starting`: the host is starting it. - `active`: serving. - `crashloop`: it served, then its workerd kept exiting; the host keeps restarting it with exponential backoff (200 ms doubling to 5 min) and reports `active` again once it answers. `error` holds the attempt count, the next delay and the last log lines. - `draining`: being replaced or undeployed. - `stopped`: stopped (undeployed, fenced or unassigned). - `failed`: the deploy failed (bad bundle or config, or it never became healthy); the host does not retry it. `error` says why. - `superseded`: a newer deployment of the workload replaced it. |
| `workload` | string | yes |  |

#### `DeploymentReport`

| Field | Type | Required | Description |
|---|---|---|---|
| `deploymentID` | string | yes |  |
| `error` | string | no |  |
| `status` | `DeploymentStatus` | yes | - `assigned`: mgmt placed it; the host has not reported yet. - `starting`: the host is starting it. - `active`: serving. - `crashloop`: it served, then its workerd kept exiting; the host keeps restarting it with exponential backoff (200 ms doubling to 5 min) and reports `active` again once it answers. `error` holds the attempt count, the next delay and the last log lines. - `draining`: being replaced or undeployed. - `stopped`: stopped (undeployed, fenced or unassigned). - `failed`: the deploy failed (bad bundle or config, or it never became healthy); the host does not retry it. `error` says why. - `superseded`: a newer deployment of the workload replaced it. |

#### `DeploymentStatus`

- `assigned`: mgmt placed it; the host has not reported yet.
- `starting`: the host is starting it.
- `active`: serving.
- `crashloop`: it served, then its workerd kept exiting; the host
  keeps restarting it with exponential backoff (200 ms doubling to
  5 min) and reports `active` again once it answers. `error` holds
  the attempt count, the next delay and the last log lines.
- `draining`: being replaced or undeployed.
- `stopped`: stopped (undeployed, fenced or unassigned).
- `failed`: the deploy failed (bad bundle or config, or it never
  became healthy); the host does not retry it. `error` says why.
- `superseded`: a newer deployment of the workload replaced it.

Enum: `assigned`, `starting`, `active`, `crashloop`, `draining`, `stopped`, `failed`, `superseded`

#### `DeviceDeleteRun`

| Field | Type | Required | Description |
|---|---|---|---|
| `deleted` | array of string | yes | Object IDs whose devices were deleted. |
| `errors` | object | no |  |

#### `Endpoint`

The workload entry node (`_workload` object).

| Field | Type | Required | Description |
|---|---|---|---|
| `magicDNS` | string | yes |  |
| `node` | string | yes | hostname |
| `objectID` | string | no |  |
| `service` | `ServiceEndpoint` | no | A workload's Tailscale Service: a stable name and VIPs that survive the workload node being re-minted or moving host. Callers need a grant to the `svc:` name; control routes it to the workload node only when policy `autoApprovers.services` lets the workload tags host it. |
| `status` | `ObjectStatus` | yes | `pending` → `minted` → `ready`; or, for an object given a pre-joined pool node (`ObjectCreate.poolObjectID`, ADR-031), `assigned` → `ready` (never minted). Terminal: `killed` (tombstone, `410`), `failed`, and `transferred` (a `_pool` node whose node went to an object). |

#### `Error`

The typed error catalog (docs/errors.md) shape. `code` is stable and machine-readable;
`missing` names what is absent (grant, cap, field, quota); `snippet`
is policy HuJSON to paste when a grant is missing; `doc_url` is
`https://github.com/rosskukulinski/tailworks/blob/master/docs/errors.md#<code>` (the
code's heading in the typed error catalog); `trace_id` correlates with audit.

| Field | Type | Required | Description |
|---|---|---|---|
| `code` | `ErrorCode` | yes | The catalog. Status in parentheses. \| code \| status \| when \| \|---\|---\|---\| \| `bad_request` \| 400 \| malformed body, parameter or filter \| \| `bad_cursor` \| 400 \| `cursor` does not decode \| \| `bad_bundle` \| 400 \| bundle has no `tailworks.jsonc`, neither `tailnet.tags` nor `tailnet.objects`, or invalid `triggers.crons` \| \| `reserved_tag` \| 400 \| a bundle or namespace asks for one of tailworks' own tags (`tag:tw`, `tag:tw-mgmt`, `tag:tw-host`) \| \| `immutable_field` \| 400 \| PATCH touches a field the caller may not change \| \| `mint_mode_removed` \| 400 \| a create or PATCH names the removed `apikey` or `wif` mint mode, or a tenant stored in one tries to mint (switch it to `oauth`) \| \| `too_many_secrets` \| 400 \| a workload already has 128 secrets \| \| `d1_error` \| 400 \| a D1 statement failed (SQLite's message, or a D1 limit); nothing of its batch was applied \| \| `no_identity` \| 401 \| WhoIs failed for the connection \| \| `host_token_required` \| 401 \| local-dev socket without `Tw-Host-Token` \| \| `host_token_invalid` \| 403 \| local-dev socket, wrong token \| \| `not_admin` \| 403 \| tailnet caller without `github.com/rosskukulinski/tailworks/cap/admin` (`snippet` given) \| \| `tag_not_owned` \| 400 \| a tenant with its own owner tag asks for a tag its owner tag doesn't own, or that another tenant's on the tailnet owns (`snippet` given) \| \| `shared_owner_tag` \| 409 \| a second tenant on a tailnet where either tenant uses `tag:tw`, or both the same owner tag \| \| `name_prefix_required` \| 409 \| a second tenant on a tailnet without a distinct `namePrefix` \| \| `tenant_forbidden` \| 403 \| a tenant admin's `github.com/rosskukulinski/tailworks/cap/tenant-admin` grants don't name the tenant with the action, or the route is admin-only (`snippet` given) \| \| `cap_missing` \| 403 \| an approval the caller's `github.com/rosskukulinski/tailworks/cap/approve` grants do not cover (`snippet` given) \| \| `forbidden` \| 403 \| role ok, but outside the caller's records / namespaces \| \| `host_not_registered` \| 403 \| `tag:tw-host` node that never did `PUT /v1/hosts/{id}`, or a `PUT` for a host ID the admin never created \| \| `host_identity_mismatch` \| 403 \| host ID bound to another node; `registrationSecret` missing or wrong; `auditPubKey` differs from the pinned one \| \| `delegation_invalid` \| 403 \| child tag not owned by the parent's, parent unknown (404) or dead (410) \| \| `delegation_exceeds_parent` \| 403 \| spawn caps ⊄ parent (`missing: {src, dst, cap, field}`) \| \| `policy_missing_tag_owners` \| 403 \| the control plane refused to mint a key for tailworks' own tags (`snippet` gives the tagOwners to add) \| \| `not_found` \| 404 \| — \| \| `unknown_tenant` \| 404 \| — \| \| `unknown_namespace` \| 404 \| `(workload, class)` not declared \| \| `no_logs` \| 404 \| the workload's host has no log file for it yet \| \| `unknown_secret` \| 404 \| the workload has no secret of that name \| \| `unknown_store` \| 404 \| the workload has no store of that kind and name \| \| `unknown_queue` \| 404 \| the tenant has no queue of that name \| \| `exists` \| 409 \| create of an existing ID \| \| `lease_lost` \| 409 \| epoch/holder fence failed \| \| `lease_held` \| 409 \| another host holds the lease \| \| `store_conflict` \| 409 \| a store journal append's `after` (or `incarnation`) is not the journal's head: catch up and retry \| \| `store_degraded` \| 409 \| the store object on the workload's host could not apply a journal entry: it stopped its replay there, serves reads from what it applied and refuses writes (`tw d1 export`, then recreate the database) \| \| `store_limit` \| 400 \| a store append exceeds an entry, append or store size limit \| \| `queue_limit` \| 400 \| a send exceeds a message, batch, delay or queue size limit \| \| `store_unavailable` \| 503 \| the store is unreachable, restarting or contended (a row-lock or idle-transaction timeout); retry after `Retry-After` \| \| `tier_unavailable` \| 409 \| the chosen host (`--host`, or the workload's current host) does not offer the deployment's tier, or `containers` (for the image's `arch`) when the bundle declares containers \| \| `replica_conflict` \| 409 \| a deploy names a replica (`<workload>--r<n>`) instead of its workload, or a replica name is taken by a separate workload \| \| `preview_conflict` \| 409 \| a preview's name (`<workload>--preview-<alias>`) is taken by a workload that is not a preview of this one \| \| `preview_failed` \| 409 \| promote of a preview its host reported `failed` \| \| `preview_unsupported` \| 400 \| `preview` for a bundle with queue bindings (queues are tenant-wide), or of a workload that is itself a preview or replica \| \| `unknown_preview` \| 404 \| promote of a preview that doesn't exist or is stopped \| \| `service_conflict` \| 409 \| `tailnet.service` names a Service that exists and isn't managed by tailworks for this tenant and workload \| \| `service_api_failed` \| 502 \| mgmt can't read or create the Service definition (an OAuth client needs Services › Write) \| \| `profile_unsupported` \| 400 \| the tenant's control-plane profile can't serve the request: on the Headscale profile (ADR-015), `tailnet.service` (no Tailscale Services), public routes or `publicIngress: allow` (no Funnel) \| \| `public_name_owned` \| 409 \| a deploy declares public routes under a workload name another tenant on the same tailnet owns as its public name (red-team H10) \| \| `public_name_in_use` \| 409 \| release of a public name the workload still serves: its latest deployment declares public routes, or its public node is live \| \| `move_unconfirmed` \| 409 \| `replace` would move a stateful workload off its live host, leaving its DO storage behind; confirm with `forceTakeover` \| \| `lease_changed` \| 409 \| the workload's lease changed while `replace` was moving it (a concurrent deploy); retry \| \| `takeover_unconfirmed` \| 409 \| the workload has Durable Objects and its host stopped heartbeating but is still registered: it may still be serving them. `tw host rm <host> --drain`, or redeploy with `--force-takeover` \| \| `already_joined` \| 409 \| credential for an object that has a node \| \| `identity_conflict` \| 409 \| object ID collision across identities \| \| `namespace_ambiguous` \| 409 \| class declared by several workloads, `workload` missing \| \| `not_ready` \| 409 \| certificate requested before `ready` \| \| `superseded` \| 409 \| undeploy of a non-latest deployment \| \| `tenant_has_live_objects` \| 409 \| tenant delete without `force` \| \| `host_holds_leases` \| 409 \| host delete without `drain` \| \| `version_skew` \| 409 \| a host (or CLI, or mgmt replica) outside the version window: a host newer than mgmt, or more than one release line behind it; or a store a newer tw-mgmt contracted (ADR-041 §7) \| \| `upgrade_blocked` \| 409 \| an upgrade run can't start: its plan has blockers (`message` lists them; `POST /v1/upgrades` with `dryRun` shows the plan) \| \| `upgrade_state` \| 409 \| pause, resume or abort of a run that isn't running (pause), paused (resume), or active (abort) \| \| `feature_unavailable` \| 409 \| the deployment needs a host feature (`kv`, `r2`, `d1`, …) that no eligible host, or the workload's current host, advertises: upgrade the hosts \| \| `killed` \| 410 \| tombstone (`TombstoneError`) \| \| `precondition_failed` \| 412 \| `If-Match` mismatch \| \| `misdirected_request` \| 421 \| a tailnet request whose `Host` is not one of mgmt's own names or an IP literal (DNS-rebinding guard) \| \| `object_quota_exceeded` \| 429 \| tenant `maxObjects` \| \| `spawn_budget_exceeded` \| 429 \| `maxLive` / `maxDepth` \| \| `container_quota_exceeded` \| 429 \| tenant `maxContainers` / `maxContainerMemMiB` / `maxContainerVCPU`, or the class's `max_instances` (`missing: {quota, limit, used, requested}`) \| \| `image_unavailable` \| 502 \| a container image could not be pinned at deploy (not in tailworks' registry, no image for any of the pool's arches, or the upstream mirror failed) \| \| `registry_unavailable` \| 409 \| this tw-mgmt serves no OCI registry (`-registry-dir off`) \| \| `mirror_disabled` \| 409 \| this tw-mgmt does not mirror external images (`-no-mirror`) \| \| `bootstrap_failed` \| 502 \| control plane refused the mint \| \| `host_unavailable` \| 502 \| the host holding the workload's lease did not answer mgmt (logs, approvals) \| \| `host_token_unconfigured` \| 503 \| local-dev mgmt without a token (fail closed) \| \| `identity_pending` \| 503 \| mint still queued (`Retry-After: 1`) \| \| `no_host` \| 503 \| no live host for the deployment (in the tenant's pool, offering its tier, and `containers` for the image's `arch` when it declares containers, with a free node and, for the microVM tier or containers, a free VM in its capacity vector) \| \| `policy_unavailable` \| 503 \| mgmt cannot read the tenant policy \| \| `approval_not_pending` \| 409 \| no approval of that Workflow instance is waiting (decided, timed out, or never asked) \| \| `internal` \| 500 \| — \| |
| `doc_url` | string (uri) | yes |  |
| `message` | string | yes |  |
| `missing` | object | no | What is missing. Shape depends on `code` (e.g. `{src, dst, cap, field}` for `delegation_invalid`). |
| `snippet` | string | no | Policy HuJSON to add (a grant or tagOwners entry). |
| `trace_id` | string | yes |  |

#### `ErrorCode`

The catalog. Status in parentheses.

| code | status | when |
|---|---|---|
| `bad_request` | 400 | malformed body, parameter or filter |
| `bad_cursor` | 400 | `cursor` does not decode |
| `bad_bundle` | 400 | bundle has no `tailworks.jsonc`, neither `tailnet.tags` nor `tailnet.objects`, or invalid `triggers.crons` |
| `reserved_tag` | 400 | a bundle or namespace asks for one of tailworks' own tags (`tag:tw`, `tag:tw-mgmt`, `tag:tw-host`) |
| `immutable_field` | 400 | PATCH touches a field the caller may not change |
| `mint_mode_removed` | 400 | a create or PATCH names the removed `apikey` or `wif` mint mode, or a tenant stored in one tries to mint (switch it to `oauth`) |
| `too_many_secrets` | 400 | a workload already has 128 secrets |
| `d1_error` | 400 | a D1 statement failed (SQLite's message, or a D1 limit); nothing of its batch was applied |
| `no_identity` | 401 | WhoIs failed for the connection |
| `host_token_required` | 401 | local-dev socket without `Tw-Host-Token` |
| `host_token_invalid` | 403 | local-dev socket, wrong token |
| `not_admin` | 403 | tailnet caller without `github.com/rosskukulinski/tailworks/cap/admin` (`snippet` given) |
| `tag_not_owned` | 400 | a tenant with its own owner tag asks for a tag its owner tag doesn't own, or that another tenant's on the tailnet owns (`snippet` given) |
| `shared_owner_tag` | 409 | a second tenant on a tailnet where either tenant uses `tag:tw`, or both the same owner tag |
| `name_prefix_required` | 409 | a second tenant on a tailnet without a distinct `namePrefix` |
| `tenant_forbidden` | 403 | a tenant admin's `github.com/rosskukulinski/tailworks/cap/tenant-admin` grants don't name the tenant with the action, or the route is admin-only (`snippet` given) |
| `cap_missing` | 403 | an approval the caller's `github.com/rosskukulinski/tailworks/cap/approve` grants do not cover (`snippet` given) |
| `forbidden` | 403 | role ok, but outside the caller's records / namespaces |
| `host_not_registered` | 403 | `tag:tw-host` node that never did `PUT /v1/hosts/{id}`, or a `PUT` for a host ID the admin never created |
| `host_identity_mismatch` | 403 | host ID bound to another node; `registrationSecret` missing or wrong; `auditPubKey` differs from the pinned one |
| `delegation_invalid` | 403 | child tag not owned by the parent's, parent unknown (404) or dead (410) |
| `delegation_exceeds_parent` | 403 | spawn caps ⊄ parent (`missing: {src, dst, cap, field}`) |
| `policy_missing_tag_owners` | 403 | the control plane refused to mint a key for tailworks' own tags (`snippet` gives the tagOwners to add) |
| `not_found` | 404 | — |
| `unknown_tenant` | 404 | — |
| `unknown_namespace` | 404 | `(workload, class)` not declared |
| `no_logs` | 404 | the workload's host has no log file for it yet |
| `unknown_secret` | 404 | the workload has no secret of that name |
| `unknown_store` | 404 | the workload has no store of that kind and name |
| `unknown_queue` | 404 | the tenant has no queue of that name |
| `exists` | 409 | create of an existing ID |
| `lease_lost` | 409 | epoch/holder fence failed |
| `lease_held` | 409 | another host holds the lease |
| `store_conflict` | 409 | a store journal append's `after` (or `incarnation`) is not the journal's head: catch up and retry |
| `store_degraded` | 409 | the store object on the workload's host could not apply a journal entry: it stopped its replay there, serves reads from what it applied and refuses writes (`tw d1 export`, then recreate the database) |
| `store_limit` | 400 | a store append exceeds an entry, append or store size limit |
| `queue_limit` | 400 | a send exceeds a message, batch, delay or queue size limit |
| `store_unavailable` | 503 | the store is unreachable, restarting or contended (a row-lock or idle-transaction timeout); retry after `Retry-After` |
| `tier_unavailable` | 409 | the chosen host (`--host`, or the workload's current host) does not offer the deployment's tier, or `containers` (for the image's `arch`) when the bundle declares containers |
| `replica_conflict` | 409 | a deploy names a replica (`<workload>--r<n>`) instead of its workload, or a replica name is taken by a separate workload |
| `preview_conflict` | 409 | a preview's name (`<workload>--preview-<alias>`) is taken by a workload that is not a preview of this one |
| `preview_failed` | 409 | promote of a preview its host reported `failed` |
| `preview_unsupported` | 400 | `preview` for a bundle with queue bindings (queues are tenant-wide), or of a workload that is itself a preview or replica |
| `unknown_preview` | 404 | promote of a preview that doesn't exist or is stopped |
| `service_conflict` | 409 | `tailnet.service` names a Service that exists and isn't managed by tailworks for this tenant and workload |
| `service_api_failed` | 502 | mgmt can't read or create the Service definition (an OAuth client needs Services › Write) |
| `profile_unsupported` | 400 | the tenant's control-plane profile can't serve the request: on the Headscale profile (ADR-015), `tailnet.service` (no Tailscale Services), public routes or `publicIngress: allow` (no Funnel) |
| `public_name_owned` | 409 | a deploy declares public routes under a workload name another tenant on the same tailnet owns as its public name (red-team H10) |
| `public_name_in_use` | 409 | release of a public name the workload still serves: its latest deployment declares public routes, or its public node is live |
| `move_unconfirmed` | 409 | `replace` would move a stateful workload off its live host, leaving its DO storage behind; confirm with `forceTakeover` |
| `lease_changed` | 409 | the workload's lease changed while `replace` was moving it (a concurrent deploy); retry |
| `takeover_unconfirmed` | 409 | the workload has Durable Objects and its host stopped heartbeating but is still registered: it may still be serving them. `tw host rm <host> --drain`, or redeploy with `--force-takeover` |
| `already_joined` | 409 | credential for an object that has a node |
| `identity_conflict` | 409 | object ID collision across identities |
| `namespace_ambiguous` | 409 | class declared by several workloads, `workload` missing |
| `not_ready` | 409 | certificate requested before `ready` |
| `superseded` | 409 | undeploy of a non-latest deployment |
| `tenant_has_live_objects` | 409 | tenant delete without `force` |
| `host_holds_leases` | 409 | host delete without `drain` |
| `version_skew` | 409 | a host (or CLI, or mgmt replica) outside the version window: a host newer than mgmt, or more than one release line behind it; or a store a newer tw-mgmt contracted (ADR-041 §7) |
| `upgrade_blocked` | 409 | an upgrade run can't start: its plan has blockers (`message` lists them; `POST /v1/upgrades` with `dryRun` shows the plan) |
| `upgrade_state` | 409 | pause, resume or abort of a run that isn't running (pause), paused (resume), or active (abort) |
| `feature_unavailable` | 409 | the deployment needs a host feature (`kv`, `r2`, `d1`, …) that no eligible host, or the workload's current host, advertises: upgrade the hosts |
| `killed` | 410 | tombstone (`TombstoneError`) |
| `precondition_failed` | 412 | `If-Match` mismatch |
| `misdirected_request` | 421 | a tailnet request whose `Host` is not one of mgmt's own names or an IP literal (DNS-rebinding guard) |
| `object_quota_exceeded` | 429 | tenant `maxObjects` |
| `spawn_budget_exceeded` | 429 | `maxLive` / `maxDepth` |
| `container_quota_exceeded` | 429 | tenant `maxContainers` / `maxContainerMemMiB` / `maxContainerVCPU`, or the class's `max_instances` (`missing: {quota, limit, used, requested}`) |
| `image_unavailable` | 502 | a container image could not be pinned at deploy (not in tailworks' registry, no image for any of the pool's arches, or the upstream mirror failed) |
| `registry_unavailable` | 409 | this tw-mgmt serves no OCI registry (`-registry-dir off`) |
| `mirror_disabled` | 409 | this tw-mgmt does not mirror external images (`-no-mirror`) |
| `bootstrap_failed` | 502 | control plane refused the mint |
| `host_unavailable` | 502 | the host holding the workload's lease did not answer mgmt (logs, approvals) |
| `host_token_unconfigured` | 503 | local-dev mgmt without a token (fail closed) |
| `identity_pending` | 503 | mint still queued (`Retry-After: 1`) |
| `no_host` | 503 | no live host for the deployment (in the tenant's pool, offering its tier, and `containers` for the image's `arch` when it declares containers, with a free node and, for the microVM tier or containers, a free VM in its capacity vector) |
| `policy_unavailable` | 503 | mgmt cannot read the tenant policy |
| `approval_not_pending` | 409 | no approval of that Workflow instance is waiting (decided, timed out, or never asked) |
| `internal` | 500 | — |

Enum: `bad_request`, `bad_cursor`, `bad_bundle`, `reserved_tag`, `immutable_field`, `mint_mode_removed`, `too_many_secrets`, `d1_error`, `no_identity`, `host_token_required`, `host_token_invalid`, `not_admin`, `tenant_forbidden`, `tag_not_owned`, `shared_owner_tag`, `name_prefix_required`, `cap_missing`, `forbidden`, `host_not_registered`, `host_identity_mismatch`, `delegation_invalid`, `delegation_exceeds_parent`, `policy_missing_tag_owners`, `not_found`, `unknown_tenant`, `unknown_namespace`, `no_logs`, `unknown_secret`, `unknown_store`, `unknown_queue`, `exists`, `lease_lost`, `lease_held`, `approval_not_pending`, `store_conflict`, `store_degraded`, `store_limit`, `queue_limit`, `takeover_unconfirmed`, `service_conflict`, `service_api_failed`, `profile_unsupported`, `replica_conflict`, `preview_conflict`, `preview_failed`, `preview_unsupported`, `unknown_preview`, `tier_unavailable`, `move_unconfirmed`, `lease_changed`, `store_unavailable`, `already_joined`, `identity_conflict`, `namespace_ambiguous`, `not_ready`, `superseded`, `tenant_has_live_objects`, `host_holds_leases`, `version_skew`, `feature_unavailable`, `upgrade_blocked`, `upgrade_state`, `killed`, `precondition_failed`, `misdirected_request`, `object_quota_exceeded`, `spawn_budget_exceeded`, `container_quota_exceeded`, `bootstrap_failed`, `host_unavailable`, `host_token_unconfigured`, `identity_pending`, `no_host`, `policy_unavailable`, `image_unavailable`, `registry_unavailable`, `mirror_disabled`, `public_ingress_not_allowed`, `public_name_owned`, `public_name_in_use`, `unknown_workload`, `audit_key_required`, `audit_object_foreign`, `bad_host_sig`, `internal`

#### `Health`

| Field | Type | Required | Description |
|---|---|---|---|
| `ok` | boolean | yes |  |

#### `Host`

| Field | Type | Required | Description |
|---|---|---|---|
| `arch` | `Arch` | no | A Go GOARCH. Container images must match their host's. |
| `auditPubKey` | string | no | base64 ed25519; verifies `hostSig`. |
| `capacity` | `HostCapacity` | no | The capacity vector a host registers (ADR-032 D9). `nodes` is tailnet nodes (= `capacityNodes`); the rest bound microVMs (DO instances and containers alike): their count, guest memory (all, and the part backed by 2 MiB huge pages), vCPUs (times `vcpuOvercommit` for scheduling) and scratch disk. A host without a microVM backend reports 0 for every VM dimension. |
| `capacityNodes` | integer | yes |  |
| `cordonedAt` | integer (int64) | no | unix ms; absent when not cordoned. |
| `cordonedBy` | string | no |  |
| `createdAt` | integer (int64) | yes |  |
| `dnsName` | string | no | `tw-host-<hostID>.<tailnet>`. |
| `features` | array of string | no | Host features it offers (`kv`, `r2`, `d1`, `telemetry`, …); placement needs every one a bundle uses. |
| `hostID` | string | yes |  |
| `lastSeen` | integer (int64) | yes | unix ms of the last heartbeat |
| `leases` | array of `Lease` | no | Namespace leases the host currently holds. |
| `liveNodes` | integer | yes |  |
| `nodeID` | string | no | Stable node ID of the host's `tag:tw-host` tsnet node (bound at registration). |
| `os` | string | no |  |
| `pool` | string | no |  |
| `protocols` | `Protocols` | no | Protocol versions a component speaks: `push`, `guest`, `helper`, `vz`, `rest`, `cliJSON`. |
| `push` | `HostPush` | no | Where mgmt pushes kills and assignment changes. Over the tailnet, `port` names hostd's push listener on **its own tsnet node** (the WhoIs node; mgmt never pushes anywhere else). `url` is accepted only on the local-dev socket and only as `unix:///…`. |
| `skew` | string | no | Why the host is outside the version window (placement skips it); absent when it is within. |
| `status` | `HostStatusEnum` | yes | `pending`: created by `tw host add`, never registered. `live`: heartbeated within 10 s. `stale`: registered, no recent heartbeat. `draining`: being deleted. |
| `tiers` | array of `HostTier` | yes |  |
| `updatedAt` | integer (int64) | yes |  |
| `usage` | `HostUsage` | no | Live usage against `HostCapacity`, reported with every heartbeat. `containers` is the running containers (a subset of `vms`). |
| `version` | string | no | The host's tw version, from its last registration (absent: a hostd from before version reporting, 0.1.x). |

#### `HostCapacity`

The capacity vector a host registers (ADR-032 D9). `nodes` is
tailnet nodes (= `capacityNodes`); the rest bound microVMs (DO
instances and containers alike): their count, guest memory (all, and
the part backed by 2 MiB huge pages), vCPUs (times `vcpuOvercommit`
for scheduling) and scratch disk. A host without a microVM backend
reports 0 for every VM dimension.

| Field | Type | Required | Description |
|---|---|---|---|
| `hugeMemMiB` | integer | yes |  |
| `nodes` | integer | yes |  |
| `scratchMiB` | integer | yes |  |
| `vcpuOvercommit` | number | yes |  |
| `vcpus` | integer | yes |  |
| `vmMemMiB` | integer | yes |  |
| `vms` | integer | yes |  |

#### `HostCreate`

| Field | Type | Required | Description |
|---|---|---|---|
| `hostID` | string | yes |  |
| `pool` | string | no |  |
| `tenantID` | string | no | The operator tenant whose credential mints the key (default: the tenant whose tailnet is mgmt's own). |
| `tiers` | array of `HostTier` | no |  |

#### `HostDeleteResult`

| Field | Type | Required | Description |
|---|---|---|---|
| `devicesLeft` | array of string | no | Devices mgmt could not delete (no operator credential); delete them in the console. |
| `hostID` | string | yes |  |
| `killed` | array of string | yes |  |
| `leasesReleased` | array of string | yes |  |

#### `HostDrainRequest`

| Field | Type | Required | Description |
|---|---|---|---|
| `allowLocalStateLoss` | boolean | no | Move workloads with Durable Objects too; their storage stays behind on this host. |
| `dryRun` | boolean | no |  |

#### `HostDrainResult`

| Field | Type | Required | Description |
|---|---|---|---|
| `cordoned` | boolean | yes |  |
| `dryRun` | boolean | no |  |
| `hostID` | string | yes |  |
| `moved` | array of object | yes |  |
| `refused` | array of object | yes |  |

#### `HostJoin`

A single-use join key for a host's `tag:tw-host` node. Never logged.

| Field | Type | Required | Description |
|---|---|---|---|
| `authKey` | string (password) | yes |  |
| `controlURL` | string (uri) | no |  |
| `expiresAt` | integer (int64) | yes | unix ms |
| `host` | `Host` | yes |  |
| `hostname` | string | yes | `tw-host-<hostID>` |
| `registrationSecret` | string (password) | yes | One-time secret (32 random bytes, base64url) the host presents in its first `PUT /v1/hosts/{id}`; mgmt stores only its SHA-256 and clears it once the node is bound. |
| `tags` | array of string | yes |  |
| `tailnet` | string | no |  |

#### `HostPage`

Extends `Page`.

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | array of `Host` | no |  |

#### `HostPathReport`

| Field | Type | Required | Description |
|---|---|---|---|
| `tenants` | array of `PathReport` | yes |  |

#### `HostPush`

Where mgmt pushes kills and assignment changes. Over the tailnet,
`port` names hostd's push listener on **its own tsnet node** (the
WhoIs node; mgmt never pushes anywhere else). `url` is accepted only
on the local-dev socket and only as `unix:///…`.

| Field | Type | Required | Description |
|---|---|---|---|
| `port` | integer | no |  |
| `url` | string | no | `unix:///path/hostd.sock` (local-dev only), or `inproc://` for the host embedded in the mgmt process (single-process `tw up`; accepted only from the in-process transport). |

#### `HostRegistration`

| Field | Type | Required | Description |
|---|---|---|---|
| `arch` | `Arch` | no | A Go GOARCH. Container images must match their host's. |
| `auditPubKey` | string | no | Pinned on first bind; a later registration may only repeat it. |
| `capacity` | `HostCapacity` | no | The capacity vector a host registers (ADR-032 D9). `nodes` is tailnet nodes (= `capacityNodes`); the rest bound microVMs (DO instances and containers alike): their count, guest memory (all, and the part backed by 2 MiB huge pages), vCPUs (times `vcpuOvercommit` for scheduling) and scratch disk. A host without a microVM backend reports 0 for every VM dimension. |
| `capacityNodes` | integer | yes |  |
| `features` | array of string | no |  |
| `os` | string: `darwin` \| `linux` | no | GOOS (which release tarball the host runs). |
| `pool` | string | no |  |
| `protocols` | `Protocols` | no | Protocol versions a component speaks: `push`, `guest`, `helper`, `vz`, `rest`, `cliJSON`. |
| `push` | `HostPush` | no | Where mgmt pushes kills and assignment changes. Over the tailnet, `port` names hostd's push listener on **its own tsnet node** (the WhoIs node; mgmt never pushes anywhere else). `url` is accepted only on the local-dev socket and only as `unix:///…`. |
| `registrationSecret` | string (password) | no | Required on the first registration of a host created by `POST /v1/hosts` (the `registrationSecret` from that answer); ignored once the node is bound. |
| `tiers` | array of `HostTier` | yes |  |
| `version` | string | no | The host's tw version. A host outside the version window is refused: `409 version_skew`. |

#### `HostStatus`

The heartbeat body.

| Field | Type | Required | Description |
|---|---|---|---|
| `containers` | array of `RunningContainer` | no | Every container running on this host (reservation reconciliation, ADR-032 D9): a reported reservation is renewed; a host's reservation left out for 30 s after its creation is released; a reported container with no reservation is adopted if this host holds the workload's lease (it runs; mgmt records it, over quota or not, audited `container_reserve` with `adopted`), else ignored. |
| `deployments` | array of `DeploymentReport` | no |  |
| `leases` | array of `Lease` | yes |  |
| `liveNodes` | integer | yes |  |
| `purgedTenants` | array of string | no | Deleted tenants (from `purgeTenants`) whose local state this host has removed. |
| `usage` | `HostUsage` | no | Live usage against `HostCapacity`, reported with every heartbeat. `containers` is the running containers (a subset of `vms`). |

#### `HostStatusEnum`

`pending`: created by `tw host add`, never registered. `live`: heartbeated within 10 s. `stale`: registered, no recent heartbeat. `draining`: being deleted.

Enum: `pending`, `live`, `stale`, `draining`

#### `HostStatusResponse`

| Field | Type | Required | Description |
|---|---|---|---|
| `assignmentsChanged` | boolean | no | A hint to re-list assignments (set when a push could not be delivered). |
| `purgeTenants` | array of string | no | Deleted tenants whose local state this host must remove (`tenants/<id>/`, R2 blobs); repeated until the host reports them in `purgedTenants`. |
| `revoke` | array of string | yes | Lease entries that are no longer held. |
| `revokeObjects` | array of string | yes | Object IDs killed in the last 10 min in namespaces this host holds; kill idempotently. |

#### `HostTier`

What a host offers: the isolation tiers (`process`, `microvm`) and `containers` (ADR-032: it can run containers and code sandboxes, one microVM each, for images of its `arch`).

Enum: `process`, `microvm`, `containers`

#### `HostUsage`

Live usage against `HostCapacity`, reported with every heartbeat. `containers` is the running containers (a subset of `vms`).

| Field | Type | Required | Description |
|---|---|---|---|
| `containers` | integer | yes |  |
| `hugeMemMiB` | integer | yes |  |
| `nodes` | integer | yes |  |
| `scratchMiB` | integer | yes |  |
| `vcpus` | number | yes |  |
| `vmMemMiB` | integer | yes |  |
| `vms` | integer | yes |  |

#### `ImageAlias`

| Field | Type | Required | Description |
|---|---|---|---|
| `arch` | string | no | "" = every arch. |
| `from` | string | yes |  |
| `to` | string | yes |  |

#### `ImageAliasList`

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | array of `ImageAlias` | yes |  |

#### `KVKey`

A Workers KV key as `list()` returns it (ADR-037).

| Field | Type | Required | Description |
|---|---|---|---|
| `expiration` | integer (int64) | no | Unix seconds when the key expires (absent: never). |
| `metadata` | any | no | The key's metadata (any JSON; absent: none). |
| `name` | string | yes |  |

#### `KVKeyPage`

| Field | Type | Required | Description |
|---|---|---|---|
| `cursor` | string | no | Pass as `cursor` for the next page (absent when `listComplete`). |
| `keys` | array of `KVKey` | yes |  |
| `listComplete` | boolean | yes |  |

#### `KVPut`

| Field | Type | Required | Description |
|---|---|---|---|
| `expiration` | integer (int64) | no | Unix seconds; at least 60 s ahead. |
| `expirationTtl` | integer (int64) | no | Seconds from now (at least 60). Wins over `expiration`. |
| `metadata` | any | no | Any JSON, at most 1024 bytes serialized. |
| `value` | string (byte) | yes | The value's bytes (at most 2 MiB − 4 KiB). |

#### `KVValue`

| Field | Type | Required | Description |
|---|---|---|---|
| `expiration` | integer (int64) | no |  |
| `metadata` | any | no |  |
| `name` | string | yes |  |
| `value` | string (byte) | yes | The value's bytes. |

#### `KillResult`

| Field | Type | Required | Description |
|---|---|---|---|
| `devicesDeleted` | array of string | yes |  |
| `devicesDeleting` | array of string | no | Device deletes scheduled (`-device-delete-delay`). |
| `errors` | object | no |  |
| `killed` | array of string | yes |  |
| `pushMs` | number | no |  |
| `pushed` | array of string | no | Objects whose host acknowledged the tier-1 push. |
| `warnings` | array of `KillWarning` | no | Non-fatal findings. The kill itself succeeded (tier 1 push, killed status, tombstone). |

#### `KillWarning`

| Field | Type | Required | Description |
|---|---|---|---|
| `code` | string: `device_delete_unavailable` | yes | `device_delete_unavailable`: the tenant's credential may not delete devices (an OAuth client without `devices:core`); the killed node is unreachable but its device stays in the admin console until removed. |
| `fix` | string | no | How to add the missing scope. |
| `message` | string | yes |  |
| `objects` | array of string | no |  |

#### `Lease`

| Field | Type | Required | Description |
|---|---|---|---|
| `epoch` | integer (int64) | yes |  |
| `expiresAt` | integer (int64) | no |  |
| `namespace` | string | yes | Lease name `<tenantID>/<workload>` (a workload or DO class name is also accepted on input). |

#### `MintMode`

`oauth`: a Tailscale OAuth client (scope `auth_keys`, tag `tag:tw`; optionally `devices:core` and `policy_file:read`) whose secret stays in a 0600 file on the mgmt host: the only mint mode for Tailscale tailnets. `headscale`: the Headscale profile (ADR-015): a Headscale server at `controlURL` mints tagged pre-auth keys with the Headscale API key in `apiKeyFile` (`HEADSCALE_API_KEY=…`); no Tailscale Services, no Funnel (public routes), no posture. `static`: fakemgmt only (testcontrol accepts any key). `apikey` and `wif` were removed (ADR-042): mgmt refuses to set them (`400 mint_mode_removed`); a tenant an older tailworks created in one still reports it here, but can't mint until it is switched to `oauth`.

Enum: `oauth`, `headscale`, `static`, `apikey`, `wif`

#### `NodeCertificate`

| Field | Type | Required | Description |
|---|---|---|---|
| `cert` | string | yes | `<b64url payload>.<b64url sig>` |
| `objectID` | string | yes |  |
| `oboPublicKey` | string | yes | base64 Ed25519 |
| `payload` | `NodeCertificatePayload` | yes | `internal/nodecert.Payload`; signed with the tenant's OBO root key (Ed25519). |
| `stableNodeID` | string | yes |  |
| `status` | `ObjectStatus` | yes | `pending` → `minted` → `ready`; or, for an object given a pre-joined pool node (`ObjectCreate.poolObjectID`, ADR-031), `assigned` → `ready` (never minted). Terminal: `killed` (tombstone, `410`), `failed`, and `transferred` (a `_pool` node whose node went to an object). |
| `tenantID` | string | yes |  |

#### `NodeCertificatePayload`

`internal/nodecert.Payload`; signed with the tenant's OBO root key (Ed25519).

| Field | Type | Required | Description |
|---|---|---|---|
| `exp` | integer (int64) | yes | unix seconds (24 h after iat) |
| `iat` | integer (int64) | yes | unix seconds |
| `lim` | `Caps` | no | `{capName: value \| [values]}`; see `caps ⊆ parent` semantics in `CreateObject`. |
| `objectID` | string | yes |  |
| `parent` | string | no |  |
| `root` | string | yes |  |
| `stableNodeID` | string | yes |  |
| `tags` | array of string | yes |  |
| `tenant` | string | yes |  |
| `v` | integer | yes |  |

#### `NotificationConfig`

| Field | Type | Required | Description |
|---|---|---|---|
| `tenantID` | string | yes |  |
| `updatedAt` | integer (int64) | no |  |
| `updatedBy` | string | no |  |
| `webhook` | `WebhookView` | no | A tenant's notification webhook, without its secrets. |

#### `NotificationEvent`

The JSON body mgmt POSTs to a tenant's webhook. `text` is one line
for chat tools that read it (Slack, Mattermost, Google Chat
incoming webhooks). `link` opens the row in the admin UI's
approvals page; whoever opens it still decides with their own
tailnet identity and grants.

| Field | Type | Required | Description |
|---|---|---|---|
| `cli` | string | no | The `tw approve` command line. |
| `event` | `NotificationEventKind` | yes | `approval_requested` (a Workflow instance started waiting for an approval), `test` (`tw notify test`; never subscribed to). |
| `facts` | object | no |  |
| `instance` | string | no |  |
| `link` | string | no | `https://<mgmt>/ui/approvals?tenant=<tenant>#<instance>` |
| `sentAt` | string (date-time) | yes |  |
| `step` | string | no |  |
| `tenantID` | string | yes |  |
| `text` | string | yes |  |
| `workflow` | string | no |  |
| `workload` | string | no |  |

#### `NotificationEventKind`

`approval_requested` (a Workflow instance started waiting for an approval),
`test` (`tw notify test`; never subscribed to).

Enum: `approval_requested`, `test`

#### `NotificationTestResult`

| Field | Type | Required | Description |
|---|---|---|---|
| `delivered` | boolean | yes |  |
| `error` | string | no |  |
| `status` | integer | no | The webhook's HTTP status, when it answered. |

#### `NotificationsPut`

| Field | Type | Required | Description |
|---|---|---|---|
| `webhook` | `WebhookPut` | yes |  |

#### `OTLPExport`

A tenant's OTLP export, without header values.

| Field | Type | Required | Description |
|---|---|---|---|
| `endpoint` | string (uri) | yes | OTLP/HTTP base URL; signals go to `<endpoint>/v1/<signal>`. |
| `headerNames` | array of string | yes | Names of the headers sent with every export (values are never returned). |
| `signals` | array of `TelemetrySignal` | yes |  |

#### `OTLPExportPut`

| Field | Type | Required | Description |
|---|---|---|---|
| `endpoint` | string (uri) | yes | http:// or https:// base URL of an OTLP/HTTP receiver on the tailnet (no user info, query or fragment). |
| `headers` | object | no | Headers sent with every export, typically the collector's credentials (at most 16; values at most 4 KiB). Never logged, audited or returned. |
| `signals` | array of `TelemetrySignal` | no | What to export (default all three). |

#### `OTLPGrant`

| Field | Type | Required | Description |
|---|---|---|---|
| `endpoint` | string | yes |  |
| `headers` | object | no |  |
| `signals` | array of `TelemetrySignal` | yes |  |

#### `Object`

| Field | Type | Required | Description |
|---|---|---|---|
| `actingFor` | string | no | The human principal at the head of the chain, when known. |
| `caps` | `Caps` | no | `{capName: value \| [values]}`; see `caps ⊆ parent` semantics in `CreateObject`. |
| `cert` | string | no | The node certificate `<b64url payload>.<b64url sig>` once ready. |
| `children` | array of string | no |  |
| `createdAt` | integer (int64) | yes |  |
| `deploymentID` | string | yes |  |
| `depth` | integer | yes |  |
| `ephemeral` | boolean | yes |  |
| `host` | string | no | The lease holder when known. |
| `hostname` | string | yes | The node hostname (hostd may prepend its `-hostname-prefix`). |
| `killReason` | string | no |  |
| `lease` | string | yes |  |
| `lineage` | array of string | yes | Object IDs from the root down to this object (inclusive). |
| `magicDNS` | string | no |  |
| `namespace` | string | yes | DO class name, or `_workload`. |
| `nodeID` | string | no |  |
| `objectID` | string | yes |  |
| `objectName` | string | yes |  |
| `parent` | string | no | Parent object ID (spawn). |
| `root` | string | yes | Root of the spawn tree (itself for a root). |
| `stableNodeID` | string | no |  |
| `status` | `ObjectStatus` | yes | `pending` → `minted` → `ready`; or, for an object given a pre-joined pool node (`ObjectCreate.poolObjectID`, ADR-031), `assigned` → `ready` (never minted). Terminal: `killed` (tombstone, `410`), `failed`, and `transferred` (a `_pool` node whose node went to an object). |
| `tags` | array of string | yes |  |
| `tenantID` | string | yes |  |
| `tombstone` | `Tombstone` | no | The kill record of an object. Kept until the tenant's terminal objects are purged. |
| `ttl` | integer (int64) | no | seconds |
| `updatedAt` | integer (int64) | yes |  |
| `workload` | string | yes |  |

#### `ObjectCreate`

| Field | Type | Required | Description |
|---|---|---|---|
| `actingFor` | string | no | The principal this object acts for (feeds audit). |
| `caps` | `Caps` | no | `{capName: value \| [values]}`; see `caps ⊆ parent` semantics in `CreateObject`. |
| `epoch` | integer (int64) | no | The lease epoch the calling host holds for the workload. hostd always sends it. A host may only create objects on leases it holds: the create is fenced on the lease's holder (and, when `epoch` is given, its epoch) in the insert transaction; otherwise `409 lease_lost`. |
| `hostname` | string | no | The hostname hostd proposes; mgmt answers with the one to use. |
| `namespace` | string | yes | DO class name, `_workload`, or `_pool` (a pre-joined pool node; then `objectName` is `<Class>/<suffix>` and `workload` is required). |
| `objectName` | string | yes |  |
| `parent` | `ObjectRef` | no |  |
| `poolObjectID` | string | no | A ready `_pool` node of this tenant, workload and class with exactly the class's tags, offered for a new, non-ephemeral object. If every condition holds, the object is created `assigned` to that node (no mint) and the pool node becomes `transferred`, in one transaction; the host renames the node and sends `ready` with the same `stableNodeID`. Otherwise it is ignored and the object is minted. |
| `tenantID` | string | yes |  |
| `traceID` | string | no |  |
| `ttl` | string \| integer | no | `10m`-style duration or seconds. Defaults to the class `ttl`. |
| `workload` | string | no | Always sent by hostd; see resolution rules in `CreateObject`. |

#### `ObjectPage`

Extends `Page`.

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | array of `Object` | no |  |

#### `ObjectPatch`

JSON Merge Patch. Hosts send the `ready` group; admins send `ttl`.

| Field | Type | Required | Description |
|---|---|---|---|
| `epoch` | integer (int64) | no | Fences the ready commit. hostd always sends it; with or without it, a host may only report ready on objects whose lease it holds (`409 lease_lost`). |
| `magicDNS` | string | no |  |
| `nodeID` | string | no |  |
| `stableNodeID` | string | no |  |
| `status` | string: `ready` | no |  |
| `ttl` | integer \| null (int64) | no | Admin. Seconds; `null` clears. |

#### `ObjectRef`

| Field | Type | Required | Description |
|---|---|---|---|
| `objectID` | string | yes |  |

#### `ObjectStatus`

`pending` → `minted` → `ready`; or, for an object given a pre-joined
pool node (`ObjectCreate.poolObjectID`, ADR-031), `assigned` → `ready`
(never minted). Terminal: `killed` (tombstone, `410`), `failed`, and
`transferred` (a `_pool` node whose node went to an object).

Enum: `pending`, `minted`, `assigned`, `ready`, `killed`, `failed`, `transferred`

#### `Page`

The generic page envelope (`Page<T>`); every concrete `*Page` schema
below is this shape with a typed `items`. `nextCursor` is `null` on
the last page.

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | array of any | yes |  |
| `nextCursor` | string \| null | yes | Pass as `?cursor=` for the next page; `null` when there is none. |

#### `PathClass`

How a host reaches a target: direct WireGuard, a Tailscale peer relay, or DERP; `unresolved` (no netmap peer matches the target), `unreachable` (no ping answered), `unmeasured` (no fresh measurement; placement only).

Enum: `direct`, `peer-relay`, `derp`, `unresolved`, `unreachable`, `unmeasured`

#### `PathReport`

| Field | Type | Required | Description |
|---|---|---|---|
| `targets` | array of `PathSample` | yes |  |
| `tenantID` | string | yes |  |

#### `PathSample`

| Field | Type | Required | Description |
|---|---|---|---|
| `ageMs` | integer (int64) | yes |  |
| `derpRegion` | string | no |  |
| `path` | `PathClass` | yes | How a host reaches a target: direct WireGuard, a Tailscale peer relay, or DERP; `unresolved` (no netmap peer matches the target), `unreachable` (no ping answered), `unmeasured` (no fresh measurement; placement only). |
| `peer` | string | no | The netmap peer measured (the best one when a tag matches several). |
| `rttMs` | number | no |  |
| `target` | string | yes | A `tailnet.connects` entry: a tag or a peer name. |

#### `PlacementDecision`

Why a deployment is on its host (smart placement, ADR-033).

| Field | Type | Required | Description |
|---|---|---|---|
| `aggregate` | string: `worst` \| `sum` | no |  |
| `host` | string | yes |  |
| `hosts` | array of object | no | The candidates, eligible ones best first. |
| `mode` | string: `smart` \| `off` | yes |  |
| `reason` | string: `pinned` \| `sticky` \| `smart` \| `capacity` | yes | pinned: `host` was given; sticky: kept on the current lease holder; smart: the best measured path; capacity: no measured path decided, so the most free capacity. |
| `summary` | string | yes |  |
| `targets` | array of string | no |  |

#### `PreflightMissing`

| Field | Type | Required | Description |
|---|---|---|---|
| `dst` | string | yes |  |
| `snippet` | string | yes | The grant to add (HuJSON). |
| `src` | string | yes |  |

#### `PreflightResult`

| Field | Type | Required | Description |
|---|---|---|---|
| `accessWarnings` | array of string | no | Grants to `tag:tw-mgmt` that don't do what they look like: values on `github.com/rosskukulinski/tailworks/cap/admin` (they don't narrow it), invalid `github.com/rosskukulinski/tailworks/cap/tenant-admin` values or unknown tenant IDs in them, and, on a tailnet with several tenants, `github.com/rosskukulinski/tailworks/cap/mcp` or `github.com/rosskukulinski/tailworks/cap/approve` grants (they apply to every tenant). Never make `ok` false. |
| `missing` | array of `PreflightMissing` | yes |  |
| `missingNodeAttrs` | array of string | no | Missing policy `nodeAttrs` (ADR-029 D6), e.g. `funnel on tag:tw-public` for a workload with public routes. |
| `missingTagOwners` | array of string | yes |  |
| `nodeAttrsSnippet` | string | no | The `tagOwners` + `nodeAttrs` policy to add (HuJSON). |
| `ok` | boolean | yes |  |
| `publicURL` | string (uri) | no | Where the workload's public routes will be served. |
| `service` | string | no | The Tailscale Service the workload node would host (`tailnet.service`). |
| `serviceSnippet` | string | no | The `autoApprovers` entry to add (HuJSON). |
| `serviceUnapproved` | boolean | no | The policy's `autoApprovers.services` doesn't let the workload tags host `service`: the node would advertise it but control would never route it. `ok` is false. |
| `srcTags` | array of string | no | The tags checked (workload + object tags from the bundle). |
| `tagOwnerProblems` | array of string | no | For a tenant with its own `ownerTag`, the tags its owner tag doesn't own, or that another tenant's owner tag on the tailnet owns. Makes `ok` false. |
| `tagOwnersSnippet` | string | no |  |
| `unchecked` | array of string | no | `tailnet.connects` entries that are names, not tags. |

#### `Preview`

One running preview of a workload (ADR-048).

| Field | Type | Required | Description |
|---|---|---|---|
| `alias` | string | yes |  |
| `bundleSHA256` | string | no | Hex SHA-256 of the preview's bundle. |
| `deployment` | `Deployment` | yes |  |
| `previewOf` | string | yes |  |
| `workload` | string | yes | The preview's own workload name. |

#### `PreviewList`

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | array of `Preview` | yes |  |

#### `Promotion`

A started promotion.

| Field | Type | Required | Description |
|---|---|---|---|
| `deployment` | `Deployment` | yes |  |
| `preview` | string | yes | The preview's workload name. |
| `previewStopped` | boolean | yes | The preview was undeployed. |
| `promotedFrom` | string | yes | The preview deployment whose bundle was deployed. |

#### `PromotionCreate`

| Field | Type | Required | Description |
|---|---|---|---|
| `forceTakeover` | boolean | no | As `POST /v1/deployments` `forceTakeover`. |
| `host` | string | no | Pin a host, as for a deploy. |
| `keepPreview` | boolean | no | Leave the preview running after the promotion (default: undeploy it). |
| `preview` | string | yes | The preview to promote, by alias (`3f9a1c2b`) or by its workload name (`web--preview-3f9a1c2b`). |
| `replace` | boolean | no | Re-place the workload, as `POST /v1/deployments` `replace`. |

#### `Protocols`

Protocol versions a component speaks: `push`, `guest`, `helper`, `vz`, `rest`, `cliJSON`.

#### `PublicAssignment`

A workload's public ingress (ADR-029). The host runs one extra node
for it (namespace `_public`, object name = the workload, hostname =
the workload name, `tags`) whose Funnel listener serves `routes`
while `exposed`.

| Field | Type | Required | Description |
|---|---|---|---|
| `exposed` | boolean | yes |  |
| `limits` | `PublicLimits` | yes | netd's limits on a public listener (ADR-029 D5); defaults filled in. |
| `routes` | array of string | yes |  |
| `tags` | array of string | yes |  |
| `upgrade` | array of string | no | Routes on which WebSocket/SSE upgrades are allowed. |

#### `PublicImage`

| Field | Type | Required | Description |
|---|---|---|---|
| `arch` | string: `amd64` \| `arm64` | yes |  |
| `createdAt` | integer (int64) | yes |  |
| `createdBy` | string | no |  |
| `digest` | string | yes | The linux/`arch` image manifest. |
| `id` | string | yes |  |
| `ref` | string | yes | The normalised upstream ref (or tw.local ref) deploys match. |
| `repository` | string | yes | `public/…` |
| `sourceDigest` | string | yes | The upstream top-level digest (index or manifest) at add time. |

#### `PublicImageCreate`

| Field | Type | Required | Description |
|---|---|---|---|
| `arches` | array of string: `amd64` \| `arm64` | no | Default every linux amd64/arm64 image the ref has. |
| `ref` | string | yes |  |

#### `PublicImageList`

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | array of `PublicImage` | yes |  |

#### `PublicImagePage`

Extends `Page`.

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | array of `PublicImage` | no |  |

#### `PublicIngress`

Operator gate for public (Funnel) ingress (ADR-029 D1). `allow`: the
tenant's `tailworks.jsonc` `public` routes take effect. `deny` (or absent):
a deploy that declares them is refused `403 public_ingress_not_allowed`,
and setting `deny` later makes hosts close every public listener.
`tw up` (single-tenant) creates its tenant with `allow`.

Enum: `allow`, `deny`

#### `PublicLimits`

netd's limits on a public listener (ADR-029 D5); defaults filled in.

| Field | Type | Required | Description |
|---|---|---|---|
| `burst` | integer | no |  |
| `ipBurst` | integer | no |  |
| `ipRps` | number | no | Requests per second, per client IP. |
| `maxBodyBytes` | integer (int64) | no |  |
| `maxConns` | integer | no |  |
| `maxConnsPerIp` | integer | no | Concurrent connections per client address (an IPv6 client counts per /64). |
| `maxHeaderBytes` | integer | no |  |
| `rps` | number | no | Requests per second, per workload. |
| `timeoutSeconds` | integer | no |  |
| `upgradeLifetimeSeconds` | integer | no | Longest an upgraded (WebSocket) connection stays open. |

#### `PublicNameClaim`

A tenant's claim on a public name on its tailnet (red-team H10): `https://<name>.<tailnet>/` serves only this tenant.

| Field | Type | Required | Description |
|---|---|---|---|
| `claimedAt` | integer (int64) | yes |  |
| `claimedBy` | string | no | The actor whose deploy claimed it (see Conventions); `legacy` for a name a pre-upgrade deployment already served. |
| `name` | string | yes |  |
| `tailnet` | string | yes | The tenant's tailnet, lower-cased, without a trailing dot. |
| `tenantID` | string | yes |  |

#### `PublicNamePage`

Extends `Page`.

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | array of `PublicNameClaim` | no |  |

#### `PurgeResult`

| Field | Type | Required | Description |
|---|---|---|---|
| `purged` | array of string | yes | Object IDs forgotten. |
| `warmKeysRevoked` | integer | no | The tenant's unused pooled keys revoked with the purge. |

#### `Queue`

A tenant's queue (ADR-039).

| Field | Type | Required | Description |
|---|---|---|---|
| `backlog` | integer (int64) | yes | Messages kept: not yet acked, in flight or delayed included. |
| `bytes` | integer (int64) | yes | Body bytes of the kept messages. |
| `consumedAt` | integer (int64) | no | unix ms of the last claim. |
| `consumer` | string | no | The workload (or `admin`) that last claimed messages. |
| `createdAt` | integer (int64) | yes |  |
| `delayed` | integer (int64) | yes | Not yet visible (a delay or a retry delay). |
| `inFlight` | integer (int64) | yes | Claimed by a consumer and not settled (lease not expired). |
| `incarnation` | string | yes | Random per creation. |
| `name` | string | yes |  |
| `oldestAt` | integer (int64) | no | Send time (unix ms) of the oldest message; absent when empty. |
| `tenantID` | string | yes |  |
| `updatedAt` | integer (int64) | yes |  |

#### `QueueAck`

| Field | Type | Required | Description |
|---|---|---|---|
| `acks` | array of string | no | Lease IDs to delete. |
| `deadLetterQueue` | string | no |  |
| `extend` | array of string | no | Lease IDs to keep hidden for `visibilityTimeoutMs` more. |
| `maxRetries` | integer | no | Admin pull consumers, as for pull. |
| `release` | array of string | no | Lease IDs to hand back, visible again at once, without counting the attempt (the consumer never ran them: its instance was restarting or went away). |
| `retries` | array of `QueueRetry` | no |  |
| `retryDelaySeconds` | integer | no | Admin pull consumers: the default retry delay. |
| `visibilityTimeoutMs` | integer | no | For `extend` (default 30000). |

#### `QueueAckResult`

| Field | Type | Required | Description |
|---|---|---|---|
| `acked` | integer | yes |  |
| `deadLettered` | integer | yes | Retries that went to the dead-letter queue (or were dropped) instead. |
| `extended` | integer | yes |  |
| `released` | integer | yes |  |
| `retried` | integer | yes |  |
| `stale` | integer | yes | Lease IDs that were no longer current. |

#### `QueueContentType`

How the body is encoded: `json` (UTF-8 JSON), `text` (UTF-8), `bytes` (raw), `v8` (V8 structured clone, decoded by workerd in the consumer).

Enum: `json`, `text`, `bytes`, `v8`

#### `QueueMessage`

| Field | Type | Required | Description |
|---|---|---|---|
| `attempts` | integer | yes | Deliveries so far, this one included (pull); 0 for a message never delivered. |
| `body` | string (byte) | yes |  |
| `contentType` | `QueueContentType` | yes | How the body is encoded: `json` (UTF-8 JSON), `text` (UTF-8), `bytes` (raw), `v8` (V8 structured clone, decoded by workerd in the consumer). |
| `id` | string | yes |  |
| `leaseId` | string | no | Settle the message with this (pull only). |
| `timestamp` | integer (int64) | yes | Send time (unix ms). |
| `visibleAt` | integer (int64) | no | When the message is (or was) visible (unix ms). |

#### `QueueMessageIn`

| Field | Type | Required | Description |
|---|---|---|---|
| `body` | string (byte) | yes | The encoded body (see `contentType`), at most 128 KB. |
| `contentType` | `QueueContentType` | no | How the body is encoded: `json` (UTF-8 JSON), `text` (UTF-8), `bytes` (raw), `v8` (V8 structured clone, decoded by workerd in the consumer). |
| `delaySeconds` | integer | no | Hide the message this long (default: the call's `delaySeconds`). |

#### `QueueMessageList`

| Field | Type | Required | Description |
|---|---|---|---|
| `messages` | array of `QueueMessage` | yes |  |

#### `QueuePage`

Extends `Page`.

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | array of `Queue` | no |  |

#### `QueuePull`

| Field | Type | Required | Description |
|---|---|---|---|
| `batchSize` | integer | no | At most this many messages (default 10). |
| `deadLetterQueue` | string | no | Admin pull consumers: where messages go after `maxRetries` (absent: dropped). |
| `maxRetries` | integer | no | Admin pull consumers: deliveries beyond the first before a message is dead-lettered (default 3). |
| `visibilityTimeoutMs` | integer | no | How long claimed messages stay hidden (default 30000). |
| `waitMs` | integer | no | If nothing is visible, wait this long for a message (default 0). |

#### `QueuePullResult`

| Field | Type | Required | Description |
|---|---|---|---|
| `deadLettered` | integer | no | Expired messages moved to the dead-letter queue (or dropped) by this claim. |
| `messages` | array of `QueueMessage` | yes |  |

#### `QueuePurgeResult`

| Field | Type | Required | Description |
|---|---|---|---|
| `purged` | integer (int64) | yes |  |

#### `QueueRetry`

| Field | Type | Required | Description |
|---|---|---|---|
| `delaySeconds` | integer | no |  |
| `leaseId` | string | yes |  |

#### `QueueSend`

| Field | Type | Required | Description |
|---|---|---|---|
| `delaySeconds` | integer | no | The default delay of the messages. |
| `messages` | array of `QueueMessageIn` | yes |  |

#### `QueueSendResult`

| Field | Type | Required | Description |
|---|---|---|---|
| `backlog` | integer (int64) | yes |  |
| `bytes` | integer (int64) | yes |  |
| `ids` | array of string | yes | The new messages' IDs, in order. |
| `oldestAt` | integer (int64) | no |  |

#### `ReleaseArtifact`

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes |  |
| `sha256` | string | yes |  |
| `size` | integer (int64) | yes |  |
| `version` | string | yes |  |

#### `ReleaseCatalog`

| Field | Type | Required | Description |
|---|---|---|---|
| `current` | string | yes |  |
| `items` | array of `ReleaseEntry` | yes |  |

#### `ReleaseEntry`

| Field | Type | Required | Description |
|---|---|---|---|
| `artifacts` | array of string | no | Artifacts stored on this replica. |
| `contractMigrations` | array of string | no |  |
| `date` | string | no |  |
| `notes` | array of `ReleaseNote` | yes |  |
| `protocols` | `Protocols` | no | Protocol versions a component speaks: `push`, `guest`, `helper`, `vz`, `rest`, `cliJSON`. |
| `schema` | string | no |  |
| `source` | string: `builtin` \| `imported` | no |  |
| `upgrade` | array of string | no |  |
| `version` | string | yes |  |
| `workerd` | string | no |  |

#### `ReleaseImport`

| Field | Type | Required | Description |
|---|---|---|---|
| `entries` | array of `ReleaseEntry` | yes |  |

#### `ReleaseNote`

| Field | Type | Required | Description |
|---|---|---|---|
| `kind` | string: `feature` \| `fix` \| `breaking` \| `security` | yes |  |
| `source` | string | no |  |
| `text` | string | yes |  |
| `upgrade` | array of string | no |  |

#### `ReplicaRef`

One replica of a workload with `tailworks.jsonc` `replicas` > 1.

| Field | Type | Required | Description |
|---|---|---|---|
| `deploymentID` | string | yes |  |
| `host` | string | yes |  |
| `status` | `DeploymentStatus` | no | - `assigned`: mgmt placed it; the host has not reported yet. - `starting`: the host is starting it. - `active`: serving. - `crashloop`: it served, then its workerd kept exiting; the host keeps restarting it with exponential backoff (200 ms doubling to 5 min) and reports `active` again once it answers. `error` holds the attempt count, the next delay and the last log lines. - `draining`: being replaced or undeployed. - `stopped`: stopped (undeployed, fenced or unassigned). - `failed`: the deploy failed (bad bundle or config, or it never became healthy); the host does not retry it. `error` says why. - `superseded`: a newer deployment of the workload replaced it. |
| `workload` | string | yes |  |

#### `RequestRecord`

One line of a workload's request log (`tw logs --json`,
ADR-045): a request through hostd's ingress (`kind:
request`), joined with its workerd invocation, or an invocation
with no ingress half (`kind: invocation`: a DO called from the
Worker, an alarm, a cron run), or a note that records were dropped
by the workload's rate limit (`kind: dropped`). Never holds bodies,
query strings, request headers or secret values.

| Field | Type | Required | Description |
|---|---|---|---|
| `auditTraceId` | string | no |  |
| `caller` | object | no | The verified caller (netd's WhoIs), never from tenant code. |
| `class` | string | no |  |
| `deployment` | string | no |  |
| `dropped` | integer | no |  |
| `durableObjectId` | string | no | workerd's ID of a plain-DO object whose name the invocation does not carry. |
| `durationMs` | number | no |  |
| `entrypoint` | string | no |  |
| `event` | string | no | fetch, alarm, rpc, scheduled, websocket. |
| `exceptions` | array of object | no |  |
| `host` | string | no |  |
| `kind` | string: `request` \| `invocation` \| `dropped` | yes |  |
| `logs` | array of object | no |  |
| `method` | string | no |  |
| `name` | string | no |  |
| `object` | string | no | The tailworks object ID. |
| `outcome` | string | no |  |
| `parentSpanId` | string | no |  |
| `path` | string | no | The URL path (never the query string). |
| `spanId` | string | no |  |
| `status` | integer | no |  |
| `tenant` | string | no |  |
| `time` | string (date-time) | yes |  |
| `traceId` | string | no |  |
| `truncated` | boolean | no |  |
| `workload` | string | no |  |

#### `Rollback`

A started rollback.

| Field | Type | Required | Description |
|---|---|---|---|
| `deployment` | `Deployment` | yes |  |
| `rollbackOf` | string | yes | The deployment whose bundle was redeployed. |
| `target` | `DeploymentRecord` | yes | One entry of a workload's deployment history (ADR-046). |

#### `RollbackCreate`

| Field | Type | Required | Description |
|---|---|---|---|
| `deploymentID` | string | no | The deployment to roll back to. Default the previous good one (see `CreateRollback`). |
| `forceTakeover` | boolean | no | As `POST /v1/deployments` `forceTakeover`. |
| `host` | string | no | Pin a host, as for a deploy. |
| `replace` | boolean | no | Re-place the workload, as `POST /v1/deployments` `replace`. |

#### `RunningContainer`

One running container in a heartbeat.

| Field | Type | Required | Description |
|---|---|---|---|
| `class` | string | yes |  |
| `object` | string | yes |  |
| `reservationID` | string | no | The reservation it runs under (absent: started without one, e.g. a restart while mgmt was unreachable). |
| `resources` | `ContainerResources` | yes | What one container reserves: vCPUs (fractional; Cloudflare's `lite` is 0.0625), guest memory and scratch disk (from its class's `instance_type`). |
| `tenantID` | string | yes |  |
| `workload` | string | yes |  |

#### `SchemaStatus`

| Field | Type | Required | Description |
|---|---|---|---|
| `ahead` | array of string | no | Applied migrations this replica doesn't know (a newer replica ran them). |
| `applied` | string | yes | The newest migration applied to the store. |
| `latest` | string | yes | The newest migration this replica knows. |
| `minBinary` | string | no | The oldest tw-mgmt that may open the store. |
| `pending` | array of string | no |  |
| `pendingContract` | array of string | no | Contract migrations deferred until every replica runs this version. |
| `restoreGeneration` | integer (int64) | no |  |

#### `Secret`

A workload secret's metadata. The value is never part of it.

| Field | Type | Required | Description |
|---|---|---|---|
| `createdAt` | integer (int64) | yes |  |
| `name` | string | yes |  |
| `tenantID` | string | yes |  |
| `updatedAt` | integer (int64) | yes |  |
| `updatedBy` | string | no | The actor who last set it (see Conventions). |
| `workload` | string | yes |  |

#### `SecretGrant`

A fenced release of a workload's secret values. Never logged.

| Field | Type | Required | Description |
|---|---|---|---|
| `epoch` | integer (int64) | yes |  |
| `grantedAt` | integer (int64) | no |  |
| `secrets` | object | yes | Name to value. |
| `tenantID` | string | yes |  |
| `workload` | string | yes |  |

#### `SecretPage`

Extends `Page`.

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | array of `Secret` | no |  |

#### `SecretPut`

| Field | Type | Required | Description |
|---|---|---|---|
| `value` | string (password) | yes | The secret value (UTF-8, at most 5 KiB). |

#### `SelfInfo`

| Field | Type | Required | Description |
|---|---|---|---|
| `dnsName` | string | no | `tw-mgmt.<tailnet>` |
| `https` | boolean | yes |  |
| `tailnet` | string | no |  |
| `transport` | `Transport` | no | `tsnet` in normal operation; `local-dev` when the unix socket is serving. |

#### `ServiceEndpoint`

A workload's Tailscale Service: a stable name and VIPs that survive the workload node being re-minted or moving host. Callers need a grant to the `svc:` name; control routes it to the workload node only when policy `autoApprovers.services` lets the workload tags host it.

| Field | Type | Required | Description |
|---|---|---|---|
| `addrs` | array of string | no | The Service VIPs (on create). |
| `magicDNS` | string | yes |  |
| `name` | string | yes |  |

#### `Status`

| Field | Type | Required | Description |
|---|---|---|---|
| `clock` | `StatusClock` | no | The shared clock this replica uses for leases, host liveness and expiries (ADR-028): the store's clock. `ok` is false (and the top-level `ok` with it) when this replica's wall clock is more than 2 s from the store clock, or the store clock has not been sampled for 30 s. Ownership decisions do not use the wall clock, so this is a warning, not a refusal; local-clock users (throttles) may misbehave. |
| `hosts` | object | yes |  |
| `listeners` | array of string | no | e.g. `tsnet:80`, `tsnet:443`, `funnel:443`, `unix:~/.tailworks/run/mgmt.sock`. |
| `objects` | object | yes | Count per `ObjectStatus`. |
| `ok` | boolean | yes |  |
| `pendingDeviceDeletes` | integer | yes |  |
| `replicas` | `StatusReplicas` | no | The mgmt replica registry (replicas sharing this store). `ok` is false (and the top-level `ok` with it) when this replica conflicts with another live one: file custody shared between replicas, or replicas with different custody. A replica refuses to start in that state; a conflict that appears later (a replica started with an older version, or a registry row edited) is reported here and logged. |
| `tenants` | integer | yes |  |
| `transport` | `Transport` | yes | `tsnet` in normal operation; `local-dev` when the unix socket is serving. |
| `version` | string | no |  |
| `versions` | `StatusVersions` | no | What runs where (ADR-041 §7). |

#### `StatusClock`

The shared clock this replica uses for leases, host liveness and expiries (ADR-028): the store's clock. `ok` is false (and the top-level `ok` with it) when this replica's wall clock is more than 2 s from the store clock, or the store clock has not been sampled for 30 s. Ownership decisions do not use the wall clock, so this is a warning, not a refusal; local-clock users (throttles) may misbehave.

| Field | Type | Required | Description |
|---|---|---|---|
| `message` | string | no |  |
| `offsetMs` | integer | yes | Store clock minus this replica's wall clock at the last sample. |
| `ok` | boolean | yes |  |
| `source` | string: `postgres` \| `local` | yes | `postgres`: the server clock, sampled every 5 s; `local`: SQLite, one replica. |
| `syncAgeMs` | integer | yes | Milliseconds since the last successful sample (0 for `local`). |

#### `StatusReplicas`

The mgmt replica registry (replicas sharing this store). `ok` is false (and the top-level `ok` with it) when this replica conflicts with another live one: file custody shared between replicas, or replicas with different custody. A replica refuses to start in that state; a conflict that appears later (a replica started with an older version, or a registry row edited) is reported here and logged.

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | This replica's stable ID (`<state-dir>/replica.id`). |
| `live` | integer | yes | Replicas seen within the last 30 s, this one included. |
| `message` | string | no |  |
| `ok` | boolean | yes |  |

#### `StatusVersions`

What runs where (ADR-041 §7).

| Field | Type | Required | Description |
|---|---|---|---|
| `hosts` | object | yes | Host count by reported version ("" = none reported). |
| `mgmt` | object | yes | Each live replica's version, by replica ID. |
| `schema` | `SchemaStatus` | no |  |
| `skewed` | array of string | no | Hosts outside the version window. |

#### `Store`

A platform storage object of a workload (ADR-044).

| Field | Type | Required | Description |
|---|---|---|---|
| `bytes` | integer (int64) | yes | Plaintext bytes of the kept entries. |
| `createdAt` | integer (int64) | yes |  |
| `entries` | integer (int64) | yes | Journal entries kept (after compaction). |
| `head` | integer (int64) | yes | The last sequence number appended. |
| `incarnation` | string | yes | Random per creation: a store deleted and created again has a new one, so a store object holding the old data empties itself. |
| `kind` | `StoreKind` | yes | A storage binding type (`kv`, `r2`, `d1`; later `queue`, …). |
| `name` | string | yes |  |
| `tenantID` | string | yes |  |
| `updatedAt` | integer (int64) | yes |  |
| `workload` | string | yes |  |

#### `StoreAppend`

| Field | Type | Required | Description |
|---|---|---|---|
| `after` | integer (int64) | yes | The journal head the writer has applied; the append fails with `store_conflict` unless it is still the head. |
| `entries` | array of `StoreEntryIn` | yes |  |
| `incarnation` | string | no | The incarnation the writer has applied (empty for a new store). A mismatch is a `store_conflict`. |

#### `StoreAppendResult`

| Field | Type | Required | Description |
|---|---|---|---|
| `head` | integer (int64) | yes | The new head: the entries got `after+1 … head`, in order. |
| `incarnation` | string | yes |  |

#### `StoreEntry`

| Field | Type | Required | Description |
|---|---|---|---|
| `data` | string (byte) | yes |  |
| `seq` | integer (int64) | yes |  |

#### `StoreEntryIn`

| Field | Type | Required | Description |
|---|---|---|---|
| `ckey` | string | no | Compaction key: this entry supersedes every earlier entry with the same `ckey` (for KV, the key). Empty: never compacted. |
| `data` | string (byte) | yes | The entry, opaque to mgmt (its kind's encoding), at most 2 MiB. |

#### `StoreJournal`

| Field | Type | Required | Description |
|---|---|---|---|
| `entries` | array of `StoreEntry` | yes |  |
| `head` | integer (int64) | yes |  |
| `incarnation` | string | no | Empty when the store does not exist. |
| `more` | boolean | yes | True if entries after the last one returned remain: read again with `after` = its `seq`. |

#### `StoreKind`

A storage binding type (`kv`, `r2`, `d1`; later `queue`, …).

#### `StorePage`

Extends `Page`.

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | array of `Store` | no |  |

#### `TelemetryConfig`

| Field | Type | Required | Description |
|---|---|---|---|
| `otlp` | `OTLPExport` | no | A tenant's OTLP export, without header values. |
| `tenantID` | string | yes |  |
| `updatedAt` | integer (int64) | no |  |
| `updatedBy` | string | no |  |

#### `TelemetryGrant`

A fenced release of the tenant's OTLP export config. Never logged.

| Field | Type | Required | Description |
|---|---|---|---|
| `epoch` | integer (int64) | yes |  |
| `grantedAt` | integer (int64) | no |  |
| `otlp` | `OTLPGrant` | no |  |
| `tenantID` | string | yes |  |
| `workload` | string | yes |  |

#### `TelemetryPut`

| Field | Type | Required | Description |
|---|---|---|---|
| `otlp` | `OTLPExportPut` | yes |  |

#### `TelemetrySignal`

An OTLP signal.

Enum: `logs`, `metrics`, `traces`

#### `Tenant`

| Field | Type | Required | Description |
|---|---|---|---|
| `apiKeyFile` | string | no | `headscale` mode: absolute path of the Headscale API key file, read at use; the key is never stored or returned. |
| `controlURL` | string (uri) | no | Non-default control plane (testcontrol, headscale). |
| `createdAt` | integer (int64) | yes |  |
| `maxContainerMemMiB` | integer | no | Total guest memory of the tenant's running containers. |
| `maxContainerVCPU` | number | no | Total vCPUs of the tenant's running containers (fractional: `lite` is 1/16). |
| `maxContainers` | integer | no | Running containers (ADR-032), across the tenant's workloads. Not objects: they don't count against `maxObjects`. |
| `maxObjects` | integer | yes |  |
| `mintMode` | `MintMode` | yes | `oauth`: a Tailscale OAuth client (scope `auth_keys`, tag `tag:tw`; optionally `devices:core` and `policy_file:read`) whose secret stays in a 0600 file on the mgmt host: the only mint mode for Tailscale tailnets. `headscale`: the Headscale profile (ADR-015): a Headscale server at `controlURL` mints tagged pre-auth keys with the Headscale API key in `apiKeyFile` (`HEADSCALE_API_KEY=…`); no Tailscale Services, no Funnel (public routes), no posture. `static`: fakemgmt only (testcontrol accepts any key). `apikey` and `wif` were removed (ADR-042): mgmt refuses to set them (`400 mint_mode_removed`); a tenant an older tailworks created in one still reports it here, but can't mint until it is switched to `oauth`. |
| `namePrefix` | string | no | Goes in front of every tailnet name tailworks generates for this tenant (object and workload nodes, Services, public names): `<prefix>-<name>`. Up to 11 of a-z, 0-9 and inner hyphens. On a tailnet with several tenants, at most one may go without (`409 name_prefix_required`). Fixed once the tenant has deployed. |
| `oauthClientFile` | string | no | `oauth` mode: absolute path of the 0600 file with `TS_OAUTH_CLIENT_ID=…` / `TS_OAUTH_CLIENT_SECRET=tskey-client-…`, read at use on the mgmt host (a looser mode is refused). |
| `oauthClientID` | string | no | `oauth` mode: the OAuth client's public ID (`TS_OAUTH_CLIENT_ID`). The secret is never stored or returned. |
| `oboPublicKey` | string | no | base64 Ed25519 key that verifies this tenant's node certificates; hostd pins it. |
| `ownerTag` | string | no | The tag that owns this tenant's workload and object tags, and that its OAuth client is scoped to. Default `tag:tw`. Two tenants on one tailnet need their own, so neither's client can mint the other's tags (`409 shared_owner_tag`). See docs/teams.md (ADR-043). |
| `pool` | string | no | Host pool the tenant's deployments are placed on (hosts with a matching `pool`; empty = any host). |
| `publicIngress` | `PublicIngress` | no | Operator gate for public (Funnel) ingress (ADR-029 D1). `allow`: the tenant's `tailworks.jsonc` `public` routes take effect. `deny` (or absent): a deploy that declares them is refused `403 public_ingress_not_allowed`, and setting `deny` later makes hosts close every public listener. `tw up` (single-tenant) creates its tenant with `allow`. |
| `registrationsPerMinute` | integer | yes |  |
| `tailnet` | string | yes | MagicDNS suffix of the customer tailnet., examples: [tail7906d5.ts.net] |
| `tenantID` | string | yes |  |
| `tier` | `Tier` | no |  |
| `updatedAt` | integer (int64) | yes |  |

#### `TenantCreate`

| Field | Type | Required | Description |
|---|---|---|---|
| `apiKeyFile` | string | no |  |
| `controlURL` | string (uri) | no |  |
| `maxContainerMemMiB` | integer | no |  |
| `maxContainerVCPU` | number | no |  |
| `maxContainers` | integer | no |  |
| `maxObjects` | integer | no |  |
| `mintMode` | `MintMode` | yes | `oauth`: a Tailscale OAuth client (scope `auth_keys`, tag `tag:tw`; optionally `devices:core` and `policy_file:read`) whose secret stays in a 0600 file on the mgmt host: the only mint mode for Tailscale tailnets. `headscale`: the Headscale profile (ADR-015): a Headscale server at `controlURL` mints tagged pre-auth keys with the Headscale API key in `apiKeyFile` (`HEADSCALE_API_KEY=…`); no Tailscale Services, no Funnel (public routes), no posture. `static`: fakemgmt only (testcontrol accepts any key). `apikey` and `wif` were removed (ADR-042): mgmt refuses to set them (`400 mint_mode_removed`); a tenant an older tailworks created in one still reports it here, but can't mint until it is switched to `oauth`. |
| `namePrefix` | string | no | Default none (see `Tenant.namePrefix`). |
| `oauthClientFile` | string | no |  |
| `oauthClientID` | string | no |  |
| `ownerTag` | string | no | Default `tag:tw` (see `Tenant.ownerTag`). |
| `pool` | string | no |  |
| `publicIngress` | `PublicIngress` | no | Operator gate for public (Funnel) ingress (ADR-029 D1). `allow`: the tenant's `tailworks.jsonc` `public` routes take effect. `deny` (or absent): a deploy that declares them is refused `403 public_ingress_not_allowed`, and setting `deny` later makes hosts close every public listener. `tw up` (single-tenant) creates its tenant with `allow`. |
| `registrationsPerMinute` | integer | no |  |
| `tailnet` | string | yes |  |
| `tenantID` | string | no | Optional; generated (`tn_…`) when absent. |
| `tier` | `Tier` | no |  |

#### `TenantDeleteResult`

| Field | Type | Required | Description |
|---|---|---|---|
| `deploymentsStopped` | array of string | no |  |
| `devicesDeleting` | array of string | no |  |
| `killed` | array of string | yes |  |
| `purged` | array of string | no |  |
| `removed` | object | no | What the delete removed, by kind (rows, custody entries, hosts to purge). |
| `tenantID` | string | yes |  |

#### `TenantGrant`

One valid `github.com/rosskukulinski/tailworks/cap/tenant-admin` value. Allows each action in each tenant it names.

| Field | Type | Required | Description |
|---|---|---|---|
| `actions` | array of string: `data` \| `deploy` \| `kill` \| `read` \| `secrets` | yes |  |
| `approve` | array of `ApproveGrant` | no | `github.com/rosskukulinski/tailworks/cap/approve` values that apply only to approvals in these tenants (docs/approvals.md). |
| `mcp` | object | no | Admits the caller to `/mcp` for these tenants. The read tools need `read`; each write or destructive tool must be named here and its action held in `actions`. |
| `tenants` | array of string | yes |  |

#### `TenantPage`

Extends `Page`.

| Field | Type | Required | Description |
|---|---|---|---|
| `items` | array of `Tenant` | no |  |

#### `TenantPatch`

JSON Merge Patch over the updatable fields. `null` clears an optional field.

| Field | Type | Required | Description |
|---|---|---|---|
| `apiKeyFile` | string \| null | no |  |
| `maxContainerMemMiB` | integer | no |  |
| `maxContainerVCPU` | number | no |  |
| `maxContainers` | integer | no |  |
| `maxObjects` | integer | no |  |
| `mintMode` | `MintMode` | no | `oauth`: a Tailscale OAuth client (scope `auth_keys`, tag `tag:tw`; optionally `devices:core` and `policy_file:read`) whose secret stays in a 0600 file on the mgmt host: the only mint mode for Tailscale tailnets. `headscale`: the Headscale profile (ADR-015): a Headscale server at `controlURL` mints tagged pre-auth keys with the Headscale API key in `apiKeyFile` (`HEADSCALE_API_KEY=…`); no Tailscale Services, no Funnel (public routes), no posture. `static`: fakemgmt only (testcontrol accepts any key). `apikey` and `wif` were removed (ADR-042): mgmt refuses to set them (`400 mint_mode_removed`); a tenant an older tailworks created in one still reports it here, but can't mint until it is switched to `oauth`. |
| `namePrefix` | string \| null | no | Only before the tenant's first deploy (`400 immutable_field` after). |
| `oauthClientFile` | string \| null | no |  |
| `oauthClientID` | string \| null | no |  |
| `ownerTag` | string \| null | no | Move the tenant to its own owner tag (`null` = back to `tag:tw`). Existing nodes keep their tags; set `oauthClientFile` to a client scoped to the new tag in the same patch. |
| `pool` | string \| null | no |  |
| `publicIngress` | string \| null | no | The ADR-029 gate: `allow` or `deny` (anything else is `400 bad_request`); `null` clears it (= `deny`). |
| `registrationsPerMinute` | integer | no |  |
| `tier` | `Tier` | no |  |

#### `Tier`

Enum: `process`, `microvm`

#### `Tombstone`

The kill record of an object. Kept until the tenant's terminal objects are purged.

| Field | Type | Required | Description |
|---|---|---|---|
| `by` | string | yes | The actor (see Conventions). |
| `cascade` | boolean | yes | True when this object died as part of a cascade. |
| `deviceDeleteAt` | integer (int64) | no | unix ms when the scheduled device delete runs; absent once done. |
| `deviceDeleted` | boolean | yes | Tier 2 done (false while the delete is still scheduled). |
| `killedAt` | integer (int64) | yes | unix ms |
| `namespace` | string | yes |  |
| `objectID` | string | yes |  |
| `objectName` | string | yes |  |
| `reason` | string | no |  |
| `root` | string | yes | The object the kill was issued on. |
| `tenantID` | string | yes |  |
| `workload` | string | yes |  |

#### `TombstoneError`

`410 Gone`: the error plus the kill record. `tombstone` is absent only when the 410 is `delegation_invalid` for a spawn whose parent is dead (the parent's record is then at `GET /v1/objects/{parent}`).

Extends `Error`.

| Field | Type | Required | Description |
|---|---|---|---|
| `tombstone` | `Tombstone` | no | The kill record of an object. Kept until the tenant's terminal objects are purged. |

#### `Transport`

`tsnet` in normal operation; `local-dev` when the unix socket is serving.

Enum: `tsnet`, `local-dev`

#### `UpgradePlan`

| Field | Type | Required | Description |
|---|---|---|---|
| `actions` | array of `ReleaseNote` | no | Notes that need an operator action. |
| `blockers` | array of string | yes | Why the run can't start; empty when it can. |
| `breaking` | array of `ReleaseNote` | yes |  |
| `from` | object | yes |  |
| `path` | array of string | no | Every release between the oldest running version and `to` that has a contract migration (each must be passed through). |
| `steps` | array of `UpgradeStep` | yes |  |
| `to` | string | yes |  |
| `warnings` | array of string | yes |  |

#### `UpgradeRequest`

| Field | Type | Required | Description |
|---|---|---|---|
| `canary` | string | no | The canary host (default: the first host in order). |
| `dryRun` | boolean | no |  |
| `hosts` | array of string | no | Upgrade only these hosts (default: every host below `to`). |
| `settleSeconds` | integer | no | How long a host may take to re-register and settle (default 300). |
| `soakSeconds` | integer | no | How long to watch the canary before the rest (default 300). |
| `to` | string | yes | The target release version (it must be in the catalog). |

#### `UpgradeRun`

| Field | Type | Required | Description |
|---|---|---|---|
| `createdAt` | integer (int64) | yes |  |
| `createdBy` | string | no |  |
| `id` | string | yes |  |
| `plan` | `UpgradePlan` | no |  |
| `reason` | string | no | Why the run is paused, failed or aborted. |
| `state` | string: `running` \| `paused` \| `done` \| `aborted` \| `failed` | yes |  |
| `step` | integer | no | Index of the current step. |
| `steps` | array of `UpgradeStep` | yes |  |
| `to` | string | yes |  |
| `updatedAt` | integer (int64) | yes |  |

#### `UpgradeStep`

| Field | Type | Required | Description |
|---|---|---|---|
| `canary` | boolean | no |  |
| `detail` | string | no |  |
| `finishedAt` | integer (int64) | no |  |
| `host` | string | no |  |
| `kind` | string: `backup` \| `mgmt` \| `contract` \| `host` | yes |  |
| `method` | string: `drain` \| `bounce` | no | Hosts: `drain` moves its workloads off first; `bounce` restarts in place (workloads with host-local state, or nowhere to move). |
| `needsOperator` | string | no | A command the operator must run before this step can finish. |
| `startedAt` | integer (int64) | no |  |
| `status` | string: `pending` \| `running` \| `waiting` \| `done` \| `skipped` \| `failed` | yes |  |

#### `VolumeKeyGrant`

A fenced release of a volume's data key. Never logged.

| Field | Type | Required | Description |
|---|---|---|---|
| `epoch` | integer (int64) | yes |  |
| `grantedAt` | integer (int64) | no |  |
| `key` | string (password) | yes | 32 bytes. |
| `tenantID` | string | yes |  |
| `volumeID` | string | yes |  |
| `workload` | string | yes |  |

#### `WebhookPut`

| Field | Type | Required | Description |
|---|---|---|---|
| `events` | array of `NotificationEventKind` | no | What to send. Default `[approval_requested]`. |
| `headers` | object | no | Headers sent with every notification (at most 16; values at most 4 KiB). Never logged, audited or returned. |
| `url` | string (uri) | yes | http:// or https:// URL (no user info or fragment). Treated as a secret: a chat webhook's path is its credential. |

#### `WebhookView`

A tenant's notification webhook, without its secrets.

| Field | Type | Required | Description |
|---|---|---|---|
| `events` | array of `NotificationEventKind` | yes |  |
| `headerNames` | array of string | yes |  |
| `origin` | string | yes | The URL's scheme and host (`https://hooks.slack.com`); its path and query are never returned. |

#### `WhoAmI`

| Field | Type | Required | Description |
|---|---|---|---|
| `actor` | string | yes |  |
| `admin` | boolean | yes |  |
| `approveGrants` | array of `ApproveGrant` | no | The values of the caller's `github.com/rosskukulinski/tailworks/cap/approve` grants toward `tag:tw-mgmt` (docs/approvals.md): what it may decide through `tw approve` and the admin UI. Empty when it holds none. |
| `approver` | boolean | no | The caller holds `github.com/rosskukulinski/tailworks/cap/approve` and may decide Workflow approvals its grants cover (`tw approve`). Always false for the local operator, which has no personal identity. |
| `caps` | array of string | no | The `github.com/rosskukulinski/tailworks/cap/*` capabilities the caller holds toward mgmt (WhoIs CapMap), sorted. Empty for the local operator and hosts. |
| `hostID` | string | no |  |
| `invalidTenantGrants` | array of string | no | The caller's `github.com/rosskukulinski/tailworks/cap/tenant-admin` values that grant nothing, each with the reason (missing `tenants` or `actions`, an unknown action, a wildcard). |
| `kind` | string: `user` \| `node` \| `host` \| `local` | yes |  |
| `login` | string | no |  |
| `mcpTools` | array of string | no | The MCP tools `/mcp` lets the caller use without a `Tw-MCP-Tools` header, in `tools/list` order. |
| `mgmt` | `SelfInfo` | yes |  |
| `nodeID` | string | no |  |
| `tags` | array of string | no |  |
| `tailnet` | string | no | The caller's own tailnet (the MagicDNS suffix of its node, from WhoIs). Absent for the local operator and hosts. |
| `tenantGrants` | array of `TenantGrant` | no | The caller's valid `github.com/rosskukulinski/tailworks/cap/tenant-admin` values: the tenants it may act in, and how. Empty when it holds none. An admin's values are listed too but don't narrow it. |
| `tenants` | array of `WhoAmITenant` | no | The tenants the caller can act in without knowing their IDs: those whose `tailnet` is the caller's own tailnet. The local operator gets every tenant; hosts get none. The admin UI's approvals page and `tw approvals` / `tw approve` default to these. A tailnet admin can still list every tenant (`GET /v1/tenants`). |

#### `WhoAmITenant`

| Field | Type | Required | Description |
|---|---|---|---|
| `tailnet` | string | yes |  |
| `tenantID` | string | yes |  |

#### `WorkloadPublic`

| Field | Type | Required | Description |
|---|---|---|---|
| `exposed` | boolean | yes | `tw expose on` (true, the default) or `off`. |
| `tenantID` | string | yes |  |
| `updatedAt` | integer (int64) | no |  |
| `updatedBy` | string | no | The actor who last toggled it (see Conventions). |
| `workload` | string | yes |  |

#### `WorkloadPublicPatch`

| Field | Type | Required | Description |
|---|---|---|---|
| `exposed` | boolean | yes |  |

