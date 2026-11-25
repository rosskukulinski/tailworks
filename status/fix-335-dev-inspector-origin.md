Last update: 2026-11-24 10:30 PT. #335 fixed for the inspector proxy; workerd's direct port documented as the residual.

# fix-335-dev-inspector-origin

Issue [#335](https://github.com/rosskukulinski/tailworks/issues/335),
ledger row `H-dev-inspector`.

## What changed

- `cmd/tw/devinspectorguard.go` (new): the `tw dev --inspector-port` proxy
  checks `Host` (only `127.0.0.1`, `localhost`, `[::1]` with its port, else
  421), `Origin` when present (a debugger scheme or its own loopback
  origin, else 403) and the path (only `/json…` and `/tenant`, else 403).
  `/json` and `/json/list` keep only the `tenant` target; its
  `devtoolsFrontendUrl` now names `/tenant` (workerd's ends in `ws=<addr>/`
  with no target). `cmd/tw/devinspector.go` wraps its handler with the
  guard (two-line change).
- `platform`, `deny` (and the workflows and assets workers) are no longer
  debug targets. Nothing in the repo debugged them; `platform` holds
  `MAC_KEY`.
- docs/dev-debugging.md: "Who may connect" and "What remains: workerd's own
  inspector port".
- security/LEDGER.md: `H-dev-inspector` row and run-log line.

## Findings

- Measured with the pinned workerd (macOS arm64): `/json` answers any
  `Host` and echoes it into `webSocketDebuggerUrl`; an upgrade with a
  foreign `Origin` gets 101; targets are the config's service names.
- `--inspector-addr=unix:<path>` binds the socket, but `/json/list` is `[]`
  and `/tenant` is 404, so workerd's inspector can't be moved off loopback
  TCP. The direct port (a new free port per reload) stays reachable by a
  page that scans loopback ports; that's documented. Closing it needs a
  Host/Origin check upstream in workerd.

## Tests

- `go test ./cmd/tw -run 'TestRT_HDevInspector|TestDevInspectorProxy' -count=1`: pass.
  With master's `devinspector.go` the new test fails (29 errors: 101 for
  foreign origins and `/platform`/`/deny`, 200 for foreign Hosts, all
  targets listed).
- `go test ./cmd/tw -run 'TestDevParity$' -count=1` (real workerd, after
  `make deps`): pass; `/json/list` has `/tenant`, no `/platform`.
- `make lint`: pass (local).
- `make test`: pass (on the mbp runner via `mbp-test.sh test`; no FAIL lines).
- Not run: the extra suites in AGENTS.md's table don't cover `tw dev`'s
  inspector; `e2e-compat` and friends don't touch this proxy.
