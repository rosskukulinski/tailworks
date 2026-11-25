Last update: 2026-11-17 PT. RFC "As built (v0.2.0)" section, ADR-012 amended, slug ADRs numbered 044-048, shipped ADRs Accepted, design-docs tests (issue #101).

# prelaunch-101-rfc-as-built

Issue #101: the RFC and ADRs described a different system from the one
shipped.

## What changed

- **RFC (`docs/design/RFC.md`).** A new "As built (v0.2.0)" section right
  after the status line: one table of RFC claims (section, claim,
  Built / Differs / Not built / Revised, what was built instead), each
  checked against the code at 6ab669c. "Revised" callouts (the ADR-042 style)
  on §3 goals 5 and 6, §3 non-goals, §9 budgets and RPO, §10 operator
  bootstrap and backup, §15 governance. The §15 repository layout is the
  real one. Every remaining mention of `tw connect`, `tw policy
  lint|test|push`, `tw tail`, `tw login`, `tw restore`, `tw-stord`, DX.md,
  `MAINTAINERS`, DCO and `rfcs/` is either replaced by what was built or
  says "not built" on the same line.
- **ADR-012** status: "Accepted; async backup not built, see #38", with the
  as-built storage and backup facts.
- **Slug ADRs numbered** in the order they were merged:
  `platform-stores` → ADR-044, `observability` → ADR-045,
  `deploy-rollback` → ADR-046, `approval-notifications` → ADR-047,
  `deploy-preview` → ADR-048. Files renamed to `NNN-<slug>.md`, every
  `ADR-<slug>` and `adr/<slug>.md` reference replaced (code comments, the
  OpenAPI spec and its generated files via `make api`, docs, Makefile,
  `schema/cli.schema.json`, a migration comment, two unreleased
  `changes/` fragments), index rows added. Other workstreams' `status/`
  files still say the slugs; they are their records (status/README.md).
- **Statuses.** ADR-029 and ADR-033 to ADR-041 flipped to Accepted (each
  surface checked in code: `tw expose`, `mgmt/placement.go`, `tw secret`,
  mgmt `/mcp`, `tw r2`/`kv`/`d1`/`queues`, `tw approvals`, `tw upgrade`);
  044 to 048 Accepted (045 for phase 1 only). ADR-004 and ADR-018 stay
  Proposed, with the reason in the index.
- **ADR index** notes that `prototype/` and `review/` files cited by early
  ADRs are not published, and links the As built section.
- **Tests.**
  - `cmd/tw:TestDesignDocsNameRealCommands`: every `tw <command>` in a code
    span or fenced block of `docs/design/**/*.md` exists in `commands`,
    and `tw policy lint|test|push` are flagged, unless the line says
    "not built".
  - `docs:TestADRIndex`: every index row links an existing
    `NNN-<slug>.md` whose number matches, rows are in order, every ADR
    file is numbered, has a row, and starts `# ADR-NNN:`.

## Tests run

- `make lint test`: pass (exit 0; includes the new `cmd/tw:TestDesignDocsNameRealCommands` and `docs:TestADRIndex`).
- After merging origin/master (SECURITY.md, RELEASING.md only): `make lint test` failed once on `hostd:TestWorkersKVMove` with a data race in `hostd/mgmtapi.(*Client).setHeaders` (client.go:197, untouched here; pre-existing). `go test ./hostd -run TestWorkersKVMove -count=3` passed. CI (`ci-ok`) passed on the pre-merge commit after one rerun of an unrelated `cmd/tw:TestCLIOAuth` audit flake.
- `CI=true go test ./docs -run TestADRIndex`: pass; with a dummy `adr/zz-draft.md` it fails under CI=true and only logs without it.

- Later merges of origin/master: `make lint test` passed twice; on the last one `hostd:TestQueues/FencedHostConsumesNothing` failed once and passed on `go test ./hostd -run TestQueues -count=2`. CI hit an unrelated `cmd/tw:TestCLIOAuth` audit flake twice.

## Not done / follow-ups

- The RFC body below the As built table is the original design; only the
  revised sections changed. A full rewrite was out of scope.
- ADR-004 (swappable node core) and ADR-018 (setec custody of node keys and
  DEKs) are neither fully built nor rejected; they stay Proposed.
