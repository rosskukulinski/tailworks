---
kind: fix
upgrade: []
---
A Worker or Durable Object that answers a request without reading its body
(a killed object's 410, a handler that rejects an upload with 401 or 413)
now reaches the caller with that answer. Before, when the body was about
16 KB or more, the caller often got `503 upstream_unavailable` ("write:
broken pipe" or "socket is not connected") instead.
