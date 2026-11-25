package hostd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GET /v1/logs is a push route (mgmt asks the lease holder for `tw logs`
// from a linked machine), so it is on every push surface: the in-process
// handler (single-process tw up) as well as the local socket. POST
// /v1/teardown is the operator's (tw down) and stays on the local socket.
func TestPushRoutes(t *testing.T) {
	t.Parallel()
	h := &Host{workloads: map[string]*workload{}, objects: map[string]*object{}, poolBusy: map[string]bool{}}
	routed := func(hd http.Handler, method, path string) bool {
		w := httptest.NewRecorder()
		hd.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return !(w.Code == 404 && strings.Contains(w.Body.String(), "404 page not found"))
	}
	if !routed(h.PushHandler(), "GET", "/v1/logs?tenant=t&workload=w") {
		t.Error("GET /v1/logs is not routed by PushHandler (in-process mgmt)")
	}
	if !routed(h.pushAPI(), "GET", "/v1/logs?tenant=t&workload=w") {
		t.Error("GET /v1/logs is not routed on the local socket")
	}
	if routed(h.PushHandler(), "POST", "/v1/teardown") {
		t.Error("POST /v1/teardown is routed for mgmt; it is the operator's")
	}
}
