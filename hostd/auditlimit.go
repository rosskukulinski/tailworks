package hostd

import "time"

// tenantNoteLimiter is a token bucket per object on tenant audit() notes
// (RT3-03 / H03): tenant code cannot fill the host's audit queue, nor
// crowd out the platform's records, by calling audit() in a loop. A note
// over the bucket is refused before it is chained and counted in the next
// audit_dropped record.
type tenantNoteLimiter struct {
	rate, burst float64
	buckets     map[string]*noteBucket
}

type noteBucket struct {
	tokens float64
	last   time.Time
}

const (
	tenantNoteRate  = 10  // notes per second per object, sustained
	tenantNoteBurst = 200 // notes per object in a burst
	// maxNoteBuckets bounds the map; full (idle) buckets are evicted first.
	maxNoteBuckets = 4096
)

func newTenantNoteLimiter() tenantNoteLimiter {
	return tenantNoteLimiter{rate: tenantNoteRate, burst: tenantNoteBurst, buckets: map[string]*noteBucket{}}
}

// allow takes one token from objectID's bucket. Called with auditLog.mu held.
func (l *tenantNoteLimiter) allow(objectID string, now time.Time) bool {
	b := l.buckets[objectID]
	if b == nil {
		if len(l.buckets) >= maxNoteBuckets {
			l.evict(now)
		}
		b = &noteBucket{tokens: l.burst, last: now}
		l.buckets[objectID] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// evict drops refilled buckets, then (a flood of distinct objects, which
// only hostd can create) arbitrary ones down to 90%.
func (l *tenantNoteLimiter) evict(now time.Time) {
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
	for k := range l.buckets {
		if len(l.buckets) < maxNoteBuckets*9/10 {
			break
		}
		delete(l.buckets, k)
	}
}
