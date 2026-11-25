# docs/alternatives.md (issue #319)

Last update: 2026-11-24 PT. Added the alternatives page; docs only.

## What changed

- `docs/alternatives.md`: what tailworks adds to stock workerd; a dated
  comparison table (celld, workerd on its own, OpenOrange, Rivet, Cloudflare
  Mesh + Workers VPC, the Agents SDK on Cloudflare, Coolify/Dokploy,
  Tailscale tsnet/tsidp/Aperture) with a source link per row; what each is
  better at; the upstream stance (#297, #38); when not to use tailworks.
- Linked from `README.md` "Docs" (one added line), `docs/README.md`
  ("Understand") and `llms.txt` (`TestLLMsTxt` requires every doc there).

## How the competitor facts were checked

Each cell was read from the linked primary source on 2026-11-24 (project
READMEs and licence files, Cloudflare's and Tailscale's public pages).
Nothing was filled in from memory; a cell the source doesn't answer says
"not stated" or "not verified".

Findings that differ from the wording of issue #319:

- The Deno/Cloudflare post says the teams are "merging workerd and celld"
  and "merging code and ideas from celld back into workerd". It doesn't
  name a scheduler.
- The Mesh post's sentence is "the target service sees a Worker making a
  request" (no "only").
- workerd's main branch already has a cluster mode behind `--experimental`
  (`ClusterConfig` in `workerd.capnp`): instances share a bucket and each
  Durable Object has one owner at a time. The page says so and says
  tailworks doesn't use it yet. Whether the pinned release (1.20261121.1)
  has it was not checked.

Not verified (and marked so on the page, or left out):

- How workerd's cluster mode acknowledges writes.
- Whether the Agents SDK is supported off Cloudflare (its docs don't say).
- Whether Aperture can be self-run, and its licence.
- Coolify/Dokploy storage durability and per-container identity.
- Per-workload network identity for celld, OpenOrange and Rivet: their
  READMEs don't state one.
- OpenOrange's maturity and what "partial" Durable Objects lacks (on a
  linked page that wasn't read).

## Inconsistencies seen in tailworks' own docs

- `README.md` "Docs" labels `security/THREAT-MODEL.md` as "Security model",
  while `docs/security-model.md` is the page with that title. Not changed
  here (other branches are editing the README).
- `docs/COMPAT.md` has no row for Cloudflare's Rate Limiting binding, so
  the page makes no claim about it.

## Tests

- `go test -count=1 ./docs/...`: ok
- `make lint`: ok
- `make changes-check`: ok (no upgrade-relevant paths changed)
- `go test -count=1 ./cmd/tw -run Docs`: ok
- Full `make test` not run: docs-only change.
