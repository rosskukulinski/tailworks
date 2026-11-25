package netd

import "time"

// UpstreamIdleTimeout is how long UnixUpstream keeps an idle keep-alive
// connection to workerd's ingress socket. It must stay below workerd's own
// idle close (15 s after the last response, measured on workerd
// 2026-11-18; TestWorkerdKeepsIdleConnLongerThanUpstream checks it), so
// that net/http never picks a connection workerd is closing (#287).
//
// When the two race, net/http can hand a request a pooled connection in
// the moment between workerd's close and its own read loop noticing it.
// The write then fails (EPIPE) or the response read gets EOF, and net/http
// can't resend a proxied POST, because its body is not replayable. The
// caller gets 503 upstream_unavailable "write: broken pipe" or "EOF". With
// the old 60 s, 25 of 1200 POSTs sent on connections idle 14.98-15.02 s
// failed that way. With a timeout below workerd's, net/http closes the
// connection itself first.
const UpstreamIdleTimeout = 10 * time.Second
