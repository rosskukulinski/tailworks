package hostd

import (
	"net/http"
	"strings"

	"github.com/rosskukulinski/tailworks/netd"
)

// stripRoutingHeaders removes the platform's internal routing headers from
// an ingress request once hostd has read them (#303). netd stamps
// netd.HeaderObject so hostd can find the object; tenant code (a Worker's
// fetch, a Durable Object, a Workflow) must not see it, as on Cloudflare an
// app sees no platform routing headers. netd already strips client-sent
// x-tw-* / tw-* copies, so anything left with those prefixes is ours.
// netd.HeaderCF stays: it is workerd's cfBlobHeader, which workerd itself
// consumes and turns into request.cf.
func stripRoutingHeaders(h http.Header) {
	for k := range h {
		if strings.EqualFold(k, netd.HeaderCF) {
			continue
		}
		if l := strings.ToLower(k); strings.HasPrefix(l, "x-tw-") || strings.HasPrefix(l, "tw-") {
			delete(h, k)
		}
	}
}
