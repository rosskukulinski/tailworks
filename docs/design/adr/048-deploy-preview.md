# ADR-048: A preview is a workload of its own next to the workload; promote deploys its bundle to the workload

**Date:** 2026-11-15
**Status:** Accepted (2026-11-16, built on master after v0.2.0: `tw deploy
--preview`, `tw previews`, `tw promote`; issue #45 item 3;
results in `status/preview-deploys-issue-45.md`). Drafted as
`ADR-deploy-preview`. Builds on ADR-046 (history, retention, rollback).

**Context.**

- Issue #45 item 3 asks for `tw deploy --preview`, a second workload node
  (`<workload>-preview-<short>`) with the same tags, and `tw promote`. It
  leaves one question to an ADR: may a preview share the workload's
  Durable Object state? A preview must not take the DO lease.
- On Cloudflare, `wrangler versions upload --preview-alias` uploads a
  version without deploying it and serves it at a preview URL. Preview
  versions share the Worker's bindings and secrets; Durable Objects are not
  supported in previews. Vercel gives each deploy a preview URL and
  `vercel promote` makes one the production deploy.
- In tailworks, a workload's Durable Objects run in exactly one place: the
  `(tenant, workload)` lease, its epoch and host fencing enforce that
  (ADR-011, ADR-028). DO storage lives on the host's disk.
- Replicas (`tailworks.jsonc` `replicas`) already run extra copies of a workload
  as workloads of their own (`<workload>--r2`), each with its own lease,
  record, assignment and node, and the group's secrets and stores.
- Platform stores (KV, R2, D1) are per workload. Queues are per tenant:
  any workload that binds a queue sends to it or consumes from it.

**Decision.**

1. **A preview is a workload of its own,** `<workload>--preview-<alias>`,
   like a replica (the `--` separator is the same). `POST /v1/deployments`
   with `preview: true` (`tw deploy --preview`) deploys the bundle under
   that name. The alias is `previewAlias` (`--preview-alias`, a DNS label of
   at most 20 characters), or by default the first 8 hex digits of the
   bundle's SHA-256, so deploying the same bundle again redeploys the same
   preview. The `_workload` namespace records `previewOf`. Every
   per-workload mechanism then applies unchanged: the preview's own
   lease and epoch, deployment record and history, assignment, workload
   node, MagicDNS name and identity. A redeploy by the preview's own name
   (for example by `tw host drain`) keeps it a preview.

2. **A preview never shares Durable Object state, and never takes the
   workload's lease.** Its lease is its own, so deploying it never moves,
   renews or fences the workload's lease or host, and its Durable Objects
   start with empty storage wherever it is placed. Sharing the
   workload's DO storage would mean two copies of a DO class running
   against one store, which is what fencing exists to prevent; and a
   preview that wrote to production state would not be a preview.

3. **The preview's stores are its own too.** KV, R2 and D1 start empty
   (a store exists from its first write); D1 migrations are applied to the
   preview by name. That is stricter than Cloudflare, where a preview
   version writes to production bindings. Here, trying a version can't
   corrupt production data, and a schema change can be tried before it is
   applied to the workload.

4. **Queues are refused.** Queues are tenant-wide, so a preview that binds
   one would consume the workload's messages or inject its own. A bundle
   with queue producers or consumers is `400 preview_unsupported`. Deploy
   it under another name (`--name`) to try it with its own queues.

5. **Secrets are the workload's.** A preview is released the workload's
   secrets at its own lease's epoch, as a replica is, and the release is
   audited with `previewOf`. Copying them, or making the operator set them
   again on every preview, would add copies of the most sensitive data or
   friction without protecting anything: only an admin can deploy a
   preview, and an admin can deploy the same code to the workload.
   Cloudflare previews share secrets the same way. `docs/secrets.md` warns
   that previews run unpromoted code with those secrets.

6. **A preview takes no production traffic.** It joins no Tailscale
   Service (`tailnet.service` is ignored), has no public routes
   (`public` and per-class `public` are ignored) and runs one replica. It
   keeps the workload's tags, so callers with grants to the workload's
   tags reach it at its own MagicDNS name. Placement is the preview's own
   (smart placement as for any new workload; `--host` pins it).

7. **Promote is a deploy of the preview's bundle to the workload.**
   `POST /v1/tenants/{id}/workloads/{workload}/promotions` (`tw promote
   <workload> <alias>`) loads the preview's current bundle and calls
   `Mgmt.Deploy` for the workload, as rollback does: lease at `epoch+1`,
   host fencing, sticky placement, every deploy refusal, and the
   workload's own replicas, Service and public routes. Container images the
   preview's deploy built are reused by digest. The CLI preflights the
   bundle first (`dryRun`). Then the preview is undeployed (`keepPreview`
   keeps it). Promote is audited as `kind: promote` with `promotedFrom` and
   `preview`; it is a privileged kind that tenants can't submit. A failed
   preview is `409 preview_failed`; an unknown or stopped one is
   `404 unknown_preview`. Promote moves code and configuration only; the
   preview's data is not copied, and the workload keeps its storage.

8. **Authorization is deploy's**: `x-tw-role: admin` on
   `GET …/previews` and `POST …/promotions`; hosts get `403`.

**Consequences.**

- A version can be tried at its own address, with its own data, without
  touching the running workload, and made the workload's deployment in one
  command, without a rebuild.
- A preview of a stateful app shows the new code against empty state.
  Seeding it (or copying production data into it) is the operator's job.
- Previews are workloads: they count against the tenant's quotas, appear
  in `tw ls`, and are stopped with `tw undeploy <workload>--preview-<alias>`.
  Undeploying the workload doesn't stop its previews.
- History stays per workload: the preview's deploys are in
  `tw deployments <workload>--preview-<alias>`, the promotion in the
  workload's history (and audit, with `promotedFrom`).

**Not built.**

- Traffic splitting (`wrangler versions deploy --percentage`, canaries).
- Copying a workload's data into a preview.
- Previews of bundles that bind queues.
- An MCP `preview`/`promote` tool.
- A promote-time check that the preview's D1 migrations were applied to the
  workload.
