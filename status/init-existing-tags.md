# init-existing-tags

`tw init --tags` / `--object-tags`: start an app on tags the tailnet policy
already has (docs/tailworks-jsonc.md#reusing-existing-tags), for tailnets
whose policy file is managed by someone else.

- `cmd/tw/inittags.go`: `parseInitTagOverrides` and `setTags` (rewrites the
  tag arrays in tailworks.jsonc in place, keeping comments; finds each array
  by its current tags and refuses when two nodes share a set and only one
  would change).
- `cmd/tw/app.go`: the two flags; checked against the template before
  anything is copied; the `tag:tw-wl-<workload>` rename is skipped when
  `--tags` is given.
- `mgmt/tagsyntax.go`: exported `ValidTagSyntax` / `IsReservedTag` so the CLI
  checks tags the way deploy does.

Gap: `tw init --json` output is unchanged (no tags field).

## Tests (2026-11-23, after merging origin/master cfb66834)

- `make lint test`: pass (exit 0), before and after the merge.
- `make cli-docs`: `docs/cli.md` regenerated.
- New: `TestSetTags`, `TestSetTagsRefuses`, `TestParseInitTagOverridesRefuses`,
  `TestInitExistingTags` (real `agent`/`hello` templates; a refused init
  leaves no directory).
- No extra suites apply (no template, spec, store or upgrade-relevant change).
