---
kind: security
upgrade: []
---
Security fixes from review. `tw up` runs as a single process by default (mgmt with the host agent embedded; `--separate-host` keeps the old layout), and tw-mgmt serves an admin UI at `/ui/`.
