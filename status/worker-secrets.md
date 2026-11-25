Last update: 2026-11-14 16:00 PT. Worker secrets (ADR-034): `twd secret put|ls|rm`, lease-fenced release, workerd text bindings, node env, `.dev.vars`.

# Worker secrets

Design: [ADR-034](../docs/design/adr/034-worker-secrets.md). User
docs: [docs/secrets.md](../docs/secrets.md).

## Built

- **API** (`api/openapi.yaml`, tag `secrets`; `make api`):
  - `GET /v1/tenants/{id}/workloads/{workload}/secrets` (admin, paged by name, metadata only)
  - `PUT …/secrets/{name}` `{"value"}` (admin, 201/200, audited `secret_put`)
  - `DELETE …/secrets/{name}` (admin, 204 / 404 `unknown_secret`, audited `secret_delete`)
  - `POST …/secret-grants?epoch=N` (host; lease holder at epoch only, `409 lease_lost`, `Cache-Control: no-store`, audited `secret_release` with names when non-empty)
  - New `AuditKind`s: `secret_put`, `secret_delete`, `secret_release`, after `volume_key`.
    New `ErrorCode`s: `too_many_secrets`, `unknown_secret`.
- **mgmt** (`mgmt/secrets.go`): values sealed in custody, one entry per
  workload (`twd/<tenant>/workloads/<workload>/secrets`). The store holds
  nothing. The release uses the same fence as `VolumeKey`. Limits: 128
  secrets per workload, 5 KiB per value. Names must match
  `[A-Za-z_][A-Za-z0-9_]{0,127}`; `TWD_` and `__TWD` are reserved.
- **hostd**:
  - `hostd/secrets.go`: `deploy()` fetches the grant after `tierFor`, so a
    refusal happens before the old instance stops.
  - `hostd/gen/secrets.go`: text bindings in `config.capnp` (a secret
    overrides a var of the same name), plus `twd-config.json` vars for
    facets. Only in the 0700 instance dir.
  - Runtime `node`: env vars over `node.env` (`nodeEnv`). twd's own
    variables are refused with `bad_secret_name`.
  - microVM tier: refused with `secrets_unsupported_tier`.
  - `hostd/mgmtapi/secrets.go`: the client.
  - `hostd/fakemgmt/secrets.go`: the fake.
- **CLI** (`cmd/twd/secret.go`): `twd secret put|ls|rm [--workload] [--tenant] [--json]`.
  - The workload defaults to `loadApp(".")`.
  - The value comes from stdin, with a hidden prompt on a TTY. A value in
    argv is refused and not echoed.
  - The audit `describe` renders the three kinds.
- **`twd dev`** (`cmd/twd/devvars.go`): `.dev.vars` (dotenv) is synced into
  the dev mgmt as secrets on every (re)deploy.
- **Templates**: hello, agent, mcp and ai-agent now ship a `gitignore` with
  `.dev.vars`, and node-workflow's gains it. `create-twd.js` now renames
  `gitignore` to `.gitignore`, as `twd init` already did. The ai-agent
  `model.ts` comment points at `twd secret put LLM_API_KEY`.
- **Docs**: `docs/secrets.md`, a README "More" entry, a COMPAT.md
  "Secrets" section, a node-tier.md env line, and the ADR draft.

## Tests (2026-11-14, macOS arm64)

- `make lint`: ok, after `make api` (api-check, gofmt, every vet, check-secrets).
- `make api-lint`: 4 errors, all pre-existing in the `/v2` registry paths
  (trailing slash, no 4xx). None are in the new operations.
- `go test ./mgmt/ -run 'TestSecrets|TestV1RoleMatrix|TestSpec'`: ok.
  - `TestSecretsStore`: put/replace/list/paging/delete/limits. The value
    is never in plaintext in mgmt's dir (custody, DB), logs or audit.
  - `TestSecretsRelease`: holder at epoch only; wrong epoch, other host and
    no lease give 409; empty map for no secrets; `no-store`.
  - `TestSecretsRoles`: over WhoIs, admin vs non-admin vs object node vs host.
  - `TestSecretsReplicas`: `web--r2` gets `web`'s secrets under its own
    lease; the other host gets 409.
  - `TestV1RoleMatrix` covers the four new operations automatically.
- `go test ./hostd/ -run 'TestWorkerSecrets|TestNodeSecrets|TestApplySecrets'`: ok
  (needs `make deps`; the node test needs `e2e/node` `npm ci`).
  - `env.API_KEY` is the string in the Worker and in a facet DO, and
    overrides the var. Quotes, backslash and newline survive.
  - One grant per deploy. The value is found nowhere outside `run/i*/`.
  - An update applies on the redeploy.
- `go test ./hostd/gen/ -run Secret`: ok (bindings, facet vars, `app.Vars` not mutated, bad names).
- `go test ./cmd/twd/ -run 'TestCLISecret|TestParseDotenv|TestDevVars|TestDevOnce|TestHelp'`: ok.
  `TestDevVars` runs the real `twd dev --once` with `.dev.vars`.
- `npm test`: ok.
- `make test-go` (`go test -race` over netd, internal, cmd, mgmt, hostd,
  security and guest): exit 0. It ran before the replica fix, so
  `go test -race ./mgmt/...` was run again after it: ok.

## Gaps and next steps

- **microVM tier.** A cold boot writes the config drive
  (`<RunDir>/<vm>/config.tar`, the instance dir as a tar) outside the
  instance dir; warm and snapshot boots stream it in memory. Next step:
  send secrets over the control channel on every boot path, or keep the
  cold-boot drive in memory.
- **No restart on change.** Secrets apply on the next deploy, or when
  hostd restarts. There is no restart-in-place hook.
- **`tenant rm` doesn't wipe secrets.** Neither does a workload that is
  never deployed again: the custody entry stays until `twd secret rm`.
- **Concurrent puts across mgmt replicas** on a shared setec custody can
  race (last write wins).
- **macOS Seatbelt:** not verified whether a same-uid process can read a
  node-tier workload's environment. Linux Landlock hides other processes'
  `/proc` entries.
- **ADR index.** The ADR has no number and no row in
  `docs/design/adr/README.md` yet. Both come at merge (AGENTS.md rule 5).
