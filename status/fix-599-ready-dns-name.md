# fix-599-ready-dns-name

#599 (found by the e2e/dataplane containers loop, #423, scenario `pool`
seed 1): an object could go ready, and report ready to mgmt, with an empty
MagicDNS name (18 times in one 4-minute run).

Fix: `netd/dnsname.go` `statusWithName`, called from `(*Object).up` in
`netd/registry.go` instead of a single `StatusWithoutPeers`. It reads the
node's status until its own DNS name is there, for at most 10 s (a control
that sends no names still joins).

Regression test: `netd` `TestStatusWithName`.
