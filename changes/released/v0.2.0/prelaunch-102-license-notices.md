---
kind: fix
upgrade: []
---
Release tarballs now include the project's `LICENSE` and a `THIRD_PARTY_NOTICES` file. The notices cover workerd, the Go modules compiled into the binaries, the vendored JavaScript packages and, where a tarball includes the microVM guest, Firecracker, the Linux kernel and the guest image's Ubuntu packages. Tarballs with a microVM guest also include `share/tailworks/vm/KERNEL-SOURCE.md` and `ROOTFS-SOURCE.md`, which say where to get the corresponding source and include a written offer for it. The guest image now carries the Debian copyright files of the Ubuntu packages it takes files from (in `/usr/share/doc`), and its `manifest.json` lists those packages (#102).
