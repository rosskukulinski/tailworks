# unlisted-do-plain (#492)

Unlisted Durable Object classes are internal (`"node": false`, plain DO)
whether or not tailworks.jsonc exists.

- `cmd/tw/defaultobjects.go`: `defaultInternalClasses` writes
  `{"node": false}` into the packed tailworks.jsonc (comments kept, as
  `defaultWorkloadTags` does) for every wrangler `durable_objects` class
  (no `script_name`) missing from `tailnet.objects`. `defaultTwConfig`
  now carries only the tags; the classes come from the same default.
  `estimateAppNodes` needs no change: it reads the defaulted config, so
  `tw deploy` lists them as sharing the workload node.
- hostd unchanged (bundle.go's facet default and nodeless.go stay for
  bundles packed by an older tw, as the issue asks). Follow-up for a later
  release: delete `facetEgressObject`'s nodeless branch and `nodelessEnsure`.
- mcp template: `objects` block dropped. Docs: egress-modes (also the stale
  "facets have no alarms" row), create-admission, security-model,
  tailworks-jsonc, wrangler, ADR-006 amendment paragraph.
- e2e/upgrade: an unlisted class `Tally` (packed old-style) whose storage
  must survive the hostd upgrade.
- e2e/compat: `ag-cross-domain` and `ag-mcp-elicitation` XPASSed (imported
  env: an internal class needs no create admission), so their expected
  failure is gone; reran both under `e2e-compat-dev` and `e2e-compat`, then
  `make compat-upstream`. COMPAT.md's imported-env row now says it works for
  unlisted classes.
- `changes/unlisted-do-plain.md`: kind breaking, `config-default`.

## Tests

See the PR body.

- `make lint`: ok (after merging origin/master).
- `make test` (mbp): all ok except `hostd TestContainerFenceInheritance`
  (timing: fenced container stopped 4.5 s after the partition, under
  load); passes on a local rerun. hostd is unchanged on this branch.
- `make test-sdk`, `make e2e-upgrade` (new unlisted-class storage case),
  `make e2e-wrangler`: pass.
- `make e2e-compat` with `TW_COMPAT_ONLY` on the DO projects
  (hello-world-do, durable-chat, workflows-starter, mcp-agent,
  ag-github-webhook, ag-workflows, ag-mcp-elicitation): pass (elicitation
  XPASSed, see above); then ag-cross-domain + ag-mcp-elicitation under
  `make e2e-compat-dev` and `make e2e-compat`: pass.
