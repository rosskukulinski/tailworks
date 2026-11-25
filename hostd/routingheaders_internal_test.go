package hostd

import (
	"net/http"
	"testing"

	"github.com/rosskukulinski/tailworks/netd"
)

// TestStripRoutingHeaders: hostd drops netd's routing headers (#303) before
// the request reaches workerd, keeping only the cf blob workerd consumes.
func TestStripRoutingHeaders(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set(netd.HeaderObject, "obj-1")
	h.Set(netd.HeaderCF, `{"objectId":"obj-1"}`)
	h.Set("X-Tw-Future-Routing", "x")
	h.Set("Tw-Something", "x")
	h.Set("Traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	h.Set("X-Twin", "app header") // no dash after "tw": not ours
	h.Set("Accept", "*/*")
	stripRoutingHeaders(h)
	for k := range h {
		switch k {
		case "X-Tw-Cf", "Traceparent", "X-Twin", "Accept":
		default:
			t.Errorf("%s survived: %v", k, h)
		}
	}
	if h.Get(netd.HeaderCF) == "" || h.Get("X-Twin") == "" || h.Get("Accept") == "" || h.Get("Traceparent") == "" {
		t.Errorf("stripped a header it must keep: %v", h)
	}
}
