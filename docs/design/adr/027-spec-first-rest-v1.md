# ADR-027: Spec-first REST v1: OpenAPI 3.1 as the source of truth, real CRUD, pagination from day one, tsnet-only transport

**Date:** 2026-11-08
**Status:** Accepted (Ross). Implementation: tailworks M11.
**Context:** The v0 API grew from the hostd↔mgmt contract markdown: POST-verb state transitions (`/kill`, `/ready`, `/remint`, `/purge`), no `PATCH`/`DELETE`, no pagination, and a unix socket + host token in normal operation. Ross: "Normal operation should be tsnet only"; "add pagination now, it's a pain to do it later"; "all POST seems like an anti-pattern in sound REST design"; "be spec first: build the API spec (OAS) and then implement from that".
**Decision:**
1. `api/openapi.yaml` (OAS 3.1) is the single source of truth. Server interfaces, types and the Go client are generated (oapi-codegen); requests are validated against the spec in tests; the markdown reference is generated. The spec is reviewed before handlers are written.
2. `/v1` replaces `/v0` (pre-release, no shim). Nouns with `GET`/`POST`/`PATCH` (JSON Merge Patch, `If-Match`/`ETag`)/`DELETE` (query-param options). Kill = `DELETE /objects/{id}?cascade&reason` → `202`, then `410 Gone`; purge = `DELETE /tenants/{id}/objects?state=killed`; ready = `PATCH /objects/{id}`; credentials = `POST /objects/{id}/credentials?epoch` (a create; remint is a reason on it). `POST` remains only for creates and computations; preflight is `POST /v1/deployments?dryRun=true` (decided by Ross, 2026-11-08) and audit verification is `GET /v1/audit/verification`.
3. Cursor pagination (`items`/`nextCursor`, `limit` ≤ 500, stable `(createdAt,id)` order) on every list endpoint, with store indexes.
4. Transport: hostd is its own `tag:tw-host` tsnet node (key minted by mgmt via WIF); mgmt↔hostd and CLI↔mgmt are WhoIs-authenticated over the tailnet. Unix socket + host token survive only behind `--local-dev` for `tw dev` and hermetic tests.
**Consequences:** breaking change to every internal client, harnesses and the CLI; audit on every mutating op (`x-tw-audit`); roles declared per operation (`x-tw-role`).
