---
kind: feature
upgrade: []
---
tailworks runs on a self-hosted **Headscale** control plane (ADR-015): `tw up --control=headscale` and `tw tenant add --control=headscale`. It needs Headscale v0.29 or later. There is no Funnel (public routes) and there are no Tailscale Services there; see docs/headscale.md.
