---
kind: security
upgrade: [protocol]
---
tw-mgmt now refuses tailnet requests whose `Host` header is not one of its own names (its MagicDNS FQDN or short name, `-tsnet-hostname`, `-tsnet-service`) or an IP literal, with the new error `421 misdirected_request`. This closes a DNS-rebinding hole: before, a web page on an attacker's domain rebound to mgmt's tailnet IP was same-origin with mgmt's plain-HTTP `:80` listener and could read `/v1` and post admin UI forms with the admin's identity. The `tw` CLI, hosts and the admin UI address mgmt by those names or its IP and are unaffected. The admin UI's `Origin` check is now tied to the same allowlist.

Operator action: if you reach tw-mgmt through a CNAME or another DNS name of your own, use its tailnet name or IP instead. If you renamed the mgmt node, restart tw-mgmt so it learns the new name.
