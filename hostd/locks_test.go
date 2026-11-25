package hostd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// tokenStride matches TOKEN_STRIDE in the lock template (src/lock.ts).
const tokenStride = 1_000_000_000

type lockClientNode struct {
	name, id string
	n        *nettest.Node
	hc       *http.Client
}

type lockReply struct {
	Status     int
	Error      string `json:"error"`
	Token      int64  `json:"token"`
	TTLMs      int64  `json:"ttlMs"`
	ExpiresAt  int64  `json:"expiresAt"`
	Held       bool   `json:"held"`
	LastToken  int64  `json:"lastToken"`
	Epoch      int64  `json:"epoch"`
	Why        string `json:"why"`
	RetryAfter int64  `json:"retryAfterMs"`
	Holder     *struct {
		NodeID string   `json:"nodeId"`
		Node   string   `json:"node"`
		Tags   []string `json:"tags"`
	} `json:"holder"`

	// Raw is the body of an answer that is not the lock template's own
	// (no "error" field: a tailworks error from the platform, hostd or
	// netd), so a failure shows which layer answered (#136).
	Raw string `json:"-"`
}

func (c *lockClientNode) lock(t *testing.T, base, name, op string, body map[string]any) lockReply {
	t.Helper()
	u := base + "/locks/" + url.PathEscape(name)
	var req *http.Request
	if op == "status" {
		req, _ = http.NewRequest("GET", u, nil)
	} else {
		b, _ := json.Marshal(body)
		req, _ = http.NewRequest("POST", u+"/"+op, strings.NewReader(string(b)))
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatalf("%s %s %s: %v", c.name, op, name, err)
	}
	defer resp.Body.Close()
	var r lockReply
	b, _ := io.ReadAll(resp.Body)
	json.Unmarshal(b, &r)
	r.Status = resp.StatusCode
	if r.Status != 200 && r.Error == "" {
		r.Raw = resp.Header.Get("Tw-Error") + " " + string(b)
	}
	return r
}

// TestLockTemplate covers the lock template (js/packages/create-tailworks/templates/lock)
// on tw-hostd + stock workerd: contended acquire from several tailnet
// nodes, expiry by alarm, holder-bound renew/release, fencing tokens that
// carry the lease epoch, `tw kill` as an emergency release, and the
// template's client (withLock) against an external store that checks the
// token. docs/locks.md cites these subtests.
func TestLockTemplate(t *testing.T) {
	t.Parallel()
	// lockuser (a workload) calls the lock workload over the tailnet on the
	// same host: netd's in-process hop (samehost.go), no hairpin flag.
	s := startSuiteCapsOpts(t, fakemgmt.Config{}, tailcfg.PeerCapMap{nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue}}, nil)
	e := s.e
	ctx := context.Background()

	// Three more client nodes and the fenced store, concurrently.
	clients := []*lockClientNode{{name: "ttest-client", id: s.clientID, n: s.client, hc: s.hc}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var store *nettest.Node
	errs := make(chan error, 4)
	for _, hn := range []string{"ttest-c2", "ttest-c3", "ttest-c4", "ttest-store"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := nettest.NewNode(ctx, s.ctl.URL, nettest.DummyKey, hn, filepath.Join(e.Dir, hn))
			if err != nil {
				errs <- err
				return
			}
			t.Cleanup(func() { n.Close() })
			mu.Lock()
			defer mu.Unlock()
			if hn == "ttest-store" {
				store = n
				return
			}
			st, _ := n.LC.StatusWithoutPeers(ctx)
			clients = append(clients, &lockClientNode{name: hn, id: string(st.Self.ID), n: n, hc: n.HTTPClient(30 * time.Second)})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	fs, err := hostdtest.ServeFencedStore(store)
	if err != nil {
		t.Fatal(err)
	}

	locks := e.WaitActive("locks", e.Deploy(hostdtest.Template("lock"), "locks", nil), 60*time.Second)
	user := e.WaitActive("lockuser", e.Deploy(hostdtest.Fixture("lockuser"), "lockuser",
		map[string]string{"LOCKS_URL": "http://" + locks.Hostname, "STORE_URL": "http://ttest-store"}), 60*time.Second)
	for _, c := range clients {
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := c.n.WaitPeers(wctx, locks.IP, user.IP); err != nil {
			t.Fatal(err)
		}
		cancel()
	}
	base := "http://" + locks.IP
	epoch := func() int64 {
		for _, w := range e.Host.Status().Workloads {
			if w.Workload == "locks" {
				return w.Epoch
			}
		}
		t.Fatal("no locks workload")
		return 0
	}
	c1, c2 := clients[0], clients[1]
	userGet := func(t *testing.T, path string) map[string]any {
		t.Helper()
		code, m := s.getJSON("http://" + user.IP + path)
		if code != 200 {
			t.Fatalf("lockuser %s: %d %v", path, code, m)
		}
		return m
	}

	t.Run("ContendedAcquireOneWinner", func(t *testing.T) {
		// 4 tailnet nodes × 5 concurrent attempts on one fresh lock name.
		type res struct {
			c *lockClientNode
			r lockReply
		}
		out := make(chan res, 20)
		var start sync.WaitGroup
		start.Add(1)
		var all sync.WaitGroup
		for _, c := range clients {
			for range 5 {
				all.Add(1)
				go func() {
					defer all.Done()
					start.Wait()
					out <- res{c, c.lock(t, base, "job:contended", "acquire", map[string]any{"ttlMs": 20000})}
				}()
			}
		}
		start.Done()
		all.Wait()
		close(out)
		var winners []res
		held := 0
		for r := range out {
			switch {
			case r.r.Status == 200:
				winners = append(winners, r)
			case r.r.Status == 409 && r.r.Error == "lock_held":
				held++
			default:
				t.Fatalf("%s: unexpected reply %+v", r.c.name, r.r)
			}
		}
		if len(winners) != 1 || held != 19 {
			t.Fatalf("winners %d, lock_held %d; want exactly 1 and 19", len(winners), held)
		}
		w := winners[0]
		if w.r.TTLMs != 10000 {
			t.Fatalf("asked for 20 s, got a %d ms lease: want it capped at MAX_TTL_MS (10000)", w.r.TTLMs)
		}
		if w.r.Holder == nil || w.r.Holder.NodeID != w.c.id {
			t.Fatalf("winner %s (%s): holder %+v", w.c.name, w.c.id, w.r.Holder)
		}
		ep := epoch()
		if w.r.Epoch != ep || w.r.Token < ep*tokenStride+1 || w.r.Token >= (ep+1)*tokenStride {
			t.Fatalf("token %d at epoch %d (self().epoch %d): not in the epoch's range", w.r.Token, ep, w.r.Epoch)
		}
		st := c2.lock(t, base, "job:contended", "status", nil)
		if !st.Held || st.Holder.NodeID != w.c.id || st.LastToken != w.r.Token {
			t.Fatalf("status %+v", st)
		}
		t.Logf("winner %s (holder %s) token %d (epoch %d); 19 × lock_held", w.c.name, w.r.Holder.Node, w.r.Token, ep)
		// `tw audit <lock>` shows who took it.
		o, ok := e.Host.Object(e.TenantID, "locks", "Lock", "job:contended")
		if !ok {
			t.Fatal("no lock object")
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			b, _ := json.Marshal(e.Audit(o.ID))
			if strings.Contains(string(b), "lock_acquired") && strings.Contains(string(b), w.c.id) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("no lock_acquired audit note naming %s: %s", w.c.id, hostdtest.Trim(string(b), 2000))
			}
			time.Sleep(200 * time.Millisecond)
		}
	})

	t.Run("RenewAndReleaseOnlyByHolder", func(t *testing.T) {
		a := c1.lock(t, base, "job:renew", "acquire", map[string]any{"ttlMs": 2000})
		if a.Status != 200 {
			t.Fatalf("acquire %+v", a)
		}
		time.Sleep(300 * time.Millisecond)
		r := c1.lock(t, base, "job:renew", "renew", map[string]any{"token": a.Token, "ttlMs": 2000})
		if r.Status != 200 || r.ExpiresAt <= a.ExpiresAt || r.Token != a.Token {
			t.Fatalf("renew %+v (acquired %+v)", r, a)
		}
		// The right token from another node: not the holder.
		if x := c2.lock(t, base, "job:renew", "renew", map[string]any{"token": a.Token}); x.Status != 403 || x.Error != "not_holder" {
			t.Fatalf("renew from another node: %+v", x)
		}
		if x := c2.lock(t, base, "job:renew", "release", map[string]any{"token": a.Token}); x.Status != 403 {
			t.Fatalf("release from another node: %+v", x)
		}
		if x := c1.lock(t, base, "job:renew", "renew", map[string]any{"token": a.Token - 1}); x.Status != 409 || x.Error != "lock_lost" {
			t.Fatalf("renew with a wrong token: %+v", x)
		}
		if x := c1.lock(t, base, "job:renew", "release", map[string]any{"token": a.Token}); x.Status != 200 || x.Held || x.Why != "released" {
			t.Fatalf("release %+v", x)
		}
		b := c2.lock(t, base, "job:renew", "acquire", map[string]any{"ttlMs": 2000})
		if b.Status != 200 || b.Token <= a.Token {
			t.Fatalf("acquire after release: %+v (previous token %d)", b, a.Token)
		}
		c2.lock(t, base, "job:renew", "release", map[string]any{"token": b.Token})
	})

	t.Run("ExpiresWithoutRenewal", func(t *testing.T) {
		a := c1.lock(t, base, "job:expiry", "acquire", map[string]any{"ttlMs": 1000})
		if a.Status != 200 {
			t.Fatalf("acquire %+v", a)
		}
		if x := c2.lock(t, base, "job:expiry", "acquire", nil); x.Status != 409 || x.RetryAfter <= 0 || x.RetryAfter > 1000 {
			t.Fatalf("contender before expiry: %+v", x)
		}
		// The alarm frees it; nobody has to ask first: with no request to the
		// lock at all, its lock_expired note shows up in the audit.
		o, _ := e.Host.Object(e.TenantID, "locks", "Lock", "job:expiry")
		st := time.Now()
		for {
			b, _ := json.Marshal(e.Audit(o.ID))
			if strings.Contains(string(b), "lock_expired") {
				break
			}
			if time.Since(st) > 10*time.Second {
				t.Fatalf("no lock_expired note 10 s after a 1 s lease (alarm didn't fire?)")
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Logf("alarm freed the 1000 ms lease; audit note seen %v after acquire returned", time.Since(st).Round(time.Millisecond))
		for {
			x := c2.lock(t, base, "job:expiry", "status", nil)
			if !x.Held {
				if x.Why != "expired" {
					t.Fatalf("freed but why=%q: %+v", x.Why, x)
				}
				break
			}
			if time.Since(st) > 5*time.Second {
				t.Fatalf("still held 5 s after a 1 s lease: %+v", x)
			}
			time.Sleep(50 * time.Millisecond)
		}
		if x := c1.lock(t, base, "job:expiry", "renew", map[string]any{"token": a.Token}); x.Status != 409 || x.Error != "lock_lost" {
			t.Fatalf("renew after expiry: %+v", x)
		}
		b := c2.lock(t, base, "job:expiry", "acquire", map[string]any{"ttlMs": 1000})
		if b.Status != 200 || b.Token <= a.Token {
			t.Fatalf("acquire after expiry %+v (previous %d)", b, a.Token)
		}
	})

	t.Run("TokensStrictlyIncrease", func(t *testing.T) {
		var last int64
		for i := range 8 {
			c := clients[i%len(clients)]
			a := c.lock(t, base, "job:seq", "acquire", map[string]any{"ttlMs": 5000})
			if a.Status != 200 || a.Token <= last {
				t.Fatalf("round %d (%s): %+v after token %d", i, c.name, a, last)
			}
			last = a.Token
			if r := c.lock(t, base, "job:seq", "release", map[string]any{"token": a.Token}); r.Status != 200 {
				t.Fatalf("release %+v", r)
			}
		}
	})

	t.Run("ClientWithLockRenewsAndFences", func(t *testing.T) {
		// withLock holds a 3 s lease for 4 s: it must renew (every 1 s), and
		// a contender meanwhile gets lock_held.
		//
		// The lease is 3 s, not 1 s (#196, as KillHolderReleases in #136):
		// with 1 s renewed every 333 ms, a loaded machine under -race let a
		// renewal or the release land after the deadline, and the lock
		// rightly showed "expired" instead of "released".
		type withReply struct {
			code int
			m    map[string]any
		}
		done := make(chan withReply, 1)
		go func() {
			// Not userGet: its t.Fatalf, after this subtest has already
			// failed below, would panic the whole test binary.
			code, m := s.getJSON("http://" + user.IP + "/with?name=job:report&hold=4000&ttl=3000")
			done <- withReply{code, m}
		}()
		var first lockReply
		for i := 0; ; i++ {
			if first = c2.lock(t, base, "job:report", "status", nil); first.Held {
				break
			}
			if i > 100 {
				t.Fatalf("lockuser never took job:report: %+v", first)
			}
			time.Sleep(50 * time.Millisecond)
		}
		// Wait until withLock has renewed (the deadline moved), rather than
		// sleeping past the first lease.
		for i := 0; ; i++ {
			x := c2.lock(t, base, "job:report", "status", nil)
			if !x.Held || x.LastToken != first.LastToken {
				t.Fatalf("withLock lost job:report while holding it: %+v (took it as %+v)", x, first)
			}
			if x.ExpiresAt > first.ExpiresAt {
				break
			}
			if i > 200 {
				t.Fatalf("withLock never renewed job:report: %+v", x)
			}
			time.Sleep(50 * time.Millisecond)
		}
		if x := c2.lock(t, base, "job:report", "acquire", nil); x.Status != 409 || x.Holder == nil || !strings.HasPrefix(x.Holder.Node, user.Hostname+".") {
			t.Fatalf("contender while withLock holds (after a renewal): %+v", x)
		}
		r := <-done
		if r.code != 200 {
			t.Fatalf("lockuser /with: %d %v", r.code, r.m)
		}
		m := r.m
		if m["ok"] != true || m["aborted"] != false || m["renewedByMs"].(float64) < 1000 {
			t.Fatalf("withLock: %v", m)
		}
		for _, w := range m["writes"].([]any) {
			if w.(map[string]any)["status"].(float64) != 200 {
				t.Fatalf("fenced writes: %v", m["writes"])
			}
		}
		// Released afterwards.
		if x := c2.lock(t, base, "job:report", "status", nil); x.Held || x.Why != "released" {
			t.Fatalf("after withLock: %+v", x)
		}

		// A stale holder: lockuser takes a 500 ms lease and stops renewing.
		a := userGet(t, "/acquire?name=job:fence&ttl=500")
		stale := int64(a["lease"].(map[string]any)["token"].(float64))
		if w := userGet(t, fmt.Sprintf("/write?name=job:fence&token=%d", stale)); w["status"].(float64) != 200 {
			t.Fatalf("holder's write: %v", w)
		}
		var b lockReply
		for i := 0; ; i++ {
			if b = c2.lock(t, base, "job:fence", "acquire", map[string]any{"ttlMs": 5000}); b.Status == 200 {
				break
			}
			if i > 100 {
				t.Fatalf("c2 never got job:fence: %+v", b)
			}
			time.Sleep(50 * time.Millisecond)
		}
		if b.Token <= stale {
			t.Fatalf("new token %d ≤ stale %d", b.Token, stale)
		}
		rq, _ := http.NewRequest("POST", "http://ttest-store/write?resource=job:fence", nil)
		rq.Header.Set("Fencing-Token", fmt.Sprint(b.Token))
		resp, err := c2.hc.Do(rq)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("new holder's write: %v %v", resp, err)
		}
		resp.Body.Close()
		// The stale holder wakes up and writes: the store refuses it.
		w := userGet(t, fmt.Sprintf("/write?name=job:fence&token=%d", stale))
		if w["status"].(float64) != 409 || get(w, "body", "error") != "stale_token" {
			t.Fatalf("stale holder's late write: %v", w)
		}
		if _, r := s.getJSON("http://" + user.IP + fmt.Sprintf("/renew?name=job:fence&token=%d", stale)); r["ok"] != false || r["code"] != "lock_lost" {
			t.Fatalf("stale holder's renew: %v", r)
		}
		if fs.Max("job:fence") != b.Token {
			t.Fatalf("store max %d, want %d", fs.Max("job:fence"), b.Token)
		}
	})

	t.Run("KillHolderReleases", func(t *testing.T) {
		// The holder is lockuser's workload node, inside withLock with a 3 s
		// lease that it renews (every 1 s). Killing that node cuts it off
		// the tailnet: it can't renew, and the lease runs out.
		//
		// The lease is 3 s, not 1 s (#136): with 1 s, a loaded machine under
		// -race let a renewal miss the deadline, and the contender below
		// legitimately took the lock before the kill.
		done := make(chan struct{})
		go func() {
			defer close(done)
			qc := s.client.HTTPClient(30 * time.Second)
			if r, err := qc.Get("http://" + user.IP + "/with?name=job:holder&hold=20000&ttl=3000"); err == nil {
				r.Body.Close()
			}
		}()
		var a lockReply
		for i := 0; ; i++ {
			if a = c2.lock(t, base, "job:holder", "status", nil); a.Held {
				break
			}
			if i > 100 {
				t.Fatalf("lockuser never took job:holder: %+v", a)
			}
			time.Sleep(50 * time.Millisecond)
		}
		// Wait until the holder has renewed (the deadline moved), rather
		// than sleeping past the first lease: then it is renewing.
		for i := 0; ; i++ {
			x := c2.lock(t, base, "job:holder", "status", nil)
			if !x.Held || x.LastToken != a.LastToken {
				t.Fatalf("holder lost job:holder before the kill: %+v (took it as %+v)", x, a)
			}
			if x.ExpiresAt > a.ExpiresAt {
				break
			}
			if i > 200 {
				t.Fatalf("holder never renewed job:holder: %+v", x)
			}
			time.Sleep(50 * time.Millisecond)
		}
		if x := c2.lock(t, base, "job:holder", "acquire", nil); x.Status != 409 {
			t.Fatalf("contender while the holder renews: %+v", x)
		}
		st := time.Now()
		if _, err := e.FM.Kill(ctx, user.ID, false, "emergency release (test)"); err != nil {
			t.Fatal(err)
		}
		var b lockReply
		for {
			if b = c2.lock(t, base, "job:holder", "acquire", map[string]any{"ttlMs": 5000}); b.Status == 200 {
				break
			}
			if time.Since(st) > 10*time.Second {
				t.Fatalf("lock still held 10 s after killing its holder: %+v", b)
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Logf("killed the holder (3 s lease): lock taken by another node %v later", time.Since(st).Round(10*time.Millisecond))
		if b.Token <= a.LastToken {
			t.Fatalf("token %d after %d", b.Token, a.LastToken)
		}
		<-done
	})

	t.Run("KillLockObjectStopsIt", func(t *testing.T) {
		// `tw kill` on the lock object itself is a kill switch, not a
		// release: the object keeps a killed marker in its storage, so the
		// name answers 410 killed for good. The holder's next renew fails and
		// nobody can take that name again.
		a := c1.lock(t, base, "job:kill", "acquire", map[string]any{"ttlMs": 10000})
		if a.Status != 200 {
			t.Fatalf("acquire %+v", a)
		}
		o, ok := e.Host.Object(e.TenantID, "locks", "Lock", "job:kill")
		if !ok {
			t.Fatal("no lock object")
		}
		if _, err := e.FM.Kill(ctx, o.ID, false, "stop job:kill (test)"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(6 * time.Second) // past the suite's 5 s kill tombstone
		if r := c1.lock(t, base, "job:kill", "renew", map[string]any{"token": a.Token}); r.Status != 410 {
			t.Fatalf("holder's renew after the kill: %+v", r)
		}
		if r := c2.lock(t, base, "job:kill", "acquire", nil); r.Status != 410 {
			t.Fatalf("acquire after the kill: %+v", r)
		}
		// Other lock names are unaffected.
		if r := c2.lock(t, base, "job:kill2", "acquire", nil); r.Status != 200 {
			t.Fatalf("another lock after the kill: %+v", r)
		}
	})

	// Last: it redeploys the lock service.
	t.Run("RedeployKeepsHolderTokenCarriesEpoch", func(t *testing.T) {
		a := c1.lock(t, base, "job:redeploy", "acquire", map[string]any{"ttlMs": 10000})
		ep := epoch()
		if a.Status != 200 || a.Epoch != ep {
			t.Fatalf("acquire %+v (epoch %d)", a, ep)
		}
		e.WaitActive("locks", e.Deploy(hostdtest.Template("lock"), "locks", nil), 60*time.Second)
		if epoch() <= ep {
			t.Fatalf("epoch %d after redeploy, was %d", epoch(), ep)
		}
		// Same host: storage stays, and so does the holder.
		var r lockReply
		for i := 0; ; i++ {
			if r = c1.lock(t, base, "job:redeploy", "renew", map[string]any{"token": a.Token, "ttlMs": 10000}); r.Status == 200 {
				break
			}
			if i > 100 {
				t.Fatalf("holder's renew after redeploy: %+v", r)
			}
			time.Sleep(100 * time.Millisecond)
		}
		c1.lock(t, base, "job:redeploy", "release", map[string]any{"token": a.Token})
		b := c2.lock(t, base, "job:redeploy", "acquire", nil)
		if b.Status != 200 || b.Epoch != epoch() || b.Token != epoch()*tokenStride+1 {
			t.Fatalf("acquire after redeploy %+v, want token %d", b, epoch()*tokenStride+1)
		}
		t.Logf("epoch %d → %d; token %d → %d", ep, b.Epoch, a.Token, b.Token)
	})
}
