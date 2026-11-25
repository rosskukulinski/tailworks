Last update: 2026-11-25 PT. Five small flag removals from the simplification audit (#534, #516, #527, #526, #530).

# simplify-cli-flag-removals

## What changed

- **#534** `tw notify set --events` removed (flag, usage line, completion
  test). The API's `events` field, `notifyView.Events` and the `events` line
  in `tw notify` stay.
- **#516** `tw docs` registers `--limit` only for `search`; `docs ls` and
  `docs show` no longer list it.
- **#527** `tw types --remote` and `--tenant` removed, with
  `remoteSecretNames`. Secret names come from `.dev.vars`,
  `.dev.vars.example` and `secrets.required` only; the generated secrets
  group comment drops `--remote`. docs/wrangler.md and docs/secrets.md
  updated (`.dev.vars.example` holds names and placeholders only).
- **#526** `tw init --keep-tags` removed. Call sites: hello, mcp and
  ai-agent sites drop the flag (a no-op there: the workload tag was always
  renamed); agent sites use `cliflow.InitArgs` / `cliflow.AgentObjectTags`
  (`--object-tags SupportAgent=tag:tw-agent-support --object-tags
  LookupAgent=tag:tw-agent-support-sub`). Checked: the old binary's
  `--keep-tags` output and the new flags' output are byte-identical for
  agent, hello and mcp. docs/tailworks-jsonc.md and the tw-scaffold skill
  updated. No TW_INIT_KEEP_TAGS env var.
- **#530** `tw-hostd -default-tier` removed with `hostd.Config.DefaultTier`,
  `hostdrun.Options.DefaultTier` and `hostdtest.Options.DefaultTier`
  (unused outside one unit test). `tierFor` is deployment > tenant >
  process. The flag stays registered for one release, hidden from `-help`
  (cmd/tw-hostd/deprecated.go): `process` logs a deprecation warning and is
  ignored; `microvm` is a startup error naming `tw tenant set --tier
  microvm`, never a silent downgrade. No `-min-tier`. docs/INSTALL.md
  names the per-tenant default.

## Tests

See the PR body for the full list of commands and results.
