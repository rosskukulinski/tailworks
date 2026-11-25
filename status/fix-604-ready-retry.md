Last update: 2026-11-25 01:00 PT. A ready report lost to a mgmt outage is retried (#604).

# fix-604-ready-retry

## The bug

Found by the e2e/dataplane deploys loop (#421, `deploys-mixed` seed 1 on
Linux, SQLite mgmt, V6): a workload node joined 70 ms after the nemesis
SIGKILLed mgmt; its ready report failed with `connection reset by peer`
and was never sent again, so mgmt kept the object `minted` and the
deployment's endpoint never became ready. hostd retried a ready report
only after `lease_lost` (#210); `rereport` waits for egress, which a node
that only serves never makes.

## The fix

`retryableReady`: a report that got no answer or a 5xx is retried like
one refused with `lease_lost` (same backoff, 5 tries); `killed` and other
4xx stay final. Both report paths (`objects.go`, `poolnodes.go`) and
`retryReady`'s own loop use it.

## Tests

- `TestRetryReadyAfterMgmtOutage` (hostd): mgmt answers the first retry
  503, the second 200; fails before the fix (one report, given up).
- `TestRetryableReady`: the classification.
- `go test ./hostd/`: pass. `make lint`: pass. `make changes-check`: ok.
