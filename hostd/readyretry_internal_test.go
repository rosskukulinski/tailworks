package hostd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// A ready report that got no answer or a 5xx (mgmt restarting, a replica
// down) is sent again, as one refused with lease_lost is (#604, found by
// e2e/dataplane's deploys loop: the report failed with "connection reset
// by peer" while mgmt restarted, and the deployment's endpoint stayed
// "minted" for good).
func TestRetryReadyAfterMgmtOutage(t *testing.T) {
	t.Parallel()
	var (
		mu      sync.Mutex
		reports []int // status answered to each ready report
	)
	mg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch {
		case r.Method == "PATCH" && r.URL.Path == "/v1/objects/o_dp":
			mu.Lock()
			st := 200
			if len(reports) == 0 {
				st = 503 // the first retry still finds mgmt down
			}
			reports = append(reports, st)
			mu.Unlock()
			w.WriteHeader(st)
			if st == 503 {
				w.Write([]byte(`{"code":"store_unavailable","message":"test mgmt"}`))
				return
			}
			w.Write([]byte(`{}`))
		case r.Method == "GET" && r.URL.Path == "/v1/hosts/h/assignments":
			w.Write([]byte(`{"items":[]}`))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"code":"not_found","message":"test mgmt"}`))
		}
	}))
	t.Cleanup(mg.Close)
	h, _ := ctrHost(t, func(c *Config) { c.Mgmt = &mgmtapi.Client{Base: mg.URL} })
	stallWorkload(h, "dp", true)
	h.mu.Lock()
	o := h.objects["o_dp"]
	gen := o.nodeGen
	h.mu.Unlock()

	h.retryReady(o, mgmtapi.ReadyRequest{NodeID: o.NodeID, StableNodeID: o.NodeID}, gen)
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 2 || reports[1] != 200 {
		t.Fatalf("ready reports answered %v; want a 503, then a retry that mgmt accepts", reports)
	}
}

func TestRetryableReady(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{errors.New("read: connection reset by peer"), true},
		{&mgmtapi.Error{Code: "store_unavailable", Status: 503}, true},
		{&mgmtapi.Error{Code: mgmtapi.CodeLeaseLost, Status: 409}, true},
		{&mgmtapi.Error{Code: mgmtapi.CodeKilled, Status: 410}, false},
		{&mgmtapi.Error{Code: "bad_request", Status: 400}, false},
	} {
		if got := retryableReady(c.err); got != c.want {
			t.Errorf("retryableReady(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
