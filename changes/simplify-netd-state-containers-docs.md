---
kind: breaking
upgrade: []
---
Release tarballs (and the npm platform packages made from them) no longer
ship `bin/tw-netd`. Nothing ran it: tw-hostd links netd as a library and
runs one registry per tenant in its own process, and `tw doctor` checks only
tw-mgmt and tw-hostd. The tarball is about 10 MB smaller compressed.
`cmd/tw-netd` stays in the source tree as the netd test harness for
`make e2e-local`.

`tw up` no longer passes `<state>/tenants` in hostd's sandbox deny list:
nothing writes that directory (tenant state is in `<state>/hostd/tenants`,
which stays denied). docs/INSTALL.md has one table of what is in
`~/.tailworks`, which entries `tw backup` holds and what uninstall removes.

Operator action: none, unless a script of yours runs or copies
`bin/tw-netd` from a release. An upgraded install keeps the old
`bin/tw-netd` only in `bin.prev/`.
