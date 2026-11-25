Last update: 2026-11-21 PT. Issue #74 usability and newcomer gaps: what was left after the rename, done.

# docs-74-usability (issue #74)

## Already done on master before this branch

- `SECURITY.md` (GitHub private vulnerability reporting, scope, supported
  versions) and `CHANGELOG.md` (rendered from the release catalog by
  `scripts/release-notes.sh --finalize`, checked by `TestChangelogUpToDate`).
- README's install line is `sh get.sh` (latest) with a `vX.Y.Z` placeholder.
- README, CHANGELOG and SECURITY are in `tw docs search` (`rootdocs.go`).
- `docs/cli.md` is generated (`make cli-docs`, `TestCLIReferenceUpToDate`).
- WIF and API-key minting were removed (ADR-042), so there is nothing to
  split out of `docs/INSTALL.md` into an auth-modes page. What's left is
  the short "Upgrading from API-key or WIF mode" section.

## This branch

- `CONTRIBUTING.md`: set up, `make lint test`, the extra suites (links
  AGENTS.md's Definition of done), generated files, `changes/` fragments,
  `status/` files. Linked from README, docs/README.md and llms.txt.
- Root `templates/` removed; its per-template descriptions moved into
  `js/packages/create-tailworks/README.md`, next to the templates (and
  shipped with the npm package).
- README: one **Prerequisites** table (run, `tw dev`, build from source
  Go ≥ 1.27 / Node ≥ 20, Node apps Node ≥ 24, the microVM tier) and the
  line that Windows isn't supported, even as a CLI client. INSTALL.md
  says the same and links the table.
- `cmd/tw/docrefs_test.go`: `TestDocsNameRealCommands` extracts every
  `tw <cmd> [sub…] [--flag]` from README.md, CONTRIBUTING.md, SECURITY.md,
  `docs/*.md` and `docs/contracts/*.md` (inline code and shell code
  blocks) and checks each against the completion tree built from
  `commands` and the flag sets. `TestDocRefCheckFindsMistakes` proves it
  flags a made-up command, subcommand and flag. Allowed on purpose:
  `tw r2 object get|put` (answers not_supported), `tw up --wif` (removed,
  named in the upgrade note); kv's `ls`/`rm` aliases.
- docs/ADMIN-UI.md said tenant admins get 403 from the console; they get
  the console for their tenants (mgmt/ui/ui.go admits TenantGrants).
  Fixed, with `mgmt/ui/tenantadmin_test.go` pinning both directions.
- docs/INSTALL.md's policy snippet shows the optional `tcp:9464` metrics
  grant (commented out) that observability.md describes.

## Not done here

- docs/design/ is not covered by the docs-vs-CLI test: the RFC and ADRs
  record proposals and removed commands. Running the check over them
  finds 17 references: `tw login`, `tw tail`, `tw connect`, `tw restore`
  (RFC, ADR-012), `tw mgmt migrate` (ADR-021, ADR-041), `tw host upgrade`
  and `tw upgrade --to` (ADR-041), `tw r2 bucket delete` (ADR-036; the
  CLI says `rm`), `tw tenant set --wif-client-id` and `tw doctor
  --tailnet/--api-key-file` (ADR-042, removed). Whether to amend decided
  ADRs is Ross's call.
- Template checks in CI nightly: left to the ci.yml work (#163).
- The other items in the 2026-11-16 critique comment: an operator
  footprint page and flagging not_supported paths in the README
  compatibility table.

## Tests

- `make lint`: ok.
- `go test ./cmd/tw -run 'TestDocsNameRealCommands|TestDocRefCheckFindsMistakes'`,
  `go test ./mgmt/ui -run TestConsoleAdmitsTenantAdmins`,
  `go test ./docs/`: ok.
- `make test` (mbp runner, `mbp-test.sh test`): ok, exit 0, no failures.
