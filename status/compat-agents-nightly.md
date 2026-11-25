Last update: 2026-11-24 PT. AGENTS.md: `make e2e-compat` in the "Also run" table.

# e2e-compat in the definition of done

- AGENTS.md's "Also run" table now lists `make e2e-compat` (the Cloudflare
  example-projects suite from [cloudflare-examples-compat-suite](cloudflare-examples-compat-suite.md))
  for changes to how a Cloudflare project runs unmodified: wrangler config or
  bundling, assets, bindings, `nodejs_compat`, the runtime wrapper. It is
  also in the list of credentialed suites that are fine to skip (say so in
  the PR).

## Tests

- `go test -count=1 ./docs/...`: ok. `make changes-check`: ok.
- Docs-only change: no other suites apply.
