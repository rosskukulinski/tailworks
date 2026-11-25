---
kind: security
upgrade: [protocol]
---
Public ingress refuses paths with control characters (`%00`–`%1F`, `%7F`)
with `400 bad_request`, counted in the `public_rejected` summary like other
bad paths, so NUL, ESC and CR/LF never reach your app. New object names
containing a C0 control, DEL, a C1 control or a bidi control are refused
with the new error code `400 bad_object_name` (hostd ensure/admit and mgmt
`POST /v1/objects`). Objects that already have such a name are not renamed
or deleted. `docs/public-ingress.md` now says that apps receive the path
decoded once and should not decode it again.

Operator action: none. In a mixed-version fleet, an upgraded hostd or an
upgraded mgmt is enough to refuse such names.
