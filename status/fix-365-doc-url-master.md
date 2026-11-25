Last update: 2026-11-24 PT. Error doc_url links name blob/master (#365).

## What changed

- `hostd/gen/js/svc.js` exports `DOC` (`.../blob/master/docs/errors.md#`);
  `platform.js` imports it from `tw-svc.js` instead of keeping its own
  `blob/main` copy. Both runtime-wrapper error bodies now link to the
  default branch directly instead of relying on GitHub's branch redirect.
- `hostd/gen/docurl_test.go` (`TestDocBaseMatchesNetd`) pins the JS `DOC` to
  `netd.DocBase` and rejects any `/blob/main/` in either file.
- `mgmt/mcp.go` `toolError` uses `mgmt.DocBase` instead of a hardcoded URL
  (same value).

Checked with `git grep -nE '(blob|tree)/main'`: the rest are links to other
projects' repos (`docs/alternatives.md`, which really use `main`), the
recorded `docs/assets/demo/demo.cast` (a recording of past output, left as
is) and another workstream's status file.

## Tests

- `go test ./hostd/gen -run TestDocBaseMatchesNetd`: ok
- `make lint`: ok
- `make test` (via the remote test runner): see PR
- `make changes-check`: ok (no upgrade-relevant paths changed)
- Skipped `make e2e-compat` (runtime wrapper touched): needs a real tailnet.
