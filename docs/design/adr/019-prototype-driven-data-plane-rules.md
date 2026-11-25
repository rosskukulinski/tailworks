# ADR-019: Prototype-driven data-plane rules

**Date:** 2026-11-07
**Status:** Accepted (measured; prototype/per-object-identity/README.md).
Rule 1 amended 2026-11-21 by ADR-049: internet egress
is on by default and leaves from the host's network through netd's SSRF
guard, not via an exit node or Aperture. Tailnet names still resolve from
the netmap only.
1. **netd owns all egress policy.** `tsnet.Server.Dial` falls back to the host network for non-tailnet IPs and to the host resolver for unknown names (`net/tsdial/tsdial.go:647-652, 450, 475-488`). Measured: example.com was reached from the host IP. So netd resolves from the netmap only, allows only tailnet IPs, refuses its own and host IPs, and sends internet egress only via an exit node or Aperture.
2. **No mutual peer visibility between objects by default.** netd RSS was 176 MB with 0 peers vs 417 MB with a full mesh at N=50, roughly O(N²). Generated grants never give `tag:tw-obj-*` sight of each other. Same-host object→object calls go through netd's policy check.
3. **Kill tier 1 resets connections.** `Close()` alone black-holes callers until their own timeout, so netd closes and RSTs in-flight flows first, then triggers tier 2 (device delete).
4. **`OmitAuth = true`** on every tsnet LocalClient. Otherwise macOS pays ~25 ms per call; WhoIs is 0.04 ms with it.
5. **Per-object outbound attribution currently relies on Dynamic Worker facets (experimental).** Facets lose alarms (DX-H4). Upstream ask WD-3 (per-DO outbound props) is on the v1 critical path. Until it lands, choose per class: facet mode (attributed egress, no alarms) or plain-DO mode (alarms, workload-level egress identity).
