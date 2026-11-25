---
kind: security
upgrade: [feature-string]
---
Internet egress to **TCP port 25** is now refused by default
(`internet_port_refused`, audited as `deny`), on every path that names a
port: `connect()`, `node:net`, the Node tier's CONNECT, and a `fetch()` to
`http://host:25/`. Internet flows leave from the host's own address, so
mail relayed from a workload (a prompt-injected agent, say) would come from
you. Authenticated submission (465, 587) and every other port are
unchanged. Containers' `enableInternet` applies the same rule.

To allow a relay, name it with the port in `tailnet.egress.internet`:
`["smtp.example.com:25"]`. Any entry may now carry a `:port` (`"*:25"`,
`"203.0.113.0/24:25"`, `"[2001:db8::1]:25"`); an entry with a port covers
that port only, and an entry without one covers every port except 25. A
bad port in an entry fails the deploy.

What is deliberately not bounded: how many internet flows a workload opens
per unit of time (docs/limits.md "Limits that don't exist"; #339).

Operator action: a bundle whose `tailnet.egress.internet` names a port is
placed only on hosts that offer the `internet-ports` feature (an older host
would read the entry as a name that matches nothing); upgrade the hosts
first. A workload that relays mail on port 25 today needs the entry and a
redeploy, or it gets `internet_port_refused` once its host runs this
release.
