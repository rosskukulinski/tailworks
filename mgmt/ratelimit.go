package mgmt

import (
	"math"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// API request limits for tailnet callers (issue #115; docs/limits.md
// "Management API"). Each caller (WhoIs node) has its own token bucket;
// hosts get a larger one, because their store appends, heartbeats and
// queue pulls are the data path. Non-host callers (admins, tenant admins,
// approvers, MCP clients) also share a bounded number of in-flight
// requests, so a flood of slow requests can't exhaust mgmt; hosts are
// outside that gate, so a user flood can never delay a heartbeat into a
// fence. mgmt's own node (the operator console) and the local socket are
// not limited. Over a limit is 429 rate_limited with Retry-After.
const (
	// DefaultAPIRPS is a non-host caller's sustained requests per second;
	// its burst is twice that.
	DefaultAPIRPS = 100
	// DefaultAPIHostRPS is a host's sustained requests per second; its
	// burst is twice that.
	DefaultAPIHostRPS = 1000
	// DefaultAPIInFlight bounds the requests from non-host callers that
	// mgmt serves at once.
	DefaultAPIInFlight = 256
)

// APILimits configure the limiter. Zero fields take the defaults; a
// negative field turns that limit off.
type APILimits struct {
	RPS      float64 // per non-host caller
	HostRPS  float64 // per host
	InFlight int     // non-host callers, all together
}

// APILimiter enforces APILimits (Wrap). The zero value is not usable; use
// NewAPILimiter.
type APILimiter struct {
	rps, hostRPS float64
	gate         chan struct{} // nil: no in-flight bound
	now          func() time.Time

	mu      sync.Mutex
	buckets map[string]*rate.Limiter
	swept   time.Time
}

// NewAPILimiter returns a limiter for l (defaults filled in).
func NewAPILimiter(l APILimits) *APILimiter {
	def := func(v, d float64) float64 {
		if v == 0 {
			return d
		}
		return v
	}
	a := &APILimiter{rps: def(l.RPS, DefaultAPIRPS), hostRPS: def(l.HostRPS, DefaultAPIHostRPS),
		now: time.Now, buckets: map[string]*rate.Limiter{}}
	n := l.InFlight
	if n == 0 {
		n = DefaultAPIInFlight
	}
	if n > 0 {
		a.gate = make(chan struct{}, n)
	}
	return a
}

// sweepAfter: buckets idle this long are full again and are dropped, so the
// map holds only recent callers.
const sweepAfter = time.Minute

// allow takes a token from key's bucket. It returns 0, or how long until a
// token is available.
func (a *APILimiter) allow(key string, rps float64) time.Duration {
	if rps < 0 {
		return 0
	}
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if now.Sub(a.swept) > sweepAfter {
		for k, b := range a.buckets {
			if b.TokensAt(now) >= float64(b.Burst()) {
				delete(a.buckets, k)
			}
		}
		a.swept = now
	}
	b := a.buckets[key]
	if b == nil {
		b = rate.NewLimiter(rate.Limit(rps), int(math.Max(1, 2*rps)))
		a.buckets[key] = b
	}
	r := b.ReserveN(now, 1)
	if d := r.DelayFrom(now); d > 0 {
		r.CancelAt(now)
		return d
	}
	return 0
}

func tooMany(w http.ResponseWriter, r *http.Request, wait time.Duration, format string, a ...any) {
	e := errf(http.StatusTooManyRequests, "rate_limited", format, a...)
	e.RetryAfter = max(1, int(math.Ceil(wait.Seconds())))
	writeErr(w, r, e)
}

// Wrap limits h. It must run inside TailnetHandler (it reads the
// Principal TailnetHandler sets); requests without one are not limited.
func (a *APILimiter) Wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalOf(r.Context())
		if p.Kind == "" || p.Kind == "local" {
			h.ServeHTTP(w, r)
			return
		}
		key := p.NodeID
		if key == "" {
			key, _, _ = net.SplitHostPort(r.RemoteAddr)
		}
		rps := a.rps
		if p.Kind == "host" {
			rps = a.hostRPS
		}
		if wait := a.allow(key, rps); wait > 0 {
			tooMany(w, r, wait, "caller %s is over its mgmt API rate (%g requests/s); retry after Retry-After", key, rps)
			return
		}
		if p.Kind != "host" && a.gate != nil {
			select {
			case a.gate <- struct{}{}:
				defer func() { <-a.gate }()
			default:
				tooMany(w, r, time.Second, "mgmt is serving its maximum of %d concurrent API requests from non-host callers; retry after Retry-After", cap(a.gate))
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}
