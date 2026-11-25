---
kind: feature
upgrade: [workerd]
---
Python Workers run on the microVM tier. The guest image now carries the
pinned Pyodide bundles (about 53 MB more per image, in
`/usr/share/tailworks/pyodide`), so a Python Worker or Durable Object with
`"tier": "microvm"`, or a tenant whose default tier is microVM, runs in a
microVM instead of being refused with `tier_unavailable`. The pins moved to
`internal/pyodide/bundles.txt`, which hostd and the guest image build both
read. See docs/python.md.

Operator action: microVM hosts need this release's guest image
(`rootfs.ext4`), as with any release. An older image has no bundles, and a
Python deployment on it fails at start.
