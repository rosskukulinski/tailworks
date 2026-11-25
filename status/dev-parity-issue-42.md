Last update: 2026-11-15 13:19 PT. Issue #42 (`twd dev` parity with `wrangler dev`): source-mapped stacks, `req.url`, `--inspector-port` and hotkeys are built and tested. Stacked on PR #49.

# twd dev parity with wrangler dev (branch `dev-parity-issue-42`)

Branched from `benchmark-dx-infra-vs-cloudflare-workerd-vercel` (PR #49).
#49 adds the request and console records to `twd dev`, which is where the
exception stacks appear. The gaps come from #49's benchmark table; see
`status/benchmark-dx-infra-vs-cloudflare-workerd-vercel.md`.

## What's built

| Gap (#42) | Before | Now | Where |
|---|---|---|---|
| Exception stack | `dist/index.js:129:54` and 4 `__twd_rt.js` frames | `src/boom.ts:3:9`, `src/index.ts:8:7`, then `… 4 twd runtime frames (__twd_rt.js)` | `internal/srcmap` (v3 map reader), `hostd/stackmap.go`, `internal/obs/stack.go`, `cmd/twd/sourcemap.go` |
| `req.url` | `http://100.64.0.2/boom` | `http://127.0.0.1:8787/boom`, or `localhost:8787` if you typed that | `cmd/twd/dev.go`: the proxy keeps `Host` (one line) |
| Debugger | none | `twd dev --inspector-port 9229`: Chrome DevTools and VS Code, and it follows reloads | `hostd/inspector.go`, `cmd/twd/devinspector.go` |
| Hotkeys | none | `b` opens a browser, `x`/`q` stops; only with a foreground TTY and without `--json` | `cmd/twd/devhotkeys.go` |

How it works:

- **Source maps.** The built-in esbuild writes `dist/index.js.map`
  (`sourcemap: external`, no `sourcesContent`). Its sources are rewritten
  relative to the app dir, and the map ships in the bundle.
  `bundle.App.SourceMaps` keeps `*.js.map`, which workerd never loads.
  twd-hostd maps each exception stack in `telemetry.tail` before the record
  is written, so `twd dev`, `twd logs` (local and remote), `--json` and OTLP
  all get `src/…` positions from one code path. The parser does one pass
  that stores a checkpoint per generated line, and `Lookup` decodes only
  the line it needs. Maps are capped at 32 MiB, and malformed maps are
  errors, never panics: the map is tenant input parsed in hostd. Each map is
  parsed once per instance, and a failure is cached as nil.
- **`upload_source_maps`** is now honoured instead of warned: `false`
  leaves the map out of `twd deploy`, and `twd dev` always maps. With
  `--wrangler` it is still warned, because wrangler-built bundles ship no
  map yet.
- **Folding** happens in `Record.Text()` only. The JSON record keeps the
  whole stack, so agents and OTLP see everything.
- **`req.url`.** `pr.Out.Host = pr.In.Host` instead of the node IP.
  Identity and security check: netd's ingress takes the caller from
  `WhoIs(RemoteAddr)` on the tailnet connection (here, the dev client node
  `dev-you`) and strips the identity headers. Nothing in netd, hostd or the
  platform worker reads `Host` for identity or routing. `nodeConnect` uses
  `r.Host` for CONNECT egress, which isn't on this path. On a deployed host
  the tailnet client already chooses `Host` freely, so dev trusts nothing
  new. `TestDevParity` checks that `getCaller` is still the dev user with
  the new `Host`.
- **Inspector.** A reload is blue/green (the new workerd starts before the
  old one drains), so two workerds can't share one port. With
  `Config.WorkerdInspector` (which takes effect only with
  `ForceProcessTier`, that is, twd dev), each workerd gets
  `--inspector-addr=127.0.0.1:<free>`, and the port is added to the
  Landlock bind list. A restart keeps its port. twd dev serves the fixed
  `--inspector-port` as a reverse proxy to `Host.InspectorAddr` of the
  active instance, rewrites the `/json*` answers to point at itself, and
  passes the WebSocket upgrade through. With `--inspector-port`, the bundle
  also carries its map inline (a data: URL with `sourcesContent`), so
  DevTools shows `src/…` without fetching anything. The spawn change is one
  line in `hostd/seatbelt.go` (`args, bind := inst.inspectorArgs(args)`),
  kept small because #44 also touches that spawn.
- **Hotkeys.** `stty -icanon -echo min 1` (ISIG and OPOST stay, so Ctrl-C
  and newlines still work), restored on exit. The foreground check uses
  `TIOCGPGRP` against `getpgrp`, so a background job never gets SIGTTOU.
- `--json` gets a new `inspector` event (`addr`, `ws`) in
  `schema/cli.schema.json` and `docs/cli-json.md`.

Docs: `docs/dev-debugging.md` (new; covers Chrome DevTools, the VS Code
`launch.json`, stacks, `req.url` and hotkeys); rows in `docs/COMPAT.md`
(wrangler section and the logs row), `docs/wrangler.md` and `llms.txt`.

## Test results

Unit tests:

- `go test ./internal/srcmap/`: ok. `TestLookupEsbuild` maps a real esbuild
  bundle and a V8 stack across two TS files. Also `TestLookupHandMade`
  (VLQ, state carried across lines, unmapped segments), `TestParseRejects`
  (12 malformed or oversized maps), `TestMapStackLeavesUnknown` and
  `TestModuleName`.
- `go test ./internal/obs/ -run Fold\|TextFolds`: ok. Covers runs, a
  single frame, frames that only mention `__twd_` in an app function name
  or a message, and checks that Text folds while the JSON stack stays
  whole.
- `go test ./hostd/ -run TestInstanceMapStack` and
  `go test ./hostd/bundle/ -run TestSourceMaps`: ok.
- `go test ./cmd/twd/ -run 'TestRewriteMapSources|TestWantSourceMap|TestDevHotkeys|TestDevInspectorProxy'`: ok.

End to end (real workerd, testcontrol):

- `go test ./cmd/twd/ -run 'TestDevParity|TestDevJSONEvents|TestWranglerWarnings'`:
  ok. `TestDevParity` runs `twd dev --json --inspector-port` on the hello
  template with a throwing helper and checks four things. The stack has
  `src/boom.ts:3:` and `src/index.ts:7:` and no `dist/index.js`. `req.url`
  is `http://127.0.0.1:<port>/hello` and `getCaller` is still the dev user.
  The inspector `/json/list` lists `ws://127.0.0.1:<port>/tenant`. Every
  event matches `$defs/dev.event`.

By hand on macOS arm64 (`bin/twd dev --inspector-port 9342`, hello
template with `src/boom.ts`):

```text
2026-11-15T21:12:35.176Z GET /boom 500 1.9ms exception  caller=user-1@fake-control.example.net  trace=c4e7…
    error Error: boom at /boom
          at explode (src/boom.ts:3:9)
          at Object.fetch (src/index.ts:8:7)
          … 4 twd runtime frames (__twd_rt.js)
```

- `curl 127.0.0.1:8842/` returned
  `{"url":"http://127.0.0.1:8842/","abs":"http://127.0.0.1:8842/cb","hello":"user-1@…"}`,
  and `curl localhost:8842/x` returned `"url":"http://localhost:8842/x"`.
- After editing `boom.ts`, the reload took 47 ms and the stack showed the
  new line (`src/boom.ts:4:9`). The inspector followed the new workerd.
- A CDP client (Node's WebSocket) attached to `ws://127.0.0.1:9342/tenant`
  through the proxy. `Debugger.scriptParsed` listed `dist/index.js` with an
  inline source map holding `src/boom.ts` and `src/index.ts` and 4
  `sourcesContent` entries. `Runtime.evaluate` outside a request returns
  workerd's "Cannot find default execution context", as expected: there's
  no context until a request runs.
- Hotkeys were tested on a pty (Python `pty.fork`). While running, `ICANON`
  and `ECHO` were off and `ISIG` was on. `b` ran `open http://127.0.0.1:8844`
  (a stub `open` on PATH). `q` printed "stopping" and exited 0. After exit
  the terminal had `ICANON` and `ECHO` back.

`make lint`: ok (api-check, compat-check, gofmt, vet, check-secrets).
`make test` (macOS arm64, `-race`): every Go package passed except two
tests, which failed under the full parallel run and passed when rerun alone:

- `hostd:TestQueues/FencedHostConsumesNothing`: no delivery within 30 s.
- `hostd/gen:TestGeneratedWorkflows/AppCannotApprove`: the instance was
  still `running`.

Neither touches code this branch changed. Rerun alone, both pass:
`go test -race -count=1 -run TestQueues$ ./hostd/` and
`go test -race -count=1 -run TestGeneratedWorkflows ./hostd/gen/`.
`make test-js`: 40/40 passed.

## Gaps and next steps

- No `d` hotkey (open DevTools) and no `c` (clear). The issue asked only
  for `b` and `x`/`q`. Chrome can't be opened on a `devtools://` URL from
  the command line, so `d` would need a hosted DevTools frontend, as
  wrangler uses.
- `--wrangler` / `--from-wrangler-out` bundles ship no map
  (`readWranglerOut` drops `*.map`). Picking up wrangler's
  `index.js.map` when `upload_source_maps` is true would be a small
  follow-up.
- The runtime-node tier is untouched. Node has `--enable-source-maps` and
  its own stack format.
- Stacks are mapped when the record is written. Records written before
  this change, or by a host running an older twd-hostd, keep bundle
  positions.
- Source paths for twd's own packages are shown from their last
  `node_modules/` on. In a repo checkout (workspace symlink) they are
  absolute paths.
