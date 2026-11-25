Last update: 2026-11-24 PT. Issue #74 checked item by item against master (ed4d2831); the few gaps left are closed here.

# Newcomer gaps (issue #74)

## Each item against master

Done before this branch:

- `CONTRIBUTING.md`, `SECURITY.md`, `CHANGELOG.md` (#327) are at the repo root.
- The root `templates/` folder is gone.
- WIF and API-key minting were removed (ADR-042). INSTALL.md keeps only
  "Upgrading from API-key or WIF mode" (about 25 lines), so there was nothing
  to move to `docs/auth-modes.md`.
- One Prerequisites table: `docs/INSTALL.md#prerequisites`. It puts Node ≥ 18
  (npm), ≥ 20 (scaffold, source) and ≥ 24 (node tier) in separate rows.
- README's get.sh line is versionless (`curl … get.sh | sh`).
- `tw docs` serves README.md, CHANGELOG.md and SECURITY.md (`rootdocs.go`).
- Docs-vs-CLI test: `cmd/tw/docrefs_test.go` `TestDocsNameRealCommands`
  covers README, CONTRIBUTING, SECURITY, docs/*.md, contracts, the RFC and the
  numbered ADRs, and checks commands, subcommands and flags. It has an allowlist
  (`docRefAllowed`) and the footnote rule. `cmd/tw/designdocs_test.go` covers
  `tw connect` and `tw policy lint|test`.
- Generated CLI reference: `docs/cli.md` (`make cli-docs`, `TestCLIReferenceUpToDate`).
- Template checks: `templates-typecheck` runs in ci.yml on every PR (#163), and `test-sdk` runs
  nightly (`.github/workflows/nightly.yml` matrix).
- ADMIN-UI.md now says tenant admins get the console for their tenants. That
  matches `mgmt/ui/ui.go` `admit`. `mgmt/ui/tenantadmin_test.go` tests both
  directions. The "any other tailnet user → 403 `not_admin`" row is also right:
  mgmt's `TailnetHandler` refuses that user before the UI handler runs.
- Windows: INSTALL.md said it isn't supported (twice).
- INSTALL.md's policy snippet has the optional `tcp:9464` Prometheus grant
  (commented, as in observability.md).

Done here:

- README quick start: one line saying Windows isn't supported.
- `docs/footprint.md` (new): processes, ports, tailnet devices, state files,
  and the microVM tier's root-owned helper. It's linked from docs/README.md,
  INSTALL.md and llms.txt.
- README compatibility table: the `not_supported` paths inside shipped surfaces
  are now flagged. `tw r2 object get`/`put` (cmd/tw/r2.go), `ctx.tailnet.connect()`
  in plain-DO objects (js/packages/tailnet/src/runtime.ts), `newUniqueId()` in
  facet mode (hostd/gen/js/platform.js), and container snapshots and
  `exposePort` (hostd/gen/js/tw-rt.js). COMPAT.md's `do-identity` row now
  names `newUniqueId()` too.

Left for the maintainer:

- Whether to split INSTALL.md further. It is 1167 lines, and the macOS section
  runs from line 84 to 595. The WIF/API-key move the issue asked for no longer
  applies. Splitting out more (Running tailworks, multi-host, Headscale) is a
  doc-structure call.

## Tests

- `make lint-docs test-docs`: ok
- `make lint`, `make test`: see the PR body
