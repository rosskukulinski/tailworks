---
kind: security
upgrade: []
---
`tw doctor` has a new `side_channels` check (Linux). It reads the kernel's
CPU vulnerability report (`/sys/devices/system/cpu/vulnerabilities/*`),
the SMT state, the kernel command line (`mitigations=off`, `nosmt`) and
KSM, and warns on a `Vulnerable` entry, SMT on, `mitigations=off` or KSM
on, saying when it matters: when the host serves more than one tenant,
runs a Node-tier workload or offers the microVM tier. It never fails the
run and nothing refuses a second tenant on it. On macOS it reports that the
posture can't be read and that a macOS host is development-grade for
tenants that don't trust each other. docs/security-model.md ("Side
channels"), docs/INSTALL.md (the microVM tier) and the threat model say
what tailworks relies on the host operator for (issue #334).
