Last update: 2026-11-24 09:45 PT. A pre-built Worker can import `cloudflare:email` (#354); ag-playground starts, one demo still fails.

# fix-354-email-stub-prebuilt

## What changed

- `hostd/gen/email.go`: the generated workerd config provides the
  `cloudflare:email` stub for every bundling path, not only the built-in
  bundler's.
  - Tenant service (and platform): an `extensions` entry with the internal
    module `cloudflare-internal:email`, which stock workerd's built-in
    `cloudflare:email` imports (`import { default as email } from
    'cloudflare-internal:email'; export const { EmailMessage } = email`)
    but which only production workerd builds in. `internal = true`, so
    tenant code can't import it by that name.
  - Facets: config extensions don't reach Dynamic Workers (tested: the
    facet still failed with the extension alone), so the facets' module
    map (`tw-modules.js`) carries a module named `cloudflare:email`; a
    loaded Worker's own module of that name takes the import before the
    built-in does. (A module named `cloudflare-internal:email` there does
    not work.)
- `internal/emailstub`: one source for the stub's code and error message.
  `internal/esbundle` uses it; `TestBuildMJSMatches` keeps
  `create-tailworks/lib/build.mjs`'s copy identical (it can't import Go).
  The bundler keeps its stub, so bundles built by a new `tw` still run on
  older hosts.
- `e2e/compat/upstream.go`: `ag-playground` was `ExpectRefused` tied to
  #354. With the fix it starts and would XPASS that expectation, but its
  probe still fails 1 of 9 demo families: `SupervisorAgent.createChild`
  fails with `Could not serialize object of type "RpcPromise"` (the
  failure #354 predicted). Now `ExpectProbeFail` under `do-rpc`. That
  failure has no issue of its own yet (filing one was not possible from
  this session); it needs one, and the `Why` should then link it.
- docs/COMPAT.md email rows, e2e/compat/README.md finding 1, the upstream
  tables (`make compat-upstream`) and `changes/fix-354-email-stub-prebuilt.md`.

## Tests

- `go test ./hostd/gen -run 'Email'`: `TestEmailImportUnbundled` (the
  issue's repro: `no_bundle` + `import { EmailMessage } from
  "cloudflare:email"`, in the Worker, a facet and a plain DO, on stock
  workerd) passes; without the extension it fails with the issue's
  `No such module "cloudflare-internal:email"`, without the facet module
  the facet fails the same way.
- `make e2e-compat-dev` (full, at f1b842e6): PASS 17, XFAIL 3
  (ag-cross-domain, ag-mcp-elicitation: #355; ag-playground: RpcPromise).
  One earlier full run had ag-push-notifications FAIL
  (`getVapidPublicKey: null`); it passed alone and in the next two full
  runs, so a flake under parallel load, unrelated.
- The deployed column for ag-playground was not re-run (`make e2e-compat`
  needs a real tailnet); its recorded XFAIL is from the #354 run.
- `make lint`, `make test`, `make e2e-wrangler`: see the PR.
