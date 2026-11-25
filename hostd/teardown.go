package hostd

import (
	"net/http"
	"time"
)

// Teardown gate for `tw down --purge`. down lists every object and kills
// it, then stops the daemons. Anything created after the list stayed on
// the tailnet as a device:
//   - a pool refill (killing a pool node let maintainPools top the pool up:
//     e2e-multi left one "<prefix>-pool-…" per tailnet, on master too);
//   - a new object from a request that arrived meanwhile, e.g. a public
//     request over Funnel to a class with public.create (e2e-public left
//     "<prefix>-open-…" devices).
// POST /v1/teardown closes the gate: no pool refill and no new object from
// then on, and it waits for creates already in flight, so a list taken
// after it is complete.

// teardownWait bounds the wait for in-flight creates (each has ≤ 30 s).
const teardownWait = 35 * time.Second

// gateCreate is h.create's gate: refused once teardown began, else counted
// until done() so Teardown can wait for it.
func (h *Host) gateCreate() (done func(), err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tearingDown {
		return nil, &apiError{Code: "upstream_unavailable", Detail: "tw is shutting down (tw down): no new objects"}
	}
	h.createsInFlight++
	return func() {
		h.mu.Lock()
		h.createsInFlight--
		h.mu.Unlock()
	}, nil
}

// Teardown closes the gate and waits (up to teardownWait) until no create
// is in flight. It returns how many still were when it gave up.
func (h *Host) Teardown() (stillCreating int) {
	h.mu.Lock()
	h.tearingDown = true
	h.mu.Unlock()
	deadline := time.Now().Add(teardownWait)
	for {
		h.mu.Lock()
		n := h.createsInFlight + len(h.poolBusy)
		h.mu.Unlock()
		if n == 0 || time.Now().After(deadline) {
			return n
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// teardownHandler is POST /v1/teardown (push API; the local socket for tailworks
// down).
func (h *Host) teardownHandler(w http.ResponseWriter, r *http.Request) {
	n := h.Teardown()
	writeJSON(w, 200, map[string]any{"ok": n == 0, "creating": n})
}
