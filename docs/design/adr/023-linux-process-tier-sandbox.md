# ADR-023: Linux process-tier sandbox: Landlock + seccomp, no unix sockets for workloads

**Date:** 2026-11-08
**Status:** Accepted (implemented, tailworks L2).
**Context:** Ubuntu 24.04 blocks unprivileged user namespaces (`apparmor_restrict_unprivileged_userns=1`), so bubblewrap-style sandboxes need root. Landlock (ABI 4 on kernel 6.8) works unprivileged. The red team showed that without a sandbox, any tenant workload could reach mgmt's admin socket and every tenant's keys (RT-03).
**Decision:** Each tenant's workerd and Node app runs under Landlock (fs: read-only runtime, read-write only its own dirs; TCP pinned to its own loopback ports) plus seccomp (no-new-privs; deny ptrace, mounts, bpf, namespaces, io_uring; **deny `socket(AF_UNIX)`**, with STREAM/SEQPACKET `socketpair` allowed). workerd's listener is an inherited fd; its outbound channels are loopback TCP with per-instance tokens. On by default on Linux; without Landlock, multi-tenant is refused unless `--insecure-shared-host`.
**Consequences:** `tailscaled.sock` (0666 by design) is unreachable from workloads, so it is exempted from the host-socket refusal while seccomp is confirmed enforcing; container sockets and group memberships still refuse. Gaps: unix-listen Node apps keep AF_UNIX; ABI < 6 can't block pid signalling, so seccomp restricts `kill` to self/own process group.
