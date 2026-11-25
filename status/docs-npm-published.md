Last update: 2026-11-17 PT. The `@tailworks/*` npm packages are published; docs no longer say `npm install` 404s.

# docs-npm-published

v0.2.0 published `@tailworks/tailnet`, `@tailworks/node`, `@tailworks/mcp`
and `create-tailworks` to npm, so the "until the first release that
publishes them, `npm install` fails with `404 Not Found`" notes are stale.

- `docs/wrangler.md` (`tw types` section): dropped the 404 note; says the
  packages are on npm since v0.2.0.
- `docs/testing.md`: dropped the 404 note and the "`npm test` can't run
  outside this repository" caveat.
- `docs/RELEASING.md` step 8, one-time setup: removed the to-do "remove the
  notes from wrangler.md and testing.md".
- Grepped README.md, llms.txt, docs/, templates and `js/packages` READMEs,
  agent skills for other "not on npm yet" / 404 claims: none left.
  (`docs/design/RFC.md` "the npm scope must be reserved before launch" is a
  design record and stays.)

## Tests

- `make lint test`: pass.
- `make changes-check`: ok (no upgrade-relevant paths changed; no fragment).
