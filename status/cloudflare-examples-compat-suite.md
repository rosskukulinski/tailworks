Last update: 2026-11-23 20:45 PT. `make e2e-compat`: 30 Cloudflare example projects on tailnet 1, 27 PASS + 3 expected gaps.

# Cloudflare examples compatibility suite

## What's built

- `e2e/compat/`: a real-tailnet suite (`make e2e-compat`, build tag
  `e2ecompat`) that deploys 30 example projects from Cloudflare's own
  repositories, unmodified and pinned by commit, with `tw deploy` to a
  separate `tw-hostd` host (`tw up --oauth-client-file --separate-host`,
  production control, tailnet 1). A `tag:ttest` tsnet client then calls each
  one over the tailnet as its README does. See `e2e/compat/README.md` for
  the list, what "unmodified" means, the workarounds, and how to add a
  project.
  - Sources: cloudflare/templates (16), cloudflare/workers-sdk C3
    templates (11), cloudflare/agents (2), cloudflare/ai (1), fetched at run
    time (blobless partial clone, `~/.cache/tailworks-compat`). Nothing is
    vendored.
  - Probes cover pages, JSON APIs, form posts, cookies and redirects, D1
    bookmarks, WebSockets (broadcast plus SQLite history after a reconnect),
    MCP Streamable HTTP sessions (stateless and McpAgent state), Workflows
    (start → waiting → event → complete), and cron and queue-consumer output
    via `tw logs --console`.
  - Known gaps are entries with an `Expect`. Python (`c3-python`) must be
    refused by name. Service bindings (`microfrontend`) and Workers AI
    (`text-to-image`) deploy and must fail. An unexpected pass (XPASS) fails
    the run.
  - `Validate` checks the list: unique valid workload names, full commit
    SHAs, `Exercises` anchors that exist in COMPAT.md, and at least 20
    projects expected to pass.
  - Policy: the run may add only `tag:tw-wl-compat` (tagOwner) and the
    `tag:ttest → tag:tw-wl-compat tcp:80` grant. Anything else missing means
    it refuses to write. Every project gets a `tailworks.jsonc` with only
    its name and that tag.
- `make e2e-compat-prepare`: fetch, install and build only (no tailnet).

## Findings (and what this branch did about them)

1. **Fixed: `tw deploy --wrangler` didn't put `node_modules/.bin` on
   PATH.** `durable-chat-template`'s `build.command` (`esbuild …`) failed
   with `command not found`, though `npm run deploy` works.
   `wranglerEnv` in `cmd/tw/wranglerapp.go` now does what npm does.
   Test: `cmd/tw:TestWranglerEnvPutsBinOnPath`.
2. **Fixed (the interim half of [#292](https://github.com/rosskukulinski/tailworks/issues/292)): a Python Worker failed with esbuild's "No loader is configured
   for .py files".** It's now `not_supported: Python Workers are not
   supported …` (`cmd/tw/python.go`). Test:
   `cmd/tw:TestPythonWorkerRefused`; `docs/errors.md#not_supported`.
3. **Open ([#301](https://github.com/rosskukulinski/tailworks/issues/301)): Vite plugin output.** `react-router-ssr`, `workflows-starter`
   and `mcp-agent` need `tw deploy <dist/<worker>> --wrangler`. `tw deploy`
   doesn't follow `.wrangler/deploy/config.json`, and the built-in bundler
   refuses the generated config (`no_bundle`, ESModule `rules`). Following
   the redirect, and accepting `no_bundle` ES-module output in the built-in
   path, would make all three plain `tw deploy` projects.
4. **Open ([#302](https://github.com/rosskukulinski/tailworks/issues/302)): `request.cf` has no geo.** `multiplayer-globe` connects, but
   `request.cf.latitude/longitude` are unset, so no marker is sent (the
   server logs a warning). The probe records this and doesn't fail.
5. **Open ([#303](https://github.com/rosskukulinski/tailworks/issues/303), minor): `x-tw-object` reaches app code.** `c3-queues` logs the
   producer request's headers, and they include `x-tw-object` (an internal
   header) next to `traceparent`.
6. Upstream quirk: `to-do-list-kv-template`'s lockfile fails `npm ci` on
   npm 11 (wrangler 4 vs `@remix-run/dev`'s optional wrangler 3 peer), so
   the suite uses `--legacy-peer-deps`.

## Test results

- `make e2e-compat` (2026-11-23, tailnet 1, darwin-arm64):
  `--- PASS: TestE2ECompat (106.48s)`, counts `PASS:27 XFAIL:3`,
  cleanup `devices_left: 0`, no errors. Results:
  `e2e/real/results/compat-20261123-203901-a605de.json`
  (gitignored). Every deploy took 1.3–2.7 s. Preparing all 30 from a
  cold cache took about 60 s; warm it takes 7 s.
- Earlier runs on the same day found findings 1–2 and five probe/harness
  bugs: `tw d1 migrations apply` reads `./wrangler.jsonc`, so setup runs in
  the app dir; openauth's state cookie; SPA navigation needs
  `sec-fetch-mode: navigate`; parsing tw's JSON error; the npm peer
  conflict.
- `make lint`: ok.
- `make test`: every package ok (`cmd/tw` 285 s) except
  `internal/tsnetlc` `TestCloseDrainsLocalAPIConns`, which this branch doesn't touch ([#304](https://github.com/rosskukulinski/tailworks/issues/304)).
  It's a timing flake under the full parallel run: "ts.Close (holds) took
  24ms, want 4s–1m0s". Rerun alone, `go test -race -count=1 -run
  TestCloseDrainsLocalAPIConns ./internal/tsnetlc/` passed 3 of 3. `make test-js`:
  ok.
- `make e2e-wrangler` (bundling touched): ok.
- `make changes-check`: ok (no upgrade-relevant paths).

## Not run

- The suite runs projects in the process tier only. `containers-template`
  is not included (it needs Docker or a microVM host).
