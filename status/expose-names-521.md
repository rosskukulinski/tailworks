Last update: 2026-11-25 PT. Issue #521: tw expose names | tw expose release <name>.

## What changed

- `cmd/tw/exposenames.go`: `tw expose names [--tenant]` (GET
  `/v1/tenants/{id}/public-names`, all pages, `--json` = schema
  `expose names`) and `tw expose release <name> [--tenant]` (DELETE with
  `Tw-Actor`, so the `public_name_release` audit record names the
  operator). The release prints what it means (anyone on the tailnet can
  claim the name next). A 404 adds the fix `tw expose names`; a 409
  `public_name_in_use` carries mgmt's fix text.
- `cmd/tw/public.go`: one dispatch line; `main.go`: the usage/help of the
  `expose` entry. Not added to `tw mcp` or the admin UI (security-perf
  amendment).
- mgmt fix (the verifier's "check the prefixed-tenant guard" note): for a
  team tenant (name prefix) the claim is `t.Name(workload)` (`pay-support`)
  but `ReleasePublicName`'s in-use guard compared it with the unprefixed
  workload and public object names, so a team tenant could release a name
  still being served. It now checks both spellings; the 409 message names
  the workload for `tw undeploy` and suggests `tw expose release`.
  `TestRT3_H10_PrefixedPublicNameRelease` fails without the fix.
- `mgmt/public_test.go` `publicHost`: a sequence number in the socket name
  (parallel tests collided on `bind: file exists`).
- Docs: public-ingress.md "Releasing" and the expose block,
  errors.md `public_name_in_use` / `public_name_owned` fixes, docs/cli.md.

## Tests

- `go test ./cmd/tw -run 'TestCLIExposeNames|TestCLIExpose|TestCompletion|TestSubcommandHelp|TestCLIJSONContract|TestAgentEval'`: ok
- `go test ./mgmt -run 'TestRT3_H10|Public'` x3: ok
- `make lint`: ok; `make test` (mbp-test.sh): ok
- Skipped: `make e2e-multimgmt` (Docker; the change is a read-only guard in
  the release path, not in leases/mints/audit plumbing). `make test-pg` not
  needed (no store change).
