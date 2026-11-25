# docs/limits.md citations pinned by a test (issue #119)

Last update: 2026-11-17 PT. Citations are symbols, flags or anchors; `TestLimitsDocCitations` checks them under `make test`.

## What changed

- `docs/limits.md` cites `` `path:target` `` instead of `path:line`. A
  target is a Go/JS symbol (`MaxReplicas`, `D1_LIMITS.params`,
  `Defaults.RPS`), a flag (`-vm-mem-mib`) or an anchor (`#vm-boot`, a
  `// limits.md#vm-boot` comment in the code). The format is described at
  the top of the page.
- `docs/limits_test.go` (`TestLimitsDocCitations`) resolves every citation:
  Go through `go/ast` (const/var decls first, else a unique assignment,
  composite-literal field or `def(&x.Name, v)` call; `Name.Key` descends a
  literal; `-flag` matches `x.Kind("flag", default, …)`), JS/TS by line
  regex (an `OBJ.key` lookup is scoped to the object literal), anchors by
  comment. It evaluates the cited constant expression (numbers, `<<`,
  `time.*`, conversions, same-file constants) and requires the value to be
  one of the quantities in that table row, in any common unit. It fails on
  a missing or ambiguous target and on any `path:123` citation.
- 19 one-line `// limits.md#slug` anchor comments in code where the limit is
  an inline literal (hostd/workload.go, hostd/http.go, hostd/objects.go,
  hostd/node.go, netd/registry.go, netd/ingress.go, netd/public_serve.go,
  mgmt/server.go, mgmt/service.go, cmd/tw/ops.go, cmd/tw/wranglercfg.go,
  hostd/gen/js/store-r2.js, js/packages/tailnet/src/workflows.ts). No
  behaviour change.
- Fixed the drifted citations from the issue (all of mgmt/types.go and
  internal/queuecfg, hostd/workload.go microVM boot, mgmt/service.go spawn
  lowering, hostd/image/limits.go, the public-ingress defaults/ceilings, and
  others). The redeploy wait row now says 15 s is the 10 s drain plus 5 s.

Result: 169 citations, 160 with a value checked; the other 9 (for example
`secretNameRE`, `InstanceTypes.lite`, anchors on lines with no number)
are checked for existence only.

Mutation check: changing DefaultMaxObjects, kvwire MaxValueBytes, D1
params, `-vm-mem-mib`, telemetry RecordRate and JoinWait, the public
MaxBodyBytes ceiling and DefaultMaxImageBytes made the test fail on each
row; reverted.

## Decisions

- No optional line numbers (the issue allowed them for humans): they are
  the part that drifts, and a symbol or `#slug` is greppable. The test
  rejects `path:123`.

## Tests

- `go test ./docs/ -run TestLimitsDocCitations -v`: pass (169 / 160).
- `make lint`: pass.
- `make changes-check`: ok (no upgrade-relevant paths changed).
- `make test`: first run failed only in TestWorkersKVMove on the known
  mgmtapi data race (#135); after merging origin/master the rerun passed
  (exit 0).
