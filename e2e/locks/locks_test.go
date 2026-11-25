//go:build e2elocks

// Package locks is the hermetic multi-host e2e for docs/locks.md: real
// tw-mgmt and two real tw-hostd processes on testcontrol + a local DERP,
// with the default timings (heartbeat 3 s, FenceAfter 30 s, namespace lease
// 45 s). h1's only path to mgmt goes through a proxy the test can cut, so
// h1 is partitioned from mgmt while it stays on the tailnet and keeps
// answering clients: the split-brain case. The workload is the lock
// template's service plus Pulse, a Durable Object whose alarm makes an
// egress call every 250 ms, so the test sees which host's copy acts.
// Nothing talks to the internet; no real keys.
package locks

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

const (
	fakeToken   = "fake-api-token"
	tokenStride = 1_000_000_000 // TOKEN_STRIDE in the lock template
	lockTTL     = 10 * time.Second
)

func fakeTS(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/oauth/token" { // the OAuth client's exchange (mint mode oauth)
			json.NewEncoder(w).Encode(map[string]any{"access_token": fakeToken, "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != fakeToken {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/keys"):
			mu.Lock()
			n++
			id := fmt.Sprintf("k%06d", n)
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"id": id, "key": "fake-authkey-" + id})
		case r.Method == "DELETE":
			w.WriteHeader(200)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/acl"):
			w.Header().Set("ETag", `"p1"`)
			w.Write([]byte(`{"tagOwners": {"tag:ttest": ["autogroup:admin"]}}`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/devices"):
			json.NewEncoder(w).Encode(map[string]any{"devices": []any{}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// sockProxy forwards a unix socket to mgmt's. cut(true) drops every
// connection and refuses new ones: the host can no longer reach mgmt.
type sockProxy struct {
	path, target string
	mu           sync.Mutex
	cutOff       bool
	conns        map[net.Conn]bool
}

func startProxy(t *testing.T, path, target string) *sockProxy {
	p := &sockProxy{path: path, target: target, conns: map[net.Conn]bool{}}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			if p.cutOff {
				p.mu.Unlock()
				c.Close()
				continue
			}
			u, err := net.Dial("unix", target)
			if err != nil {
				p.mu.Unlock()
				c.Close()
				continue
			}
			p.conns[c], p.conns[u] = true, true
			p.mu.Unlock()
			go func() { io.Copy(u, c); u.Close() }()
			go func() { io.Copy(c, u); c.Close() }()
		}
	}()
	return p
}

func (p *sockProxy) cut(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cutOff = on
	if on {
		for c := range p.conns {
			c.Close()
		}
		p.conns = map[net.Conn]bool{}
	}
}

type proc struct {
	cmd  *exec.Cmd
	done chan struct{}
}

type env struct {
	t        *testing.T
	bin, dir string
	ctl      *nettest.Control
	sock     string
	token    string
	hc       *http.Client
	workerd  string
	mu       sync.Mutex
	procs    map[string]*proc
}

func (e *env) start(name, bin string, args ...string) {
	e.t.Helper()
	lf, err := os.OpenFile(filepath.Join(e.dir, name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		e.t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(e.bin, bin), args...)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	p := &proc{cmd: cmd, done: make(chan struct{})}
	go func() { cmd.Wait(); lf.Close(); close(p.done) }()
	e.mu.Lock()
	e.procs[name] = p
	e.mu.Unlock()
}

func (e *env) stopAll() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range e.procs {
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	}
	for _, p := range e.procs {
		select {
		case <-p.done:
		case <-time.After(15 * time.Second):
			syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			<-p.done
		}
	}
}

func (e *env) dumpLogs() {
	for _, name := range []string{"mgmt", "h1", "h2"} {
		b, _ := os.ReadFile(filepath.Join(e.dir, name+".log"))
		lines := strings.Split(string(b), "\n")
		if len(lines) > 60 {
			lines = lines[len(lines)-60:]
		}
		e.t.Logf("---- %s.log (tail)\n%s", name, strings.Join(lines, "\n"))
	}
}

func (e *env) do(method, path string, body, out any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	rq, _ := http.NewRequest(method, "http://mgmt"+path, rd)
	rq.Header.Set(mgmt.HostTokenHeader, e.token)
	rq.Header.Set("content-type", "application/json")
	resp, err := e.hc.Do(rq)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode/100 == 2 {
		json.Unmarshal(b, out)
	}
	return resp.StatusCode, b
}

type deployment struct {
	DeploymentID, Host, Status, Error string
	Epoch                             int64
	Endpoint                          struct{ MagicDNS, Status string }
}

func (e *env) waitActive(id string, timeout time.Duration) deployment {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var d deployment
		if st, b := e.do("GET", "/v1/deployments/"+id, nil, &d); st != 200 {
			e.t.Fatalf("deployment %s: %d %s", id, st, b)
		}
		if d.Status == "active" && d.Endpoint.Status == "ready" && strings.HasPrefix(d.Endpoint.MagicDNS, d.Host+"-") {
			return d
		}
		if d.Status == "failed" || time.Now().After(deadline) {
			e.dumpLogs()
			e.t.Fatalf("deployment %s: status %s endpoint %s: %s", id, d.Status, d.Endpoint.Status, d.Error)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (e *env) hostWorkloads(id string) map[string]string {
	sock := filepath.Join(e.dir, id, "run", "hostd.sock")
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, err := hc.Get("http://hostd/v1/status")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var st struct {
		Workloads []struct{ Workload, Status string }
	}
	json.NewDecoder(resp.Body).Decode(&st)
	out := map[string]string{}
	for _, w := range st.Workloads {
		out[w.Workload] = w.Status
	}
	return out
}

type lockReply struct {
	Status int
	Error  string `json:"error"`
	Token  int64  `json:"token"`
	Epoch  int64  `json:"epoch"`
	Held   bool   `json:"held"`
	Holder *struct {
		NodeID string `json:"nodeId"`
		Node   string `json:"node"`
	} `json:"holder"`
}

// lockCall sends one lock op; err is a transport failure (host unreachable).
func lockCall(hc *http.Client, base, name, op string, body map[string]any) (lockReply, error) {
	var rq *http.Request
	if op == "status" {
		rq, _ = http.NewRequest("GET", base+"/locks/"+name, nil)
	} else {
		b, _ := json.Marshal(body)
		rq, _ = http.NewRequest("POST", base+"/locks/"+name+"/"+op, bytes.NewReader(b))
	}
	resp, err := hc.Do(rq)
	if err != nil {
		return lockReply{}, err
	}
	defer resp.Body.Close()
	var r lockReply
	json.NewDecoder(resp.Body).Decode(&r)
	r.Status = resp.StatusCode
	return r, nil
}

type pulse struct {
	at    time.Time
	node  string
	epoch int64
}

// TestLocksFailover answers docs/locks.md's questions with measurements:
// while h1 is cut off from mgmt but still on the tailnet, does it stop
// serving the lock (and stop its side effects) before h2 can start it?
// How long is the gap? Do fencing tokens still increase across the move,
// and does the example store refuse the old holder?
func TestLocksFailover(t *testing.T) {
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	if !hostdtest.HasNode() {
		t.Skip("node not available (needed to bundle the app)")
	}
	dir, err := hostdtest.ShortDir("tw-lk-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !t.Failed() || os.Getenv("TW_KEEP") == "" {
			os.RemoveAll(dir)
		}
	})
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	ts := fakeTS(t)
	e := &env{t: t, bin: cliflow.BuildBinaries(t), dir: dir, ctl: ctl, sock: filepath.Join(dir, "mgmt.sock"), workerd: wd, procs: map[string]*proc{}}
	t.Cleanup(e.stopAll)
	e.hc = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", e.sock)
	}}}
	ctx := context.Background()

	// Tailnet clients: holder A, contender B, the fenced store, the pulse recorder.
	nodes := map[string]*nettest.Node{}
	var nmu sync.Mutex
	var wg sync.WaitGroup
	for _, hn := range []string{"ttest-a", "ttest-b", "ttest-store", "ttest-rec"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := nettest.NewNode(ctx, ctl.URL, nettest.DummyKey, hn, filepath.Join(dir, hn))
			if err != nil {
				t.Error(err)
				return
			}
			t.Cleanup(func() { n.Close() })
			nmu.Lock()
			nodes[hn] = n
			nmu.Unlock()
		}()
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	fs, err := hostdtest.ServeFencedStore(nodes["ttest-store"])
	if err != nil {
		t.Fatal(err)
	}
	var pmu sync.Mutex
	var pulses []pulse
	rln, err := nodes["ttest-rec"].Listen("tcp", ":80")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(rln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, err := nodes["ttest-rec"].LC.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil {
			w.WriteHeader(403)
			return
		}
		ep, _ := strconv.ParseInt(r.URL.Query().Get("epoch"), 10, 64)
		pmu.Lock()
		pulses = append(pulses, pulse{time.Now(), who.Node.Name, ep})
		pmu.Unlock()
	}))
	pulsesFrom := func(host string) []pulse {
		pmu.Lock()
		defer pmu.Unlock()
		var out []pulse
		for _, p := range pulses {
			if strings.HasPrefix(p.node, host+"-") {
				out = append(out, p)
			}
		}
		return out
	}
	hcA, hcB := nodes["ttest-a"].HTTPClient(5*time.Second), nodes["ttest-b"].HTTPClient(5*time.Second)

	// mgmt (SQLite) and two hosts with default timings; each host reaches
	// mgmt through its own proxy.
	keyFile := filepath.Join(dir, "api.env")
	cliflow.WriteFakeOAuthFile(t, keyFile)
	e.start("mgmt", "tw-mgmt", "-local-dev", "-socket", e.sock, "-host-token-file", filepath.Join(dir, "token"), "-rotate-host-token",
		"-state-dir", filepath.Join(dir, "mgmt"), "-tsapi-url", ts.URL, "-reap-interval", "1h")
	for i := 0; ; i++ {
		b, err := os.ReadFile(filepath.Join(dir, "token"))
		if _, serr := os.Stat(e.sock); err == nil && len(bytes.TrimSpace(b)) == 64 && serr == nil {
			e.token = string(bytes.TrimSpace(b))
			break
		}
		if i > 100 {
			e.dumpLogs()
			t.Fatal("mgmt did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	var tn struct{ TenantID string }
	if st, b := e.do("POST", "/v1/tenants", map[string]any{"tailnet": nettest.MagicDNSDomain, "controlURL": ctl.URL, "mintMode": "oauth", "oauthClientFile": keyFile}, &tn); st/100 != 2 {
		t.Fatalf("tenant: %d %s", st, b)
	}
	proxies := map[string]*sockProxy{}
	for _, id := range []string{"h1", "h2"} {
		ps := filepath.Join(dir, id+"-mgmt.sock")
		proxies[id] = startProxy(t, ps, e.sock)
		hd := filepath.Join(dir, id)
		e.start(id, "tw-hostd", "-local-dev", "-mgmt", "unix://"+ps, "-mgmt-token-file", filepath.Join(dir, "token"),
			"-host-id", id, "-data-dir", filepath.Join(hd, "data"), "-run-dir", filepath.Join(hd, "run"),
			"-control-url", ctl.URL, "-ephemeral", "-sandbox", "none", "-workerd", wd, "-hostname-prefix", id+"-", "-log-level", "debug")
	}
	for i := 0; ; i++ {
		var pg struct {
			Items []struct{ HostID, Status string }
		}
		e.do("GET", "/v1/hosts", nil, &pg)
		live := 0
		for _, h := range pg.Items {
			if h.Status == "live" {
				live++
			}
		}
		if live == 2 {
			break
		}
		if i > 300 {
			e.dumpLogs()
			t.Fatalf("hosts not live: %+v", pg.Items)
		}
		time.Sleep(100 * time.Millisecond)
	}

	app, _ := filepath.Abs(filepath.Join("testdata", "lockpulse"))
	files, err := hostdtest.Build(app, nil)
	if err != nil {
		t.Fatal(err)
	}
	bundle := base64.StdEncoding.EncodeToString(hostdtest.MustTar(files))
	deploy := func(host string, force bool) (deployment, int, []byte) {
		var d deployment
		st, b := e.do("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "locks", "bundle": bundle, "host": host, "forceTakeover": force}, &d)
		return d, st, b
	}

	// ---- On h1: A takes the lock and writes with its token; Pulse runs.
	d1, st, b := deploy("h1", false)
	if st != 202 {
		t.Fatalf("deploy to h1: %d %s", st, b)
	}
	d1 = e.waitActive(d1.DeploymentID, 90*time.Second)
	url1 := "http://" + d1.Endpoint.MagicDNS
	var a lockReply
	for i := 0; ; i++ {
		if a, err = lockCall(hcA, url1, "job:failover", "acquire", map[string]any{"ttlMs": lockTTL.Milliseconds()}); err == nil && a.Status == 200 {
			break
		}
		if i > 100 {
			t.Fatalf("A acquire on h1: %+v %v", a, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if a.Epoch != d1.Epoch || a.Token != d1.Epoch*tokenStride+1 {
		t.Fatalf("A's token %d at epoch %d; deployment epoch %d", a.Token, a.Epoch, d1.Epoch)
	}
	write := func(hc *http.Client, token int64) int {
		rq, _ := http.NewRequest("POST", "http://ttest-store/write?resource=job:failover", nil)
		rq.Header.Set("Fencing-Token", fmt.Sprint(token))
		resp, err := hc.Do(rq)
		if err != nil {
			t.Fatalf("store write: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := write(hcA, a.Token); c != 200 {
		t.Fatalf("A's first write: %d", c)
	}
	if r, err := hcA.Get(url1 + "/pulse/start?url=http://ttest-rec/pulse"); err != nil || r.StatusCode != 200 {
		t.Fatalf("pulse start on h1: %v %v", r, err)
	}
	for i := 0; len(pulsesFrom("h1")) < 4; i++ {
		if i > 100 {
			t.Fatal("no pulses from h1")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// A renews every 3 s, wherever the lock lives (it records when each
	// successful renew was sent: its lease runs until then + TTL).
	var amu sync.Mutex
	var aLastRenewSent time.Time
	aURL := url1
	stopA := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopA:
				return
			case <-time.After(3 * time.Second):
			}
			amu.Lock()
			u := aURL
			amu.Unlock()
			sent := time.Now()
			if r, err := lockCall(hcA, u, "job:failover", "renew", map[string]any{"token": a.Token, "ttlMs": lockTTL.Milliseconds()}); err == nil && r.Status == 200 {
				amu.Lock()
				aLastRenewSent = sent
				amu.Unlock()
			}
		}
	}()
	defer close(stopA)

	// Watch h1 serve the lock (B's status reads).
	var wmu sync.Mutex
	var h1LastServed time.Time
	stopW := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopW:
				return
			case <-time.After(200 * time.Millisecond):
			}
			if r, err := lockCall(hcB, url1, "job:failover", "status", nil); err == nil && r.Status == 200 {
				wmu.Lock()
				h1LastServed = time.Now()
				wmu.Unlock()
			}
		}
	}()

	// ---- Partition h1 from mgmt. It stays on the tailnet.
	time.Sleep(2 * time.Second)
	t0 := time.Now()
	proxies["h1"].cut(true)
	t.Logf("t0: h1 cut off from mgmt (still on the tailnet)")

	// Take over on h2 as soon as mgmt allows it.
	var d2 deployment
	codes := map[string]int{}
	var firstRefusal string
	for {
		d, st, b := deploy("h2", true)
		if st == 202 {
			d2 = d
			break
		}
		var er struct{ Code, Message string }
		json.Unmarshal(b, &er)
		codes[fmt.Sprintf("%d %s", st, er.Code)]++
		if firstRefusal == "" {
			firstRefusal = er.Message
		}
		if time.Since(t0) > 90*time.Second {
			e.dumpLogs()
			t.Fatalf("no takeover 90 s after the partition: %v", codes)
		}
		time.Sleep(500 * time.Millisecond)
	}
	tTakeover := time.Since(t0)
	t.Logf("takeover deploy accepted %.1fs after t0 (refused before: %v; first: %q)", tTakeover.Seconds(), codes, firstRefusal)
	d2 = e.waitActive(d2.DeploymentID, 90*time.Second)
	url2 := "http://" + d2.Endpoint.MagicDNS
	if d2.Epoch <= d1.Epoch {
		t.Fatalf("epoch %d after the move, was %d", d2.Epoch, d1.Epoch)
	}
	amu.Lock()
	aURL = url2
	amu.Unlock()

	// B takes the lock on h2: the object starts with empty storage there,
	// but the token still beats A's (the epoch moved).
	var bl lockReply
	var tB time.Time
	for i := 0; ; i++ {
		if bl, err = lockCall(hcB, url2, "job:failover", "acquire", map[string]any{"ttlMs": lockTTL.Milliseconds()}); err == nil && bl.Status == 200 {
			tB = time.Now()
			break
		}
		if i > 200 {
			t.Fatalf("B acquire on h2: %+v %v", bl, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if bl.Token <= a.Token || bl.Token != d2.Epoch*tokenStride+1 {
		t.Fatalf("B's token %d (epoch %d) vs A's %d (epoch %d)", bl.Token, bl.Epoch, a.Token, a.Epoch)
	}
	if r, err := hcB.Get(url2 + "/pulse/start?url=http://ttest-rec/pulse"); err != nil || r.StatusCode != 200 {
		t.Fatalf("pulse start on h2: %v %v", r, err)
	}
	for i := 0; len(pulsesFrom("h2")) < 4; i++ {
		if i > 100 {
			t.Fatal("no pulses from h2")
		}
		time.Sleep(100 * time.Millisecond)
	}
	close(stopW)

	// The external store: B's write lands, A's (stale) is refused.
	if c := write(hcB, bl.Token); c != 200 {
		t.Fatalf("B's write: %d", c)
	}
	if c := write(hcA, a.Token); c != 409 {
		t.Fatalf("A's stale write after the failover: %d, want 409", c)
	}
	if r, err := lockCall(hcA, url2, "job:failover", "renew", map[string]any{"token": a.Token}); err != nil || r.Status != 409 || r.Error != "lock_lost" {
		t.Fatalf("A's renew on h2: %+v %v", r, err)
	}

	// ---- What was measured.
	wmu.Lock()
	lastServed := h1LastServed
	wmu.Unlock()
	amu.Lock()
	aDeadline := aLastRenewSent.Add(lockTTL)
	amu.Unlock()
	h1p, h2p := pulsesFrom("h1"), pulsesFrom("h2")
	lastH1, firstH2 := h1p[len(h1p)-1], h2p[0]
	rel := func(x time.Time) string { return fmt.Sprintf("t0+%.1fs", x.Sub(t0).Seconds()) }
	t.Logf("h1 last answered a lock request at %s; its last pulse (side effect) at %s (epoch %d)", rel(lastServed), rel(lastH1.at), lastH1.epoch)
	t.Logf("h2 first pulse at %s (epoch %d); B granted at %s; A's lease (last renew sent + %v) ended at %s",
		rel(firstH2.at), firstH2.epoch, rel(tB), lockTTL, rel(aDeadline))
	t.Logf("gap with no lock service: %.1fs; gap between the two copies' side effects: %.1fs",
		tB.Sub(lastServed).Seconds(), firstH2.at.Sub(lastH1.at).Seconds())
	t.Logf("tokens: A %d (epoch %d) → B %d (epoch %d); store refused A's late write", a.Token, a.Epoch, bl.Token, bl.Epoch)

	// No split brain: h1 stopped (fenced) before h2 could start.
	if !lastH1.at.Before(firstH2.at) {
		t.Fatalf("pulses overlap: h1's last %s, h2's first %s", rel(lastH1.at), rel(firstH2.at))
	}
	if !lastServed.Before(tB) {
		t.Fatalf("h1 answered at %s, after B was granted on h2 at %s", rel(lastServed), rel(tB))
	}
	if lastH1.at.Sub(t0) > 40*time.Second {
		t.Fatalf("h1 kept acting until %s: it didn't fence", rel(lastH1.at))
	}
	// No overlapping holders: A's lease, renewed through h1 until the
	// fence, ran out before B could be granted on h2.
	if !aDeadline.Before(tB) {
		t.Fatalf("A's lease ran until %s, B granted at %s: two holders", rel(aDeadline), rel(tB))
	}

	// ---- h1 comes back: it is revoked, not resumed.
	n1 := len(h1p)
	proxies["h1"].cut(false)
	tBack := time.Now()
	time.Sleep(12 * time.Second)
	if p := pulsesFrom("h1"); len(p) != n1 {
		t.Fatalf("h1 pulsed again after reconnecting (%d new, last at %s)", len(p)-n1, rel(p[len(p)-1].at))
	}
	if s := e.hostWorkloads("h1")["locks"]; s == "active" || s == "starting" {
		t.Fatalf("h1 resumed the moved workload: %q", s)
	}
	if r, err := lockCall(hcB, url1, "job:failover", "status", nil); err == nil && r.Status == 200 {
		t.Fatalf("h1 serves the lock again after reconnecting: %+v", r)
	}
	t.Logf("h1 reconnected at %s: revoked, silent for 12 s; store max token %d", rel(tBack), fs.Max("job:failover"))
}
