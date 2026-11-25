package netd

import (
	"os"
	"sync"
	"time"
)

// A kill with KillOptions.Tombstone keeps the killed node up for a while
// (answering object_killed), then closes it and removes its state dir.
// Object IDs are deterministic, so the same ID can be created again before
// that (issue #364: a workload deployed again right after `tw undeploy`,
// once mgmt has forgotten the killed objects). The new node uses the same
// state dir, so the tombstone must end first: otherwise the old node keeps
// running on that dir and, when its timer fires, removes the new node's
// state. Create ends it early; the timer then does nothing.

// tombstoneEnd ends one kill's tombstone, once (zero value: no tombstone).
type tombstoneEnd struct {
	mu    sync.Mutex
	end   func()
	timer *time.Timer
}

// arm schedules the end of o's tombstone after d: close the node and, unless
// keep, remove its state dir.
func (t *tombstoneEnd) arm(o *Object, keep bool, d time.Duration) {
	var once sync.Once
	end := func() {
		once.Do(func() {
			o.teardown()
			if !keep {
				os.RemoveAll(o.dir)
			}
		})
	}
	t.mu.Lock()
	t.end = end
	t.timer = time.AfterFunc(d, end)
	t.mu.Unlock()
}

// now ends the tombstone at once if one is up; a no-op otherwise.
func (t *tombstoneEnd) now() {
	t.mu.Lock()
	end, timer := t.end, t.timer
	t.end, t.timer = nil, nil
	t.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if end != nil {
		end()
	}
}

// endTombstoneOf ends the tombstone of the killed object id, if it is still
// up, so a new object of the same ID can take its state dir.
func (r *Registry) endTombstoneOf(id string) {
	if o := r.Get(id); o != nil && o.State() == StateKilled {
		o.tomb.now()
	}
}
