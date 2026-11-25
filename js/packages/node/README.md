# @tailworks/node

The SDK for [tailworks](https://github.com/rosskukulinski/tailworks)' Node.js
process tier (`"runtime": "node"` apps).

- `getCaller(req)` / `requireCaller(req)`: the verified tailnet caller that
  tailworks passes in the signed `x-tw-caller` header.
- `capValues(caller, cap)`: the caller's app capability values.
- `fetch`, `dispatcher()`, `installGlobalDispatcher()`: egress through the
  workload's tailnet node, attributed to the workload.
- `enforceEgress()`: refuse egress that bypasses the tailnet proxy.

`tw init my-svc --template node-workflow` creates a working app. Docs:
[node-tier.md](https://github.com/rosskukulinski/tailworks/blob/master/docs/node-tier.md).
