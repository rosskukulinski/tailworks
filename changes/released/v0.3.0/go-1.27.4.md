---
kind: security
upgrade: []
---
Binaries are now built with Go 1.27.4, which fixes GO-2026-6859 (a header
race in `net/http`'s `MaxBytesReader`). Building from source needs Go 1.27.4
or later.
