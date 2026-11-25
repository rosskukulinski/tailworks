# ADR-024: microVM privilege model and persistent encrypted storage

**Date:** 2026-11-08
**Status:** Accepted (implemented, tailworks L3).
**Decision:** A one-time `sudo tw host setup --microvm` installs `tw-vmhelper`, a root systemd service with a fixed capability set that authorizes callers by uid, receives open fds rather than paths, and runs Firecracker through the **jailer** (chroot, own uid/gid, cgroup v2 CPU/memory, seccomp). After setup, tailworks runs as the user. DO storage lives on a per-(tenant, workload) host volume encrypted with **dm-crypt**, keyed from custody and released only to the lease holder at the current epoch; a volume can never attach to two VMs. Measured: boot→ready ~0.7 s, 160–190 MiB/VM, storage survives VMM SIGKILL, redeploy, clean reboot and power cut.
**Open:** snapshot restore; amd64 microVM assets; the 12–17 s client re-find delay after host reboot (under diagnosis).
