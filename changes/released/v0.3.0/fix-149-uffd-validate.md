---
kind: security
upgrade: []
---
Linux microVM hosts: `tw-vmhelper` now validates the guest memory region table that Firecracker sends when a VM is restored from a snapshot (page size, alignment, sizes, overflow, bounds against the snapshot memory file, overlap). A malformed table, which only a tenant that had already escaped its guest into Firecracker could send, used to crash the root helper and drop every VM on the host; now it fails that one VM's start. The helper also recovers from a panic in any one connection or VM session instead of exiting.
