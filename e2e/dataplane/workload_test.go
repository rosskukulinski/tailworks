//go:build e2edataplane

package dataplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/netd/nettest"
)

const (
	lockTTL     = 10 * time.Second
	lockName    = "job:dp"
	tokenStride = 1_000_000_000
)

// target is where the dp workload lives now, as mgmt last reported it.
type target struct {
	mu    sync.Mutex
	depID string
	d     deployment
	seen  map[string]string // every endpoint DNS name seen -> its host
}

func (tg *target) get() deployment {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	return tg.d
}

func (tg *target) set(d deployment) {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	tg.d = d
	tg.depID = d.DeploymentID
	if d.Endpoint.MagicDNS != "" {
		tg.seen[d.Endpoint.MagicDNS] = d.Host
	}
}

func (tg *target) endpoints() map[string]string {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	out := map[string]string{}
	for k, v := range tg.seen {
		out[k] = v
	}
	return out
}

// workload runs the client side: every loop tags its ops with a unique ID
// and records them in the history.
type workload struct {
	c       *cluster
	h       *history
	tg      *target
	cli     *http.Client // ops and probes
	recURL  string       // the recorder, for Pulse
	stop    chan struct{}
	wg      sync.WaitGroup
	seq     int
	seqMu   sync.Mutex
	lockers map[string]*http.Client
	store   *http.Client
}

func (w *workload) id(prefix string) string {
	w.seqMu.Lock()
	defer w.seqMu.Unlock()
	w.seq++
	return fmt.Sprintf("%s%06d", prefix, w.seq)
}

func (w *workload) every(d time.Duration, f func()) {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-t.C:
				f()
			}
		}
	}()
}

func getJSON(hc *http.Client, u string, out any) (int, error) {
	resp, err := hc.Get(u)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return resp.StatusCode, fmt.Errorf("%d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return 200, nil
}

func (w *workload) base() (string, string) {
	d := w.tg.get()
	return "http://" + d.Endpoint.MagicDNS, d.Host
}

func (w *workload) run(sendEvery time.Duration) {
	// Queue producer: one message per tick; every 7th is retried once, every
	// 23rd is poison (dead-lettered).
	n := 0
	w.every(sendEvery, func() {
		n++
		k := w.id("m")
		switch {
		case n%23 == 0:
			k += "-x"
		case n%7 == 0:
			k += "-r"
		}
		go func() {
			b, _ := w.base()
			o := sendOp{K: k, Sent: time.Now()}
			_, err := getJSON(w.cli, b+"/send?k="+url.QueryEscape(k), nil)
			o.Recv, o.OK = time.Now(), err == nil
			if err != nil {
				o.Err = err.Error()
			}
			w.h.send(o)
		}()
	})
	// DO register: one client, sequential increments and reads.
	i := 0
	w.every(250*time.Millisecond, func() {
		i++
		op := "incr"
		if i%3 == 0 {
			op = "get"
		}
		b, host := w.base()
		o := regOp{Op: op, Sent: time.Now(), Host: host}
		var r struct {
			V     int
			Epoch int64
		}
		_, err := getJSON(w.cli, b+"/reg/"+op, &r)
		o.Recv, o.OK, o.V, o.Epoch = time.Now(), err == nil, r.V, r.Epoch
		if err != nil {
			o.Err = err.Error()
		}
		w.h.reg(o)
	})
	// Probes: every endpoint ever seen, so a copy that should be fenced is
	// caught answering.
	w.every(500*time.Millisecond, func() {
		for dns, host := range w.tg.endpoints() {
			go func() {
				o := regOp{Op: "probe", Sent: time.Now(), Host: host}
				var r struct {
					V     int
					Epoch int64
				}
				_, err := getJSON(w.cli, "http://"+dns+"/reg/get", &r)
				o.Recv, o.OK, o.V, o.Epoch = time.Now(), err == nil, r.V, r.Epoch
				if err != nil {
					o.Err = err.Error()
				}
				w.h.reg(o)
			}()
		}
	})
	// D1: inserts with client IDs.
	w.every(300*time.Millisecond, func() {
		id := w.id("d")
		b, _ := w.base()
		o := d1Op{ID: id, Sent: time.Now()}
		_, err := getJSON(w.cli, b+"/d1/add?id="+id, nil)
		o.Recv, o.OK = time.Now(), err == nil
		if err != nil {
			o.Err = err.Error()
		}
		w.h.d1(o)
	})
	// Pulse: started on every new epoch's copy.
	var pulsedEpoch int64
	w.every(500*time.Millisecond, func() {
		d := w.tg.get()
		if d.Epoch == pulsedEpoch || d.Endpoint.MagicDNS == "" {
			return
		}
		if _, err := getJSON(w.cli, "http://"+d.Endpoint.MagicDNS+"/pulse/start?url="+url.QueryEscape(w.recURL+"/pulse"), nil); err == nil {
			pulsedEpoch = d.Epoch
		}
	})
	for name, hc := range w.lockers {
		w.runLocker(name, hc)
	}
}

type lockReply struct {
	Error string `json:"error"`
	Token int64  `json:"token"`
	Epoch int64  `json:"epoch"`
}

func lockCall(hc *http.Client, base, op string, body map[string]any) (int, lockReply, error) {
	b, _ := json.Marshal(body)
	resp, err := hc.Post(base+"/locks/"+lockName+"/"+op, "application/json", bytes.NewReader(b))
	if err != nil {
		return 0, lockReply{}, err
	}
	defer resp.Body.Close()
	var r lockReply
	json.NewDecoder(resp.Body).Decode(&r)
	return resp.StatusCode, r, nil
}

// runLocker is one lock client: acquire, renew every 3 s, release after
// holding ~12 s, and write to the fenced store with its token after every
// grant and renew.
func (w *workload) runLocker(name string, hc *http.Client) {
	var (
		holding  bool
		token    int64
		epoch    int64
		granted  time.Time
		lastSent time.Time // last successful acquire/renew, as sent
	)
	write := func(kind string) {
		rq, _ := http.NewRequest("POST", "http://dp-store/write?resource="+lockName, nil)
		rq.Header.Set("Fencing-Token", strconv.FormatInt(token, 10))
		e := lockEv{Client: name, Kind: kind, Token: token, Epoch: epoch, Sent: time.Now()}
		resp, err := w.store.Do(rq)
		e.Recv = time.Now()
		if err != nil {
			e.Err = err.Error()
		} else {
			e.Status = resp.StatusCode
			resp.Body.Close()
		}
		w.h.lock(e)
	}
	w.every(time.Second, func() {
		b, host := w.base()
		now := time.Now()
		switch {
		case !holding:
			st, r, err := lockCall(hc, b, "acquire", map[string]any{"ttlMs": lockTTL.Milliseconds()})
			if err != nil || st != 200 {
				return
			}
			holding, token, epoch, granted, lastSent = true, r.Token, r.Epoch, now, now
			w.h.lock(lockEv{Client: name, Kind: "grant", Token: token, Epoch: epoch, Host: host, Sent: now, Recv: time.Now(), Status: st})
			write("write")
		case now.Sub(granted) > 12*time.Second:
			st, _, err := lockCall(hc, b, "release", map[string]any{"token": token})
			e := lockEv{Client: name, Kind: "release", Token: token, Epoch: epoch, Host: host, Sent: now, Recv: time.Now(), Status: st}
			if err != nil {
				e.Err = err.Error()
			}
			w.h.lock(e)
			// Whatever the answer, stop believing we hold it; after a
			// failed release the lease simply runs out.
			holding = false
			time.Sleep(1500 * time.Millisecond) // let the other client in
		case now.Sub(lastSent) >= 3*time.Second:
			st, _, err := lockCall(hc, b, "renew", map[string]any{"token": token, "ttlMs": lockTTL.Milliseconds()})
			e := lockEv{Client: name, Kind: "renew", Token: token, Epoch: epoch, Host: host, Sent: now, Recv: time.Now(), Status: st}
			if err != nil {
				e.Err = err.Error()
			}
			switch {
			case err == nil && st == 200:
				lastSent = now
				w.h.lock(e)
				write("write")
			case err == nil && st == 409:
				e.Kind = "lost"
				w.h.lock(e)
				holding = false
				write("stale-write") // must be refused once a newer token wrote
			default:
				w.h.lock(e)
				if time.Since(lastSent) > lockTTL {
					holding = false
				}
			}
		}
	})
}

// watch polls mgmt for the latest deployment and records every change of
// host or epoch.
func (w *workload) watch() {
	var last deployment
	w.every(300*time.Millisecond, func() {
		w.tg.mu.Lock()
		id := w.tg.depID
		w.tg.mu.Unlock()
		var d deployment
		if st, _, err := w.c.try("GET", "/v1/deployments/"+id, nil, &d); err != nil || st != 200 {
			return
		}
		if d.Status != "active" || d.Endpoint.Status != "ready" {
			return
		}
		w.tg.mu.Lock()
		if w.tg.depID == id {
			w.tg.d = d
			w.tg.seen[d.Endpoint.MagicDNS] = d.Host
		}
		w.tg.mu.Unlock()
		if d.Host != last.Host || d.Epoch != last.Epoch {
			w.h.dep(depEv{At: time.Now(), Host: d.Host, Epoch: d.Epoch, DNS: d.Endpoint.MagicDNS})
			last = d
		}
	})
}

// recorder is the tailnet node the consumer and Pulse report to.
func serveRecorder(n *nettest.Node, h *history) error {
	ln, err := n.Listen("tcp", ":80")
	if err != nil {
		return err
	}
	go http.Serve(ln, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		at := time.Now()
		node := ""
		if who, err := n.LC.WhoIs(r.Context(), r.RemoteAddr); err == nil {
			node = who.Node.Name
		}
		switch r.URL.Path {
		case "/q":
			var recs []struct {
				Q, K, ID string
				Attempts int
			}
			if err := json.NewDecoder(r.Body).Decode(&recs); err != nil {
				rw.WriteHeader(400)
				return
			}
			for _, x := range recs {
				h.del(delivery{Q: x.Q, K: x.K, ID: x.ID, Attempts: x.Attempts, At: at, Node: node})
			}
			rw.WriteHeader(204)
		case "/pulse":
			ep, _ := strconv.ParseInt(r.URL.Query().Get("epoch"), 10, 64)
			h.pulse(pulseEv{At: at, Node: node, Epoch: ep})
			rw.WriteHeader(204)
		default:
			rw.WriteHeader(404)
		}
	}))
	return nil
}
