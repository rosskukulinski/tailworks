Last update: 2026-11-24 PT. hostd test helpers no longer panic when WhoIs returns no node.

## What and why

While verifying PR #330 under heavy load, a full `make test` panicked in `hostd`:
`serveWhoisTCP` (`hostd/connect_test.go`) dereferenced `who.Node` from a
`WhoIs` that returned no error and no node (the caller not yet in the
far node's netmap). The panic was in a bare goroutine, so it killed the whole
hostd test binary.

- `hostd/whois_helper_test.go` (new): `whoisNode` asks again every 100 ms until
  the node is there, for up to 10 s.
- `serveWhoisTCP` uses it. A still-missing node is reported to the client as
  `whois failed: …` instead of a panic.
- `startCollector` (`hostd/telemetry_test.go`) runs in a net/http handler, which
  recovers panics. It now skips a nil node and doesn't retry, because a caller
  that isn't a tailnet peer must not stall the handler.

## Tests

- `go test -race -count=3 -run 'TestTCPEgress|TestTelemetry' ./hostd`: ok
  (TestTCPEgress, TestTCPEgressInternet and TestTelemetry ran; TestTCPEgressPostgres skips).
- `make lint test` (macOS, after merging origin/master): ok.
- Not reproduced on demand: the nil node only showed up at load average ~95.
