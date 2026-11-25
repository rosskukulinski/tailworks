package hostd

import (
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
)

// TestEgressObjHandlerReadsStatusUnderLock: egressObjHandler's "killed"
// check read o.Status without h.mu, while the object's node join
// (startNode, objects.go) rewrites it under h.mu. `make test` caught it as
// a data race in TestTelemetry (http.go vs objects.go). Run under -race
// (as `make test` does), this test fails on the unlocked read.
func TestEgressObjHandlerReadsStatusUnderLock(t *testing.T) {
	t.Parallel()
	h, _ := ctrHost(t, nil)
	inst := ctrInst(h, "tn", "probe")
	inst.w.status = "active"
	const root = "deadbeefdeadbeefdeadbeefdeadbeef"
	inst.res = &gen.Result{MACKey: root}
	inst.app = &bundle.App{Classes: map[string]*bundle.Class{"Agent": {Name: "Agent", Egress: bundle.EgressObject}}}
	o := &object{ID: "obj_a", NetdID: "n_a", TenantID: "tn", Workload: "probe", Class: "Agent", Name: "a1",
		Hostname: "a1", Status: statusKilled, live: true, readyCh: make(chan struct{})}
	h.mu.Lock()
	h.objects[o.ID] = o
	h.byKey[o.key()] = o
	h.mu.Unlock()
	handler := h.egressObjHandler(inst)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() { // the join path: rewrites Status under h.mu
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			h.mu.Lock()
			o.Status = statusKilled
			h.mu.Unlock()
		}
	}()
	for i := range 200 {
		r := httptest.NewRequest("GET", "http://peer.example.ts.net/x", nil)
		ts, nonce := strconv.FormatInt(time.Now().UnixMilli(), 10), "n-"+strconv.Itoa(i)
		r.Header.Set(gen.HeaderClass, "Agent")
		r.Header.Set(gen.HeaderName, "a1")
		r.Header.Set(gen.HeaderTS, ts)
		r.Header.Set(gen.HeaderNonce, nonce)
		r.Header.Set(gen.HeaderMAC, gen.ComputeMAC(gen.ObjectMACKey(root, "Agent", "a1"), "Agent", "a1", "GET", r.RequestURI, ts, nonce))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code == 200 {
			close(stop)
			wg.Wait()
			t.Fatalf("a killed object's egress was served: %d %s", w.Code, w.Body.String())
		}
	}
	close(stop)
	wg.Wait()
}
