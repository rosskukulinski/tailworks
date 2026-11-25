---
kind: security
upgrade: []
---
A newly created object's first outbound calls now carry its caps limit
(#331). A node can send as soon as it has joined the tailnet, a moment
before mgmt has its ready report and has issued its node certificate. A
receiver that looked the new object up in that window found no
certificate and then remembered that answer for 60 s. For that minute it
treated the object as a plain tailnet node: a sub-agent spawned with
narrower caps than its tag's grants could use the difference, and
`getCaller().tw` was missing, so calls that need it (a service binding's
named entrypoint or RPC method) were refused.

Now hostd holds a new object's outbound calls (`fetch()`, `connect()`, a
Node app's proxy, service bindings) until mgmt has accepted its ready
report, so the receiver finds the certificate on the first call. This adds
up to one mgmt round trip to an object's first call. If the report has
not completed after 20 s, or it failed, the call fails with
`identity_pending` and is not sent without the limit. A failed report is
sent again on a later call. Receivers also keep a "not a tailworks object"
answer for 2 s instead of 60 s.
