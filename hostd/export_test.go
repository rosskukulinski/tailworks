package hostd

import (
	"sync"
	"time"
)

// CronTickAt runs the cron scheduler's tick for the minute containing now
// (as cronLoop does at each minute boundary) and waits for the runs it
// starts to finish.
func (h *Host) CronTickAt(now time.Time) int {
	var wg sync.WaitGroup
	due := h.cronDue(now)
	for _, r := range due {
		wg.Add(1)
		go func() { defer wg.Done(); h.runScheduled(r) }()
	}
	wg.Wait()
	return len(due)
}

// ApplyPublicConcurrently runs n applyPublic calls for workload name at once,
// with the spec mgmt last assigned, and waits for them. It first forgets
// the options the public node was started with: the state a sync sees
// while a deploy's applyPublic has created the node but not yet recorded
// them (issue #133), so every call would take the restart path.
func (h *Host) ApplyPublicConcurrently(name string, n int) {
	h.mu.Lock()
	var w *workload
	for _, x := range h.workloads {
		if x.name == name {
			w = x
		}
	}
	ps := h.public[w.tenantID+"/"+w.name]
	spec := ps.spec
	ps.opts = nil
	h.mu.Unlock()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; h.applyPublic(w, spec) }()
	}
	close(start)
	wg.Wait()
}
