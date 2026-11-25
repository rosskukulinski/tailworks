package hostd

import "github.com/rosskukulinski/tailworks/netd"

// attrPublicReason labels tw.public.rejected by the limit that refused.
const attrPublicReason = "tw.public.reason"

// countPublicRejected adds a public node's per-minute refusal summary to
// the tw.public.rejected counter (the same counts as the public_rejected
// audit record).
func (h *Host) countPublicRejected(o *object, s netd.PublicRejectSummary) {
	if h.tel == nil {
		return
	}
	c := h.tel.reg.Counter("tw.public.rejected", "{rejection}",
		"Public requests and connections refused by the public listener's limits, by reason.",
		attrTenant, attrWorkload, attrPublicReason)
	for _, r := range []struct {
		reason string
		n      int
	}{
		{"conns", s.ConnsRefused},
		{"ip_conns", s.IPConnsRefused},
		{"rate_limited", s.RateLimited},
		{"too_large", s.TooLarge},
	} {
		if r.n > 0 {
			c.Add(float64(r.n), o.TenantID, o.Workload, r.reason)
		}
	}
}
