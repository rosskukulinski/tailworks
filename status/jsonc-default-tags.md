Last update: 2026-11-24 PT. tailworks.jsonc: `tailnet.tags` defaults to `tag:tw-wl-<workload>` (#491); `tailnet.objects.<Class>.agent` no longer read (#470).

# jsonc-default-tags

## What's built

- **#491** `cmd/tw/defaulttags.go`: when `tailworks.jsonc` has no
  `tailnet.tags`, `loadAppWith` sets `tag:tw-wl-<workload>` and writes it
  into `a.TwRaw` with a hujson patch (comments kept), so the bundle mgmt,
  hostd and fakemgmt read carries it. `buildNode` now packs `a.TwRaw`
  instead of the on-disk copy build-node.mjs makes. An explicit `[]` is
  still an error. `tw init --tags` adds `tailnet.tags` when the template
  has none (`setTags`). `--keep-tags` help says it keeps class tags only.
  Templates `hello`, `static-site`, `python` drop the tag line; `ai-agent`'s
  workload tag is now `tag:tw-wl-ai-agent` (matches its name, resolves the
  #510 note). Docs: tailworks-jsonc.md, wrangler.md (Environments), skills,
  dated notes in RFC §4.3 and ADR-043 D5.
- **#470** `agent` dropped from mgmt/hostd structs, `NamespaceSpec`,
  OpenAPI `ClassConfig` (`make api`), the mgmt UI's Agent column,
  templates and e2e/sdk apps. The schema keeps `agent` as deprecated,
  "accepted and ignored"; the `"agent": true` test fixtures stay as the
  regression test that it is still accepted.

## Security review (#491)

The default is only CLI-side and lands in the bundled file, so mgmt
validates it exactly like a written tag: `validateObjectTag` (syntax,
reserved `tag:tw`, `tag:tw-mgmt`, `tag:tw-host`, ...), `checkDeployTeam` /
`tagOwnerProblem` (ownership by the tenant's owner tag, refusal when
another tenant's owner tag owns it), and the tagOwners preflight. The
caller grant is still written only when the same write adds the tag to
`tagOwners`, after the user approves the printed lines. It is the value
the no-file path has always used. Nothing a workload can do widens.

## Tests

- `make lint`: ok.
- `make test`: the first run failed in cmd/tw, mgmt, hostd and hostd/gen
  because the disk filled ("no space left on device" in the linker) and the
  load average was 150. Rerun alone with `go test -race -count=1`: all four
  ok. The other 59 packages were ok in the full run. `make test-js`: ok.
- New: `TestDefaultWorkloadTags`, `TestDefaultWorkloadTagsBundled`
  (workerd and node build paths; `mgmt.BundleConfig(...).Classes()` has
  `_workload`), `TestInitDefaultTags`, `TestInsertWorkloadTags`.
- `make templates-typecheck`: ok. `make test-sdk`: ok. `make e2e-wrangler`:
  ok. `make e2e-upgrade`: ok (changes-check: `protocol`).
- `tw types` in the changed templates: unchanged.
- Skipped (no Docker daemon on this machine): `make e2e-compat`,
  `make demo-test`, `make e2e-headscale`.
