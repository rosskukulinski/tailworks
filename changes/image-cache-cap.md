---
kind: fix
upgrade: [config-default]
---
The converted container image cache is now capped at 20 GiB by default
(`tw-hostd -container-image-cache-max-mib`, default 20480; `tw up` passes
the same value). It used to have no cap, so every redeploy of a changed
Dockerfile left another converted image on disk for good. Past the cap the
least recently used images that no running container uses are removed and
pulled again on their next start. The image a start has just converted is
held before the cache is trimmed, so it is never the one removed, even when
it alone is larger than the cap (#476).

Operator action: none. To keep the old behaviour, start `tw-hostd` with
`-container-image-cache-max-mib 0` (no cap).
