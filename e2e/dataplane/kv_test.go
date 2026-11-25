//go:build e2edataplane

package dataplane

import (
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// KV (docs/kv.md "Where the data is"), held to the bounds it documents and
// nothing more:
//
//   - Read-your-writes and durability: the workload always reads its own
//     acknowledged writes, across restarts, moves and takeovers (a new host
//     rebuilds the namespace from mgmt's journal).
//   - Expiry: an expired key disappears from get at once.
//
// One sequential client writes increasing values to a few keys and reads
// them back, and writes keys with expirationTtl 60 s that it reads again
// before and after they expire. Writes need mgmt, so failures while it is
// down are expected (not checked); a failed put may or may not have landed.

const kvTTL = 60 * time.Second

var kvKeys = []string{"a", "b", "c"}

type kvOp struct {
	Op         string // put | get | final (a get after healing)
	K          string
	V          int // the value put, or read (0: missing)
	TTL        bool
	Sent, Recv time.Time
	OK         bool
	Host       string
	Off        time.Duration // the host's wall-clock offset when sent (skew_test.go)
	Err        string        `json:",omitempty"`
}

func (h *history) kv(o kvOp) { h.mu.Lock(); h.kvs = append(h.kvs, o); h.log("kv", o); h.mu.Unlock() }

// kvCall is one KV op against the current holder.
func (w *workload) kvCall(op, k string, v int, ttl bool) {
	b, host := w.base()
	path := "/kv/get?k=" + url.QueryEscape(k)
	if op == "put" {
		path = fmt.Sprintf("/kv/put?k=%s&v=%d", url.QueryEscape(k), v)
		if ttl {
			path += fmt.Sprintf("&ttl=%d", int(kvTTL/time.Second))
		}
	}
	o := kvOp{Op: op, K: k, V: v, TTL: ttl, Sent: time.Now(), Host: host, Off: w.c.offset(host)}
	var r struct{ V *string }
	_, err := getJSON(w.cli, b+path, &r)
	o.Recv, o.OK = time.Now(), err == nil
	if err != nil {
		o.Err = err.Error()
	} else if op != "put" {
		o.V = 0
		if r.V != nil {
			o.V, _ = strconv.Atoi(*r.V)
		}
	}
	w.h.kv(o)
}

// runKV starts the KV client: every 300 ms, in turn, a TTL put, a put to
// one of kvKeys, a get of it, and a get of a TTL key (alternately one
// 20–40 s old, which must be there, and one over 75 s old, which must not).
func (w *workload) runKV() {
	i, n := 0, 0
	type ttlPut struct {
		k  string
		at time.Time
	}
	var ttls []ttlPut
	w.every(300*time.Millisecond, func() {
		i++
		k := kvKeys[(i/4)%len(kvKeys)]
		switch i % 4 {
		case 0:
			n++
			tk := fmt.Sprintf("t%05d", n)
			ttls = append(ttls, ttlPut{tk, time.Now()})
			w.kvCall("put", tk, 1, true)
		case 1:
			n++
			w.kvCall("put", k, n, false)
		case 2:
			w.kvCall("get", k, 0, false)
		case 3:
			lo, hi := 20*time.Second, 40*time.Second
			if (i/4)%2 == 1 {
				lo, hi = kvTTL+15*time.Second, 10*time.Minute
			}
			for j := len(ttls) - 1; j >= 0; j-- {
				if age := time.Since(ttls[j].at); age >= lo && age <= hi {
					w.kvCall("get", ttls[j].k, 0, true)
					break
				}
			}
		}
	})
}

// kvFinal reads every key once more after healing (durability).
func (w *workload) kvFinal() {
	for _, k := range kvKeys {
		for try := 0; try < 10; try++ {
			w.kvCall("final", k, 0, false)
			w.h.mu.Lock()
			ok := w.h.kvs[len(w.h.kvs)-1].OK
			w.h.mu.Unlock()
			if ok {
				break
			}
			time.Sleep(time.Second)
		}
	}
}

// checkKV holds the history to the documented bounds.
func checkKV(h *history) result {
	r := result{ID: "KV", Pass: true}
	puts := map[string][]kvOp{}
	for _, o := range h.kvs {
		if o.Op == "put" {
			puts[o.K] = append(puts[o.K], o)
		}
	}
	// skewSlack covers workerd taking up to a second to follow a step.
	const slack = 1500 * time.Millisecond
	reads, stale, phantom, expiredSeen, lostTTL, finals := 0, 0, 0, 0, 0, 0
	for _, g := range h.kvs {
		if g.Op == "put" || !g.OK {
			continue
		}
		reads++
		if g.Op == "final" {
			finals++
		}
		ps := puts[g.K]
		if g.TTL {
			if len(ps) != 1 || !ps[0].OK {
				continue // its put failed: it may or may not be there
			}
			p := ps[0]
			// Expiry is stamped and checked on the holder's wall clock, so
			// clock offsets shift when it happens in real time. A Durable
			// Object's clock never goes back (workerd holds it at the
			// latest value it returned), so the clock that stamps or checks
			// can be ahead of the host's by as much as the host was ahead
			// earlier. Every KV op samples the holder's offset:
			//   - it must be gone once the reader's clock has passed the
			//     latest expiry the writer could have stamped (preMax: the
			//     largest offset before the put);
			//   - it must be there while no holder's clock, at any time in
			//     between (inMax), could have passed the expiry stamped on
			//     the writer's clock. Once one has, its alarm journals the
			//     delete, and a step back doesn't bring the key back.
			// An op sent while a step was being applied may have seen either
			// offset (offRange).
			pLo, pHi := offRange(h.faults, p.Host, p.Sent, p.Off)
			gLo, gHi := offRange(h.faults, g.Host, g.Sent, g.Off)
			preMax, inMax := pHi, max(pHi, gHi)
			for _, o := range h.kvs {
				_, hi := offRange(h.faults, o.Host, o.Sent, o.Off)
				if !o.Sent.After(p.Recv) {
					preMax = max(preMax, hi)
				}
				if !o.Sent.Before(p.Sent) && !o.Sent.After(g.Recv) {
					inMax = max(inMax, hi)
				}
			}
			switch {
			case g.V != 0 && g.Sent.After(p.Recv.Add(kvTTL+preMax-gLo+slack)):
				expiredSeen++
				r.fail("expired key served: %s put at %s (on %s, clock %+v), read at %s (on %s, clock %+v), %.1fs after the put",
					g.K, p.Recv.Format("15:04:05.000"), p.Host, p.Off, g.Sent.Format("15:04:05.000"), g.Host, g.Off, g.Sent.Sub(p.Recv).Seconds())
			case g.V == 0 && g.Recv.Before(p.Sent.Add(kvTTL-time.Second-(inMax-pLo)-slack)):
				lostTTL++
				r.fail("acknowledged key missing before its expiry: %s put at %s (on %s, clock %+v), read at %s (on %s, clock %+v), %.1fs after the put",
					g.K, p.Recv.Format("15:04:05.000"), p.Host, p.Off, g.Sent.Format("15:04:05.000"), g.Host, g.Off, g.Sent.Sub(p.Recv).Seconds())
			}
			continue
		}
		maxAcked, known := 0, g.V == 0
		for _, p := range ps {
			if p.OK && p.Recv.Before(g.Sent) && p.V > maxAcked {
				maxAcked = p.V
			}
			if p.V == g.V && p.Sent.Before(g.Recv) {
				known = true
			}
		}
		if g.V < maxAcked {
			stale++
			r.fail("%s of %s at %s (on %s) read %d, but %d was acknowledged before it was sent (lost or stale write)",
				g.Op, g.K, g.Sent.Format("15:04:05.000"), g.Host, g.V, maxAcked)
		}
		if !known {
			phantom++
			r.fail("%s of %s at %s read %d, which was never written", g.Op, g.K, g.Sent.Format("15:04:05.000"), g.V)
		}
	}
	r.Summary = fmt.Sprintf("%d reads (%d final): %d stale, %d phantom; TTL keys: %d served after expiry, %d missing before it",
		reads, finals, stale, phantom, expiredSeen, lostTTL)
	if finals < len(kvKeys) {
		r.fail("only %d of %d final reads succeeded", finals, len(kvKeys))
	}
	return r
}
