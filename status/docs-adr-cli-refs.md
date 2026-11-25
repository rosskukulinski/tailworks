Last update: 2026-11-22 PT. The RFC and numbered ADRs name today's CLI, and TestDocsNameRealCommands checks them; H23's residual is out of the ledger (#156 closed).

## Design docs name real commands

`TestDocsNameRealCommands` (`cmd/tw/docrefs_test.go`, from #221) skipped
`docs/design/**`. Run over it, it found 17 references to commands or flags
that don't exist. Each now names the current command, or says in words what
was never built or was removed. No decision changed.

- RFC: `tw login` → `tw link` (hosted browser OIDC login: not built);
  `tw tail` → `tw logs -f`; `tw connect` → described in words (the row
  already names `tw up --oauth-client-file` and `tw tenant add`);
  `tw restore --namespace` → "a per-namespace restore" (not built).
- ADR-012: `tw restore` → "no per-namespace restore".
- ADR-021: `tw mgmt migrate --store postgres://…` → "moving the store to
  Postgres" (a SQLite→Postgres copy is not built on master).
- ADR-036: `tw r2 bucket delete` → `tw r2 bucket rm`.
- ADR-041: `tw mgmt migrate` → `tw-mgmt migrate` (`--check`, `--apply`);
  `tw host upgrade` → an upgrade run (`tw upgrade --fleet`);
  `tw upgrade --to vX.Y.Z` → `tw upgrade vX.Y.Z`.
- ADR-042: the removed flags are named as flags of `tw tenant set` and
  `tw doctor`, not as commands.

The test now covers `docs/design/*.md` and
`docs/design/adr/[0-9][0-9][0-9]-*.md`. ADR drafts with slug names (AGENTS.md
rule 5) stay out until they get a number.

Follow-up: PR #237 adds `tw backup`/`tw restore` and
`tw-mgmt migrate --from sqlite`. It wasn't on master when this branch was
cut. Once it lands, ADR-021 and ADR-041 §8 ("a future SQLite→Postgres store
copy") can name `tw-mgmt migrate --from sqlite`. The "per-namespace restore"
references stay as they are: `tw restore` restores the whole install.

## H23 (#156)

Ross's decision on #156: no corroboration input for now, and the residual
comes out of the ledger. The disclosed "plausible fake path numbers choose
new placement" text is gone from `security/LEDGER.md` (H23 is now
**fixed**), from the Known gaps in `docs/security-model.md`, and from
`security/THREAT-MODEL.md`. The input-bounds fix from #223 and its tests
(`mgmt.TestRT3_H23_PlacementPoison`, `mgmt.TestRT3_H23_StoredRowsBounded`)
stay recorded.

## Tests

- `go test ./cmd/tw -run Doc ./docs/...`: pass.
- `make lint`: pass.
