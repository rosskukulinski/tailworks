# fix-572-fence-drain

Issue #572, found by the data-plane routing loop (#422).

## What changed

- `hostd/workload.go` `undeploy`: for a workload that fences (holds
  Durable Objects), the nodes are closed (netd resets their flows) before
  workerd is stopped, not after its graceful drain (SIGTERM, up to 3 s).
  Stateless workloads keep the drain.
- `hostd/fencedrain.go`: `closeNodes`, the node-closing loop moved out of
  `undeploy`.
- `hostd/fencedrain_test.go` + `hostd/testdata/apps/wsfence`:
  `TestFenceClosesOpenWebSockets`.

## Tests run

- `go test -run TestFenceClosesOpenWebSockets ./hostd/`: before the fix,
  FAIL (socket closed 3.0 s after the revoke, 122 late answers); after,
  PASS (closed at the revoke, 0 late answers).
- `go test ./hostd/`: ok (155 s).
- `make lint`, `make changes-check`: ok.
- Full `make test`: left to CI (the machine was at load 20–38 and both mbp
  slots were busy).
