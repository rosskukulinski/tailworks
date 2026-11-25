# ADR-034: Worker secrets in mgmt custody, released only to the workload's lease holder

**Date:** 2026-11-14
**Status:** Accepted (2026-11-16, built and shipped in v0.2.0: `tw secret`,
[docs/secrets.md](../../secrets.md); results in `status/worker-secrets.md`).

**Context.**

- Workers had no secrets mechanism. The only per-workload configuration was
  `wrangler.jsonc` `vars`, which are plaintext and committed to git, and
  `tw deploy --var`, which ends up inside the bundle that mgmt stores.
- The `ai-agent` template tells users to swap in a real LLM provider, but
  there was nowhere safe to put its API key.
- Cloudflare's model:
  - `wrangler secret put|list|delete` reads the value from stdin.
  - The value reaches the Worker as a text binding, so `env.NAME` is a
    string.
  - A secret and a var can't share a name on Cloudflare. Locally, `.dev.vars`
    overrides vars in `wrangler dev`.
  - The limits are 128 secrets per Worker and 5 KiB per value.
- tailworks already holds key material for workloads. ADR-018 custody is
  AES-256-GCM files in homelab mode and setec otherwise. The microVM tier's
  DO volume key (ADR-024) is in custody and is released only to the host
  that holds the workload's namespace lease at its epoch (ADR-011/028
  fencing): `POST /v1/volumes/{id}/keys?epoch=N`, `409 lease_lost`.

**Decision.**

1. **Storage: custody, one entry per workload.**
   - The entry is `tw/<tenant>/workloads/<workload>/secrets`, a JSON map of
     name to `{value, createdAt, updatedAt, updatedBy}`, sealed by custody.
     FileCustody binds the entry name as additional data, so an entry can't
     be swapped for another workload's.
   - The store (SQLite/Postgres) holds no value and no secret metadata.
     Custody is the only copy, and deleting the last secret deletes the
     entry.
   - Writes are serialised in-process. Replicas that share a setec custody
     could race two concurrent puts to the same workload; the later write
     wins. This is acceptable for an operator-driven command.

2. **API** (`api/openapi.yaml`, tag `secrets`):
   - `GET /v1/tenants/{id}/workloads/{w}/secrets` is admin-only and paged by
     name. It returns metadata only.
   - `PUT …/secrets/{name}` (`{"value": …}`; 201 created, 200 replaced) and
     `DELETE …/secrets/{name}` are admin-only and audited. They follow the
     same rules as every other tenant-scoped write: WhoIs plus
     `github.com/rosskukulinski/tailworks/cap/admin`, or the local-dev socket.
   - The workload doesn't need a deployment, so you can put secrets before
     the first deploy.
   - `POST …/secret-grants?epoch=N` is host-only. It releases the values
     under exactly the volume key's fence:
     - the lease exists;
     - it is held by the calling host;
     - it is at `epoch`;
     - it hasn't expired.

     Otherwise the answer is `409 lease_lost`, and the refusal is audited
     as `deny`. The answer carries `Cache-Control: no-store`.
   - A replica (`<workload>--r2`, which has its own lease) is fenced by its
     own lease, and gets the secrets of its group's workload.
   - No admin operation returns a value, so a compromised admin laptop can
     overwrite or delete secrets but can't read them back through the API.

3. **Release at start, to the instance only.**
   - When hostd runs an assignment, it asks for the grant at the
     assignment's epoch. It asks after it has resolved the tier, and before
     anything of the new deployment starts.
   - workerd: each secret becomes a `text` binding of the tenant worker. It
     replaces a var of the same name. It is also added, as a JSON string, to
     `tw-config.json` `vars`, which is where the platform builds a facet
     Durable Object's `env`. So `env.NAME` is the same string in the Worker,
     in facet classes and in plain-DO classes.
   - Runtime `node`: secrets are written to a `0600` file in the instance
     directory; node starts with `--require` of a preload that copies them
     into `process.env` (over `node.env` and deploy vars) and deletes the
     file. They are never in the exec-time environment: on macOS a
     same-user process can read that (`KERN_PROCARGS2`), Seatbelt can't
     refuse it, and the process tier there runs every tenant as one user. Tailworks' own variables can't be replaced, and
     a deploy that tries is refused with `bad_secret_name`.
   - Both files live in the `0700` instance directory, which is removed when
     the instance stops. Nothing is written to hostd's data directory.
   - A crash restart of the same instance reuses the values. New values
     apply on the next deploy, or when tw-hostd restarts and redeploys.
     We considered pushing a restart on `secret put`. We dropped it for now
     because no restart-in-place hook exists, and a blue/green redeploy is
     what `tw deploy` already does.

4. **microVM tier: refused for now** (`secrets_unsupported_tier`).
   - The guest gets the instance directory through the config drive.
   - On a warm (pool or snapshot) boot the bundle is streamed over the
     control channel and stays in memory.
   - On a cold boot, `config.tar` is written to the VM's run directory,
     outside the instance directory.
   - Supporting secrets there means sending them over the control channel
     on every boot path, or keeping the cold-boot drive in memory (a memfd
     or the helper). That is follow-up work.

5. **Local development.**
   - `tw dev` loads `.dev.vars` (dotenv) into its embedded mgmt as the dev
     workload's secrets, on every (re)deploy. Names that are no longer in
     the file are removed.
   - The dev hostd fetches them through the same grant path, so dev and
     production behave the same.
   - Templates ship a `.gitignore` that lists `.dev.vars`.

6. **Never logged.**
   - Values are not in logs, errors or audit records. `secret_put`,
     `secret_delete` and `secret_release` record names, the workload and
     the actor.
   - The CLI reads values from stdin (a hidden prompt on a TTY) or, for
     `tw secret bulk`, from a file or piped stdin, never from argv. It
     refuses a value given in argv and doesn't echo it, and it never prints
     a value. `tw secret bulk` names a bad line or name but doesn't echo a
     value or a JSON decoder message.
   - mgmt's PUT handler doesn't echo JSON decoder errors, because they can
     quote the value.
   - The Go grant types redact themselves when printed.

**Threat model.**

| Adversary | Can they read a secret? |
|---|---|
| Another tenant's workload on the same host | No, as far as the process sandbox holds. The sandbox (INSTALL.md "Isolation") lets a workload read and write only its own instance and data directories. It can't read tailworks' state, mgmt custody or another tenant's directories, and on Linux it can't read other processes' `/proc` entries (environment, cmdline) or memory. On macOS (Seatbelt) we have not tested whether a same-uid process can read another process's environment, which matters for runtime `node`. Without a sandbox, tailworks refuses to host a second tenant on the process tier unless `--insecure-shared-host` is set. |
| Another host of the operator | No. It doesn't hold the lease, so it gets `409 lease_lost`. A host that lost its lease (fenced, ADR-028) can't fetch values again. |
| A tailnet user without the admin cap | No: `403 not_admin`. |
| A tailnet admin, or a `tw link`ed laptop | Can set, replace and delete secrets, but no API returns a value. They could still deploy code that prints `env.NAME`, which is the same trust as on Cloudflare: whoever can deploy the Worker can read its secrets. |
| Tenant code of the same workload | Yes, by design. |
| Someone who reads mgmt's disk | They need the custody key. FileCustody keeps it in `custody.key` (0600) next to the sealed files, so this only protects against copies of the custody directory without its key, and against the database and backups. Setec moves the key off the box. |
| Someone who reads the host's disk while the workload runs | Yes, from the instance directory, if they run as the tailworks user or root. The microVM tier is the answer to that, once item 4 is done. |

**Consequences.**

- Secrets are portable from Cloudflare: the same `env.NAME`, the same CLI
  verbs and the same `.dev.vars`.
- Every deploy makes one extra mgmt call (the grant). A workload with no
  secrets gets an empty map, and there's no audit record for it.
- The microVM tier and live rotation without a redeploy are open.
- When a tenant or workload is removed, its secrets entry stays in custody
  until you `tw secret rm` its names. A follow-up should wipe it in
  `tenant rm`.
