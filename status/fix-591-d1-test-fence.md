# hostd tests: FenceAfter 5 s where ordinary traffic runs (#591, #595)

Since #556 (stall fence, #501) a stateful workload refuses ingress and
egress once its last successful heartbeat is older than `FenceAfter`.
`testD1`, `TestPlatformStore` and `TestR2IndexSurvivesMove` ran with
`FenceAfter` 1 s, so a heartbeat more than 1 s late on a loaded CI runner
failed ordinary queries with 503 `upstream_unavailable`. They now use 5 s;
their fence subtests wait up to 20 s for the fence. `containers_fence_test`
keeps 1 s: it measures the fence latency and sends no ordinary traffic.

## Tests
- `go test ./hostd -run 'TestD1$|TestRealMgmtStores|TestPlatformStore|TestR2IndexSurvivesMove'`: pass.
- `make lint`: pass.
