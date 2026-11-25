Last update: 2026-11-21 PT. Dependabot no longer proposes @types/node majors.

## Change

`.github/dependabot.yml`: the npm entry ignores `@types/node`
`version-update:semver-major`. Minor and patch bumps still come in the
`npm-minor-patch` group.

## Why

#180 proposed `@types/node` 24 → 26. `@types/node`'s major tracks a
Node.js major, and the repo targets older Nodes: every `setup-node` in
`.github/workflows/` (ci, nightly, release, npm-publish) uses Node 22,
and the root `package.json` has `"engines": {"node": ">=20"}`. Types for
Node 26 would let code type-check against APIs Node 20 and 22 don't have.
#180 was closed with a comment pointing here.

## Gap

The existing `^24` (root `package.json` and the node-workflow template) is
already ahead of the Node 22 that CI runs. Aligning it with the supported
Node (`^22`, or moving CI and `engines` up) is a separate decision.

## Tests

- `ruby -ryaml` parses `.github/dependabot.yml`; the npm entry's
  `ignore` is the rule above.
- `make lint`: passes. `make test` not run locally (no code changed); CI runs it.
