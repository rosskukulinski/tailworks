Last update: 2026-11-23 21:30 PT. Deflaked `internal/tsnetlc` TestCloseDrainsLocalAPIConns (#304).

# deflake-304-tsnetlc

## Root cause

The test built its stray LocalAPI connection with timers: a request with a
50 ms deadline, a 200 ms sleep in every LocalAPI dial, then a 400 ms sleep.
The issue guessed the 400 ms sleep was too short. The real race is earlier.
`http.Transport` dials in a goroutine that first calls
`wantConn.getCtxForDial`. If the request has already been canceled by then,
it gets nil and skips the dial (Go 1.27 `net/http/transport.go`
`dialConnFor`). Under load that goroutine sometimes started after the 50 ms
deadline. Then no connection existed, and plain `ts.Close` returned in
~20 ms.

To confirm it, I swapped the final sleep for a wait on "dial succeeded".
Under load, 17 of 20 iterations then timed out with no dial at all.

## Fix

- `tsnetlc.go`: the `dialDelay` test seam is replaced by `dialHook`, an
  `atomic.Pointer` to a function that wraps each LocalAPI dial. When it is
  nil, the dial path is unchanged.
- The test's `stray` now runs on signals. It holds the first dial in the
  hook, cancels the request once that dial has started, and waits for the
  request to fail. Then it releases the dial and waits for it to succeed.
  A successful dial is server-side confirmation: memnet's `Dial` returns
  only after the LocalAPI server's `Accept` has taken the connection, and
  `http.Server.Serve` tracks an accepted connection before `Shutdown` can
  return. If the transport pools the connection only after
  `CloseIdleConnections`, it closes it (`closeIdle`), so the tsnetlc.Close
  case stays deterministic too.
- The bounds are unchanged (4 s–1 m for the hold, ≤ 2.5 s for tsnetlc.Close).

## Tests

- Before, under load (72 `yes` burners, `nice -n 20`, `GOMAXPROCS=1`, 10
  binaries in parallel, `-test.count 2`): **6/20 passed**. The 14 failures
  were all `ts.Close (holds) took 14–24ms`.
- After, same load: **20/20 passed**.
- After, same load with `-test.cpu 1,2` (10 binaries × 2 counts × 2 cpu values):
  **40/40 passed**.
- `go test -race -count=20 -run TestCloseDrainsLocalAPIConns ./internal/tsnetlc/`
  (no extra load): **20/20 passed** (hold 5.001–5.002 s).
- `make lint`: ok. `make changes-check`: ok (no fragment needed).
- `make test` on mbp (`mbp-test.sh test`): ok, before and after merging
  `origin/master`.
