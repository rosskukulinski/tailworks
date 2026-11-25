---
kind: fix
upgrade: [protocol]
---
`GET /v1/objects/{id}/certificate` is deprecated. It returns the same node
certificate as `GET /v1/certificates/{stableNodeID}?tenant=`, which is now the
only route tailworks itself calls (the admin UI's object page switched to it;
hostd already used it). The deprecated route still works, and its responses
carry a `Deprecation` header and a `Link` to the replacement
(`rel="successor-version"`). It will be removed no earlier than two releases
from now, with a Breaking note.

Operator action: scripts that call `GET /v1/objects/{id}/certificate` should
read the object's `stableNodeID` and `tenantID` (`GET /v1/objects/{id}`) and
call `GET /v1/certificates/{stableNodeID}?tenant=<tenantID>` instead.
