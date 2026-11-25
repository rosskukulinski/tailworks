Last update: 2026-11-25 PT. #540: `npm create tailworks` without `tw` names the app like `tw init`.

# create-tailworks-rename (issue #540)

## What changed

- `js/packages/create-tailworks/lib/init.mjs` (new): `tw init`'s rename
  (cmd/tw/app.go cmdInit, cmd/tw/inittags.go appTags) in Node: the workload
  label rule, the first `"name"` in package.json, tailworks.jsonc and
  wrangler.jsonc, `APP_NAME` in wrangler.jsonc when the template has it,
  `tag:tw-wl-<wl>` and `tag:tw-<wl>-<class>`.
- `bin/create-tailworks.js`: a real flag parser (`--flag value` and
  `--flag=value`, so a flag's value is no longer taken as the directory;
  unknown flags are refused), `--name`, and `--tags` / `--object-tags` /
  `--keep-tags` passed through to `tw init`. Without `tw` those three exit 2
  with the `npx tailworks init …` line to run instead, rather than scaffold
  without the tags asked for (tags are the grant unit). The copy path
  prints `created <dir> from template <t> (workload "<wl>")`.
- Per the issue's verifier consensus, no `npx tailworks init` shell-out.
- Docs: docs/INSTALL.md and the create-tailworks README describe the
  fallback; a comment in cmdInit points at lib/init.mjs.

## Drift guard

`cmd/tw/createtailworks_test.go` TestCreateTailworksRenamesLikeInit
scaffolds every template with `tw init` and with the JS bin (PATH holding
only node) into `My_App-2` and diffs the trees byte for byte. Skips when node
is absent. `test/init.test.mjs` covers the flag handling.

## Tests

- `go test ./cmd/tw -run TestCreateTailworksRenamesLikeInit`: ok (9 templates).
- `node --test test/init.test.mjs`: 4/4 pass; `make test-js`: ok.
- `make lint`: ok. `make changes-check`: ok (no fragment needed).
- `make templates-typecheck`: ok. `make test-sdk`: ok. `make npm-cli-test`: ok.
- `go test -race ./cmd/tw/`: ok. Full `make test`
  was not run locally: both mbp test slots stayed busy for 30 minutes; CI
  runs it.
