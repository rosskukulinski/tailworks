---
kind: security
upgrade: []
---
`tw` no longer prints tenant- or internet-controlled text raw. Before, a
public request path, an object name, a tenant's audit note or a line of
console output could carry terminal escape sequences, CR/LF or bidi
overrides. These could recolour or clear the operator's terminal, or forge
rows in `tw audit`. Human-readable output (`tw ls`, `tw audit`, `tw logs`,
`tw kill`, `tw deployments`, `tw previews`, `tw approvals`, the KV, R2,
queue and D1 listings, `tw status`, `tw host ls` and error messages) now
shows control characters escaped, as `\x1b`, `\r`, `\n`, `\u202e` and so
on. `--json` output decodes to the same values as before. DEL, C1
controls and bidi overrides, which encoding/json leaves raw, are now sent
as `\u` escapes.
