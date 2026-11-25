package hostd

import "time"

// Idle keep-alive bounds for hostd's clients to app servers (#309).
//
// A client must drop an idle keep-alive connection before the server does.
// In the moment between the server's idle close and net/http's read loop
// seeing the EOF, net/http can still hand the dead connection to a new
// request, which then fails with "write: broken pipe" or EOF (#287, #300).
//
// The workerd clients (inst.direct, the intercept client, the microVM
// ingress proxy) use netd.UpstreamIdleTimeout (10 s), below workerd's 15 s
// idle close (TestWorkerdKeepsIdleConnLongerThanUpstream in netd).

// nodeUpstreamIdleTimeout is how long the Node-tier transport (direct and
// ingress) keeps an idle connection to the app. Node's http.Server closes
// an idle keep-alive connection keepAliveTimeout (default 5 s) after the
// last response, plus keepAliveTimeoutBuffer (default 1 s) where Node has
// it. Measured: 5.0 s on Node 18.16 and 20.0, 6.0 s on 22.23, 25.1, 26.13.
// TestNodeKeepsIdleConnLongerThanUpstream checks the Node on PATH. An app
// that lowers keepAliveTimeout below this can still hit the race.
const nodeUpstreamIdleTimeout = 3 * time.Second
