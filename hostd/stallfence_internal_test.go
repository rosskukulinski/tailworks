package hostd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/netd"
)

// TestStallFence (#501, data-plane chaos F3): a host that was stopped past
// FenceAfter (SIGSTOP, a paused VM) has had no *failed* heartbeat, so tick
// never self-fenced it. On resume its stateful workload must refuse ingress
// and egress at once, before any heartbeat, and stay refused until the
// first heartbeat's revoke has been applied. A stateless workload keeps
// serving, as through a partition.
func TestStallFence(t *testing.T) {
	t.Parallel()
	var (
		mu       sync.Mutex
		inFlight []string // gate state seen by mgmt while hostd waited on it
		h        *Host
	)
	probe := func(where string) {
		mu.Lock()
		defer mu.Unlock()
		if w := h.workloads["tn/dp"]; w.statusSnapshot().Status != "active" {
			return // the revoke has stopped it (the sync after the heartbeat)
		}
		if err := h.stallFencedEgress("n_dp"); err == nil {
			inFlight = append(inFlight, where+": egress open")
		}
		if rec := ingress(h, "n_dp"); rec.Code != http.StatusServiceUnavailable {
			inFlight = append(inFlight, where+": ingress open")
		}
	}
	mg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch {
		case r.Method == "PUT" && r.URL.Path == "/v1/hosts/h/status":
			probe("heartbeat") // the answer carries the revoke: not applied yet
			json.NewEncoder(w).Encode(mgmtapi.HeartbeatResponse{Revoke: []string{mgmtapi.LeaseName("tn", "dp")}})
		case r.Method == "GET" && r.URL.Path == "/v1/hosts/h/assignments":
			probe("assignments") // revoke asks whether it was re-assigned here
			w.Write([]byte(`{"items":[]}`))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"code":"not_found","message":"test mgmt"}`))
		}
	}))
	t.Cleanup(mg.Close)
	h, _ = ctrHost(t, func(c *Config) { c.Mgmt = &mgmtapi.Client{Base: mg.URL}; c.FenceAfter = DefaultFenceAfter })

	stateful := stallWorkload(h, "dp", true)
	stateless := stallWorkload(h, "web", false)
	h.mu.Lock()
	h.lastHB = time.Now()
	h.mu.Unlock()

	// Fresh heartbeat: both pass the gate.
	if err := h.stallFencedEgress("n_dp"); err != nil {
		t.Fatalf("fresh heartbeat, stateful egress: %v", err)
	}
	if why := h.stallFenced(stateful); why != "" {
		t.Fatalf("fresh heartbeat, stateful ingress: %s", why)
	}

	// The stall: no heartbeat failed, the last success is past FenceAfter.
	h.mu.Lock()
	h.lastHB = time.Now().Add(-DefaultFenceAfter - 5*time.Second)
	h.mu.Unlock()
	err := h.holdEgress(context.Background(), "tn", "n_dp", "NODE-dp")
	if e, ok := netd.AsError(err); !ok || e.Code != netd.CodeUpstreamUnavailable {
		t.Fatalf("stalled host, stateful egress: %v, want upstream_unavailable", err)
	}
	if rec := ingress(h, "n_dp"); rec.Code != http.StatusServiceUnavailable || rec.Header().Get("tw-error") != "upstream_unavailable" {
		t.Fatalf("stalled host, stateful ingress: %d %s %s", rec.Code, rec.Header().Get("tw-error"), rec.Body)
	}
	if err := h.holdEgress(context.Background(), "tn", "n_web", "NODE-web"); err != nil {
		t.Fatalf("stalled host, stateless egress: %v", err)
	}
	if why := h.stallFenced(stateless); why != "" {
		t.Fatalf("stalled host, stateless ingress: %s", why)
	}

	// The first heartbeat after the stall succeeds and carries the revoke.
	// The gate stays shut until the revoke is applied.
	h.tick()
	mu.Lock()
	defer mu.Unlock()
	if len(inFlight) > 0 {
		t.Fatalf("stateful workload served while its revoke was in flight: %v", inFlight)
	}
	if st := stateful.statusSnapshot().Status; st != "stopped" {
		t.Fatalf("after the revoke: stateful workload status %q, want stopped", st)
	}
	if why := h.stallFenced(stateful); why != "" {
		t.Fatalf("after a successful heartbeat the gate is still shut: %s", why)
	}
}

// TestStallFenceAnswerFromBeforeStall: freshness counts from when a
// heartbeat left. An answer that waited out a stall (here: a slow mgmt, with
// a short FenceAfter) doesn't reopen the gate, even though it succeeded.
func TestStallFenceAnswerFromBeforeStall(t *testing.T) {
	t.Parallel()
	const fence = 300 * time.Millisecond
	mg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if r.Method == "PUT" && r.URL.Path == "/v1/hosts/h/status" {
			time.Sleep(2 * fence) // the answer is held past FenceAfter
			json.NewEncoder(w).Encode(mgmtapi.HeartbeatResponse{})
			return
		}
		w.Write([]byte(`{"items":[]}`))
	}))
	t.Cleanup(mg.Close)
	h, _ := ctrHost(t, func(c *Config) { c.Mgmt = &mgmtapi.Client{Base: mg.URL}; c.FenceAfter = fence })
	stateful := stallWorkload(h, "dp", true)
	h.mu.Lock()
	h.lastHB = time.Now()
	h.mu.Unlock()
	h.tick()
	if why := h.stallFenced(stateful); why == "" {
		t.Fatal("an answer that arrived after FenceAfter reopened the gate")
	}
}

// stallWorkload registers an active workload tn/<name> with one ready,
// reported object n_<name>.
func stallWorkload(h *Host, name string, fence bool) *workload {
	if h.ctx == nil { // tick runs without Start
		h.ctx, h.cancel = context.WithCancel(context.Background())
	}
	w := &workload{h: h, tenantID: "tn", name: name, status: "active", noFence: !fence, tenant: &tenant{id: "tn"}}
	w.active = &instance{h: h, w: w}
	o := &object{ID: "o_" + name, NetdID: "n_" + name, TenantID: "tn", Workload: name, Status: statusReady, NodeID: "NODE-" + name, readyCh: make(chan struct{})}
	o.report.node = o.NodeID
	h.mu.Lock()
	h.workloads["tn/"+name] = w
	h.objects[o.ID] = o
	h.byNetd[o.NetdID] = o
	h.mu.Unlock()
	return w
}

// ingress sends one tailnet request to node netdID through hostd's ingress.
func ingress(h *Host, netdID string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://node/", nil)
	r.Header.Set(netd.HeaderObject, netdID)
	r.Header.Set(netd.HeaderCF, `{}`)
	rec := httptest.NewRecorder()
	h.ingressHandler(&tenant{id: "tn"}).ServeHTTP(rec, r)
	return rec
}
