package hostd

import (
	"context"
	"testing"
	"time"
)

// After Teardown no object is created (maintainPools starts no pool node,
// and h.create is refused), and Teardown itself returns only once creates
// already in flight are done: `tw down --purge` lists objects after it, so
// its list is complete.
func TestTeardownGate(t *testing.T) {
	t.Parallel()
	h := &Host{
		cfg:        Config{PoolNodes: 1},
		ctx:        context.Background(),
		poolDemand: map[string]time.Time{"tn/w/SupportAgent": time.Now()},
		poolBusy:   map[string]bool{},
		workloads:  map[string]*workload{},
		objects:    map[string]*object{},
	}
	// A create in flight before teardown: Teardown waits for it.
	done, err := h.gateCreate()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		done()
	}()
	t0 := time.Now()
	if n := h.Teardown(); n != 0 {
		t.Fatalf("Teardown: %d still creating", n)
	}
	if d := time.Since(t0); d < 150*time.Millisecond {
		t.Fatalf("Teardown returned after %v, before the in-flight create finished", d)
	}
	// New creates are refused, through h.create itself.
	if _, err := h.create(context.Background(), &workload{name: "w", tenant: &tenant{}}, "Open", "x", nil, nil, "", ""); err == nil ||
		asAPIError(err) == nil || asAPIError(err).Code != "upstream_unavailable" {
		t.Fatalf("create after teardown: %v", err)
	}
	h.mu.Lock()
	n := h.createsInFlight
	h.mu.Unlock()
	if n != 0 {
		t.Fatalf("a refused create left createsInFlight = %d", n)
	}
	// And no pool refill.
	h.maintainPools()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.poolBusy) != 0 {
		t.Fatalf("maintainPools started a pool create after Teardown: %v", h.poolBusy)
	}
	if _, ok := h.poolDemand["tn/w/SupportAgent"]; !ok {
		t.Fatal("maintainPools ran its demand loop after Teardown")
	}
}
