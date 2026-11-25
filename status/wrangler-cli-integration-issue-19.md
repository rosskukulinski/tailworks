Last update: 2026-11-15 09:30 PT. Merged origin/master (#23 compat matrix, #27 platform stores); `make lint test` and `make e2e-wrangler` pass on the merged tree.

Earlier: 2026-11-15 08:40 PT. Issue #19: wrangler.toml/.json, `--env`, no-twd.jsonc defaults, `--wrangler` / `--from-wrangler-out`, warnings for ignored keys. Spike findings below.

## Merge with master (2026-11-15)

- `hostd/bundle/bundle.go` conflicted: two new `App` fields (`ModuleTypes` here,
  `Stores` from #27). Both are kept, each in its own comment group.
- Platform stores (#27) register wrangler keys through
  `bundle.RegisterStoreKind`. A new `bundle.IsStoreKey` (`hostd/bundle/storekeys.go`)
  lets the CLI skip the "no such binding" warning for any registered key,
  so KV, D1 and the rest stop being warned about as soon as they
  land. Only the test probe kind is registered today, so `kv_namespaces`
  is still warned. Test: `TestWranglerWarningsStoreKinds`.
- COMPAT.md now follows #23's checked format:
  - The `wasm` row is now ✅: `twd deploy`'s bundlers ship `.wasm` modules,
    which fixes the gap that row named.
  - The `wrangler-config` row is rewritten.
  - New rows: `wrangler-env` ✅, `wrangler-bundling` ✅, `wrangler-api` 🚫.
  - The per-key details are in "Wrangler config: details".
  - The README table has matching rows; `compatcheck` passes.
- A race in the first version of `TestCLIDeployWranglerTOML` was fixed:
  it edited the fake API's policy without its lock. It passes under
  `-race`.

# wrangler CLI integration (issue #19)

Branch `wrangler-cli-integration-issue-19`. User docs: `docs/wrangler.md`
("Using wrangler with twd"). Compat rows: `docs/COMPAT.md` "Workers platform
config".

## What's built

- **Config formats** (`cmd/twd/wranglercfg.go`). `twd deploy` and `twd dev`
  read `wrangler.json`, `wrangler.jsonc` or `wrangler.toml`, using wrangler
  4's order (json, then jsonc, then toml; checked in wrangler 4.60's
  `findUpSync` chain). twd looks in the app directory only. A shadowed
  file gets a warning. TOML is parsed with `github.com/BurntSushi/toml`
  v1.6.0, already in the module graph, now a direct dependency. Integers
  become JSON numbers, and an unquoted TOML date
  (`compatibility_date = 2026-04-01`) becomes `"2026-04-01"`. wrangler
  itself rejects the unquoted form, so twd is more lenient here. The
  bundle always carries the resolved config as JSON `wrangler.jsonc`, so
  twd-hostd and mgmt are unchanged for config.
- **Environments** (`--env <name>` on `deploy` and `dev`). These follow
  wrangler 4's `normalizeAndValidateEnvironment`:
  - Non-inheritable keys (`vars`, `define`, `durable_objects`,
    `containers` and every binding section; the list was extracted from
    wrangler 4.60) come only from the env.
  - Everything else is inherited.
  - Top-level-only keys (`keep_vars`, `dev`, `site`, …) are dropped.
  - Like wrangler, twd warns once per non-inherited key the top level has
    and the env lacks.
  - The workload is named `<name>-<env>` unless the env sets `name`.
    `<name>` is `twd.jsonc` `name`, then wrangler `name`, then the
    directory. `--name` overrides.
  - An unknown env is `bad_request` and lists the envs that exist.
- **No `twd.jsonc`** (`defaultTwdConfig`). A wrangler-only project
  deploys unchanged. The workload node is tagged
  `tag:twd-wl-<workload>`, and every DO class is `"node": false`
  (internal, plain-DO mode: Cloudflare's semantics). A warning line says
  the defaults are in use. Preflight prints the `tagOwners` snippet for the
  default tag, the same as for any new tag.
- **Built-in esbuild honours wrangler build settings**
  (`internal/esbundle/rules.go`):
  - `define`, `alias`, `minify`, `keep_names`, `jsx_factory`,
    `jsx_fragment`, `tsconfig`.
  - `rules` of type Text, Data and CompiledWasm, with wrangler's default
    rules (`.txt`/`.html`/`.sql`, `.bin`, `.wasm`/`.wasm?module`) and its
    `fallthrough` semantics.
  - Matching imports become external modules named
    `<sha1(content)>-<basename>`, byte-identical to wrangler's names for
    the same files (checked against wrangler 4.60's outdir).
  - ESModule and CommonJS rules are refused, with `--wrangler` as the fix.
- **Wrangler as the bundler** (`cmd/twd/wranglerapp.go`):
  - `--wrangler` runs `wrangler deploy --dry-run --outdir <tmp> --config
    <file> --env=<env>` in the app dir, with `WRANGLER_SEND_METRICS=false`.
    It finds the CLI at `$TWD_WRANGLER`, then the project's
    `node_modules/.bin/wrangler` (searching parents), then PATH. Without
    one, the error is `wrangler_required`, documented in `docs/errors.md`.
  - `--from-wrangler-out <dir>` packs a prebuilt outdir. The entry is
    `main`'s base name with `.js`, or the single top-level non-hashed
    `.js`. `README.md` and `*.map` are left out. The other modules are
    typed by rules (the config's plus wrangler's defaults, matched on the
    name with the hash stripped), then by extension. `.cjs` is an error.
- **Typed modules on the host** (`hostd/bundle/moduletypes.go`). There is
  a new `twd-bundle.json` `"modules": {path: "js"|"json"|"text"|"data"|"wasm"}`.
  `bundle.Parse` ships listed files whatever their extension (before this,
  `.md` and `.sql` were dropped, and `.sql` would have been data, not
  text). `hostd/gen` asks `app.ModuleKind(p)` instead of its private
  `moduleKind` (three call sites; the extension table moved to
  `bundle.KindByExt`).
- **Warnings for ignored config**: one line per top-level key, printed
  before anything else. `deploy --json` also returns them in `warnings`.
  The categories are Cloudflare routing (`routes`, `workers_dev`, …), each
  binding section (naming the `env.X` names that will be undefined),
  `assets`/`site`, build keys only wrangler honours (silent under
  `--wrangler`), "ignored by twd" keys, a DO `script_name`, and unknown
  keys.
- `twd deploy`'s build line names the bundler
  (`(esbuild)` / `(wrangler)`).

## Tests (all hermetic)

| test | what |
|---|---|
| `internal/esbundle` `TestRuleGlobs`, `TestEffectiveRules`, `TestBuildAppWranglerSettings`, `TestBuildAppUnsupportedRule` | glob semantics, rule order, define/alias/rules output shape and names |
| `hostd/bundle` `TestModuleTypes`, `hostd/gen` `TestGenerateModuleTypes` | `twd-bundle.json` modules: shipping, validation, capnp + facet module table |
| `cmd/twd` `TestFindWranglerConfigPrecedence`, `TestReadWranglerTOML`, `TestApplyWranglerEnv`, `TestWranglerWarnings`, `TestLoadAppWranglerTOMLOnly`, `TestBuildWranglerTOMLApp` (esbuild, env, from-wrangler-out, a fake `wrangler` CLI checking the exact argv, a failing CLI), `TestReadWranglerOutEntry` | units |
| `cmd/twd` `TestCLIDeployWranglerTOML` | the acceptance test: `twd up` (single process) → `twd deploy` of `e2e/wrangler/testdata/toml-app` unchanged (no twd.jsonc / wrangler.jsonc). Preflight names the default tag; the 4 warning lines; top level and `--env staging` both serve over the tailnet (define/alias/md/txt/wasm/`node:buffer`, internal DO state 1 → 2); the project dir is unchanged |
| `e2e/wrangler` `TestTwdDevWrangler` (`make e2e-wrangler`) | twd binary → `twd dev --once` (hostd + workerd) with built-in esbuild, `--from-wrangler-out` (checked-in wrangler 4.60 outdir) and `--wrangler` (real CLI; skips without one). All three give identical responses |

Commands run on this Mac (arm64, Go 1.27.3, wrangler 4.60.0, Node 25.1):

- `make lint`: ok.
- `go test -count=1 -run 'Wrangler|TOML|ReadWranglerOut' ./cmd/twd`: ok.
- `go test -count=1 ./internal/esbundle ./hostd/bundle ./hostd/gen`: ok.
- `go test -count=1 -run TestCLIDeployWranglerTOML ./cmd/twd`: ok (8.4 s).
- `make e2e-wrangler`: ok, all 3 subtests ran, `WranglerCLI` included
  (7.1 s).
- `make lint test` on the merged tree: exit 0 (27 packages ok).
- `make e2e-wrangler` on the merged tree: 3/3 subtests pass.

## Spike findings

1. **Config formats.** This was cheap and high-value, as expected. It is
   done, including environments. The no-twd.jsonc default makes "existing
   project, unchanged" true. The one thing a user must still do is the
   `tagOwners` line that preflight prints.
2. **Wrangler as the bundler.** Both `--wrangler` and
   `--from-wrangler-out` are worth having, and both are built. Compared on
   the example project:
   - The outputs agree in every check, and the additional-module names are
     identical.
   - wrangler's entry is 21 KB against 1.0 KB built-in: it inlines unenv
     polyfills for `nodejs_compat` (`process`, `performance`, and the
     `__name` helper). Those polyfills run fine on stock workerd 1.20261108.1.
   - Only wrangler handles `build` (custom commands), `no_bundle`,
     `find_additional_modules`, `preserve_file_names` and `base_dir`, so
     the built-in path warns on those and points at `--wrangler`.
   - The built-in path stays the default: it needs no Node.js.
3. **`wrangler dev` / Miniflare.** It works as an inner loop for code that
   doesn't touch the tailnet. Measured on the `hello` template under
   `wrangler dev`: `GET /` is a 500 because `getCaller` throws
   `no_identity` ("request has no verified tailnet caller"). That is
   correct and deliberate: twd must never synthesise a caller.
   - Egress leaves from the developer's own network. With tailscaled
     running, names resolve as the developer, not the workload.
   - `tailnet.objects` is ignored: plain DOs, no per-object nodes.
   - There is no `ctx.tailnet`.
   - The recommendation is `twd dev` for anything tailnet-real. It starts
     in about 0.6 to 1 s on this Mac, from the `ready` timings of the
     `twd dev --once` runs above.
   - Nothing was built for this path. Docs only.
4. **A Cloudflare-API shim for `wrangler deploy` / `secret put` / `tail`:
   no.** wrangler does honour `CLOUDFLARE_API_BASE_URL`, so it is
   technically possible. But wrangler 4.60 calls about 52 distinct
   `/accounts/:id/workers/*` paths (counted in `wrangler-dist/cli.js`).
   Deploy chains multipart upload, versions, deployments, settings,
   schedules, subdomain and routes. `tail` expects a WebSocket URL from
   Cloudflare's tail service in Cloudflare's event format. wrangler
   releases fast (4.60 is installed here; npm said 4.162 was available),
   auth is bearer tokens where twd uses tailnet identity, and twd's deploy
   steps (preflight snippets, placement, image pinning) have no place in
   wrangler's flow. The maintenance cost is open-ended for little gain over
   `twd deploy` / `twd secret put` / `twd logs -f`. This is written up in
   `docs/wrangler.md`.
5. **Warnings for ignored config.** Built, as above.

## Decisions

- A wrangler-only project gets **internal** DO classes by default, not a
  node per instance. That matches Cloudflare. A per-object node needs tags
  the user chooses, so it is opt-in through `twd.jsonc` `tailnet.objects`.
- `twd.jsonc` has no environments. `--env` changes the workload name (and
  so the default tag), not `twd.jsonc`. No ADR was needed: this changes
  CLI defaults, not the identity model.

## Gaps / next steps

- `js/packages/create-twd/lib/build.mjs` (the npm bundler, also used by
  `hostdtest`) still reads only `wrangler.jsonc` and has no
  define/alias/rules. `twd deploy` doesn't use it; bring it level if the
  npm path matters. `TestMatchesBuildMJS` still passes because the
  templates use none of these settings.
- `minify`, `keep_names`, `jsx_*` and `tsconfig` are mapped to esbuild
  options but not measured end to end.
- `--from-wrangler-out` doesn't check that the outdir is up to date with
  the sources.
- `twd init` has no "adopt an existing wrangler project" helper (for
  example, to write a `twd.jsonc` with per-class tags). The defaults plus
  docs cover the unchanged case.
- The `UnsupportedBindings` check in `e2e/sdk/cfagents_test.go` is
  unchanged: this work adds no bindings.
