Last update: 2026-11-25 11:40 PT. hostd: the syncService data race (#586), the whois helper panic (#411), the alarm-record flake (#410).

# fix-hostd-flakes

## #586 data race: awaitNode vs syncService

`syncService` read `wl.Status` for its reason string ("workload node …")
after it released `h.mu`, while `awaitNode` writes it under `h.mu`. It now
copies the field while the lock is held. `wl.Hostname`, the only other
unlocked read there, is set once when the object is created.

## #411 serveWhoisTCP nil pointer (connect_test.go:42)

6eee8fcf already handled the nil `who.Node`. The later CI panic (job
139081103575, `addr=0x20`) was on the same line, at
`c.RemoteAddr().String()`. gVisor's `gonet.TCPConn.RemoteAddr` returns nil
once the endpoint is reset (`GetRemoteAddress` fails), and calling
`String` on a nil `net.Addr` reads offset 0x20 of a nil itab. The helper,
and its copy in `e2e/vm/tcp_test.go` (which also lacked the `who.Node`
check), now drop such a conn.

## #410 TestTelemetry/EntryTraces/*Alarm

Reproduced 1 time in 80 runs under load. The `__tw_alarm` record had its
trace but `Logs:[]`, and an `alarmSeen` RPC record from the same
millisecond carried "alarm Obj". The test polled the object (`alarmSeen`)
every 50 ms while the alarm ran, and workerd's tail put the alarm's
console.log on that concurrent invocation. hostd's join wasn't the
problem: the trace was right. The test now waits for the alarm's record
first, and only then reads what the alarm saw. That's 0 failures in
180 runs under the same load.

Note for users: in workerd, a Durable Object's console.log can show up on
a different invocation's record when requests to that object overlap.
This is workerd behaviour, not something tailworks adds.

## Found while verifying: hostd panics on "close of closed channel"

A stress run of `TestAuditChainAcrossRestart` (on #427's branch) crashed
the test binary:

```
panic: close of closed channel
hostd.closeOnce (objects.go:502)
hostd.(*Host).awaitNode (objects.go:461)
```

`closeOnce` checked whether the channel was closed and then closed it.
The kill path (it sets `statusKilled` and closes `readyCh` after it
releases `h.mu`) and `awaitNode` (it checked the status earlier and
closes the same channel after it releases `h.mu`) can both pass the check
at the same time. A kill that lands while a node is joining could
therefore crash hostd. `closeOnce` now does the check and the close under
its own mutex. `TestCloseOnceConcurrent` panics without the fix (4
goroutines, 2,000 rounds) and passes with it.

## Tests

- `make lint`: ok.
- `make test` (macOS, local): every hostd package passed. The one failure
  was netd's `TestKeepEarlyAnswerNoAnswer` (#429, unrelated: "POST 2 took
  12.03 s").
- `go test -race ./hostd/` after the last merge of origin/master: ok.
- `TestTelemetry`, 100 runs (10 at once, with `yes` hogs): 100 passed.
  Before the fix, the same load failed 1 in 80.
- `go test -race -count=3 -run TestCloseOnceConcurrent ./hostd/`: ok. It
  panics without the fix.
