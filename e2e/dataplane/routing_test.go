//go:build e2edataplane

package dataplane

// The routing loop (issue #422, LOOP.md "Routing"): public ingress, service
// bindings and WebSockets across moves, on the same cluster as
// TestDataplane. Three stateful workloads (testdata/routing):
//
//   - rt-a: Worker A, with public routes and a service binding to B;
//   - rt-b: Worker B, the binding's target (fetch and RPC);
//   - rt-ws: a Durable Object that echoes on hibernatable WebSockets.
//
// Clients: a public client that drives netd's public listener directly
// (tw-hostd -test-public-listen-dir; Funnel itself is out of scope), a
// tailnet caller (rt-cli) that calls A and B and holds the WebSockets, and
// a recorder node (rt-rec) that A reports every /gate hit to. Every answer
// carries the answering copy's (host, epoch, ver).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// routingScenario is a fault mix and the workloads its faults target.
type routingScenario struct {
	faults    []string
	workloads []string
}

var routingScenarios = map[string]routingScenario{
	"bindings":   {[]string{"cut-takeover", "kill-host", "move", "pause-hostd-takeover", "pause-takeover", "restart-mgmt", "revoke-binding"}, []string{"rt-a", "rt-b"}},
	"public":     {[]string{"cut-short", "cut-takeover", "kill-host", "move", "paths", "pause-short", "pause-takeover", "restart-mgmt"}, []string{"rt-a"}},
	"routing":    {[]string{"crash-hostd", "cut-short", "cut-takeover", "hibernate", "kill-host", "move", "paths", "pause-hostd-takeover", "pause-short", "pause-takeover", "restart-mgmt", "revoke-binding"}, []string{"rt-a", "rt-b", "rt-ws"}},
	"websockets": {[]string{"cut-takeover", "hibernate", "kill-host", "kill-workerd", "move", "pause-short", "pause-takeover", "restart-mgmt"}, []string{"rt-ws"}},
}

const (
	// gateRoute is the public route the paths fault adds and removes.
	gateRoute = `"/gate/*"`
	cliNode   = "rt-cli"
)

// ident is what a copy says about itself: its node hostname, host, lease
// epoch and bundle version, and (B) the caller it saw.
type ident struct {
	Hostname string `json:"hostname,omitempty"`
	Host     string `json:"host"`
	Epoch    int64  `json:"epoch"`
	Ver      string `json:"ver"`
	Caller   string `json:"caller,omitempty"`
}

type requester struct {
	Kind     string `json:"kind"`
	Hostname string `json:"hostname,omitempty"`
}

// rtOp is one client request. Path is the op kind: public (/pub/hello),
// public-bind (/pub/bind), tailnet-a, tailnet-b, bind-fetch, bind-rpc,
// gate (/gate/hit over the public listener), not-public (a tailnet-only
// path over the public listener), and the probes probe-pub, probe-a,
// probe-b, probe-ws (every endpoint ever seen).
type rtOp struct {
	Path       string
	ID         string `json:",omitempty"`
	Via        string // the host whose endpoint or listener was called
	Sent, Recv time.Time
	Status     int
	Code       string     `json:",omitempty"` // tw-error
	OK         bool       // 200 with a parsed answer
	A          *ident     `json:",omitempty"`
	B          *ident     `json:",omitempty"`
	W          *ident     `json:",omitempty"` // rt-ws
	Req        *requester `json:",omitempty"`
	Ms         int64      `json:",omitempty"` // how long A's binding call took
	Err        string     `json:",omitempty"`
}

// wsEv is one WebSocket event: open, dial-fail, send, recv, close, stop.
type wsEv struct {
	Conn  string
	Kind  string
	S     int
	At    time.Time
	Via   string `json:",omitempty"` // the host dialed
	Host  string `json:",omitempty"`
	Epoch int64  `json:",omitempty"`
	Boot  string `json:",omitempty"`
	Err   string `json:",omitempty"`
}

// gateHit is A's report that a /gate request reached its code.
type gateHit struct {
	ID, Ver, Host string
	Epoch         int64
	At            time.Time
}

// rtFault is a fault on one workload's host.
type rtFault struct {
	faultEv
	Workload string
}

// rtDep is a change of a workload's deployment (host, epoch or version).
type rtDep struct {
	Workload string
	At       time.Time
	Host     string
	Epoch    int64
	Ver      string
	DNS      string
}

// rtRevoke is a binding revocation: B undeployed (At: mgmt answered) and
// back (Restored: its new copy active).
type rtRevoke struct {
	At, Restored time.Time
}

type rhist struct {
	mu      sync.Mutex
	f       *os.File
	enc     *json.Encoder
	ops     []rtOp
	ws      []wsEv
	gates   []gateHit
	faults  []rtFault
	deps    []rtDep
	revokes []rtRevoke
}

func newRHist(path string) (*rhist, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &rhist{f: f, enc: json.NewEncoder(f)}, nil
}

func (h *rhist) add(typ string, v any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch x := v.(type) {
	case rtOp:
		h.ops = append(h.ops, x)
	case wsEv:
		h.ws = append(h.ws, x)
	case gateHit:
		h.gates = append(h.gates, x)
	case rtFault:
		h.faults = append(h.faults, x)
	case rtDep:
		h.deps = append(h.deps, x)
	case rtRevoke:
		h.revokes = append(h.revokes, x)
	}
	h.enc.Encode(map[string]any{"type": typ, "ev": v})
}

// faultEvs are the faults as faultEv (for inFault).
func (h *rhist) faultEvs() []faultEv {
	out := make([]faultEv, len(h.faults))
	for i, f := range h.faults {
		out[i] = f.faultEv
	}
	return out
}

// rcluster is the routing loop's view of the cluster: where each workload
// is, the bundles, and the public listeners.
type rcluster struct {
	*cluster
	tgs     map[string]*target // workload → where it is
	bundles map[string]string  // rt-a (gate listed), rt-a-nogate, rt-b, rt-ws
	aGate   atomic.Bool        // the deployed rt-a lists /gate/*
	h       *rhist
}

func (c *rcluster) deployW(workload, bundle, host string, replace, force bool) (deployment, int, []byte, error) {
	var d deployment
	st, b, err := c.try("POST", "/v1/deployments", map[string]any{"tenantID": c.tenantID, "workload": workload, "bundle": bundle,
		"host": host, "replace": replace, "forceTakeover": force}, &d)
	return d, st, b, err
}

// moveW deploys workload onto host (see nemesis.moveTo): live is
// --replace --force-takeover, else --force-takeover alone, retried until
// mgmt allows it.
func (c *rcluster) moveW(workload, bundle, host string, live bool, within time.Duration) (deployment, string, error) {
	deadline := time.Now().Add(within)
	refused := map[int]int{}
	start := time.Now()
	tg := c.tgs[workload]
	for {
		d, st, b, err := c.deployW(workload, bundle, host, live, true)
		if err == nil && st == 202 {
			tg.mu.Lock()
			tg.depID = d.DeploymentID
			tg.mu.Unlock()
			d, err = c.waitActive(d.DeploymentID, 120*time.Second)
			if err != nil {
				return d, "", err
			}
			tg.set(d)
			return d, fmt.Sprintf("accepted after %.1fs (refused %v)", time.Since(start).Seconds(), refused), nil
		}
		refused[st]++
		if time.Now().After(deadline) {
			return d, "", fmt.Errorf("deploy %s to %s: %d %s %v (refused %v)", workload, host, st, b, err, refused)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (c *rcluster) aBundle() string {
	if c.aGate.Load() {
		return c.bundles["rt-a"]
	}
	return c.bundles["rt-a-nogate"]
}

func (c *rcluster) bundleOf(workload string) string {
	if workload == "rt-a" {
		return c.aBundle()
	}
	return c.bundles[workload]
}

// pubAddr is the address of the public listener a host has open now
// (testfunnel.go writes one file per public node), or "".
func (c *rcluster) pubAddr(host string) string {
	m, _ := filepath.Glob(filepath.Join(c.work, host, "pub", "*.addr"))
	for _, f := range m {
		if b, err := os.ReadFile(f); err == nil && len(b) > 0 {
			return string(b)
		}
	}
	return ""
}

// dedupeNames keeps MagicDNS names unique in testcontrol, as real control
// does. Two things leave several nodes under one name there: mgmt deletes
// a moved object's previous device through the API, which fakeTS ignores,
// and a node that rejoins from saved state can come back as a new
// testcontrol node while the old one stays. Then a call to the name (a
// service binding's target, after a move back) could reach a dead node.
// So, every 200 ms: of the nodes that ever had a name, the one that started
// last (its disco key changed most recently) has it, and the others are
// renamed out of the way; a renamed node that starts again gets it back.
func dedupeNames(ctl *nettest.Control, stop <-chan struct{}, renamed *atomic.Int64) {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	type seen struct {
		name    string // its own name (before any rename)
		disco   string
		started time.Time
	}
	nodes := map[tailcfg.NodeID]*seen{}
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		all := ctl.Server.AllNodes()
		owner := map[string]tailcfg.NodeID{}
		for _, n := range all {
			s := nodes[n.ID]
			if s == nil {
				s = &seen{name: n.Name}
				nodes[n.ID] = s
			}
			if d := n.DiscoKey.String(); d != s.disco {
				s.disco, s.started = d, time.Now()
			}
			if o, ok := owner[s.name]; !ok || s.started.After(nodes[o].started) {
				owner[s.name] = n.ID
			}
		}
		for _, n := range all {
			s := nodes[n.ID]
			want := s.name
			if owner[s.name] != n.ID {
				_, dom, _ := strings.Cut(s.name, ".")
				want = "stale-" + strings.ToLower(string(n.StableID)) + "." + dom
			}
			if n.Name != want {
				n.Name = want
				ctl.Server.UpdateNode(n)
				renamed.Add(1)
			}
		}
	}
}

// TestRouting runs one routing scenario (TW_DP_SCENARIO: public | bindings
// | websockets | routing, default routing) for TW_DP_DURATION (default 4m)
// with seed TW_DP_SEED (default today's date), heals, and checks N1–N6
// (check_routing_test.go). Artifacts: results/<stamp>-<scenario>-<seed>/.
func TestRouting(t *testing.T) {
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	if !hostdtest.HasNode() {
		t.Skip("node not available (needed to bundle the apps)")
	}
	scenario := envOr("TW_DP_SCENARIO", "routing")
	sc, ok := routingScenarios[scenario]
	if !ok {
		t.Fatalf("TW_DP_SCENARIO %q: one of public, bindings, websockets, routing", scenario)
	}
	names := sc.faults
	seed, err := strconv.ParseInt(envOr("TW_DP_SEED", time.Now().UTC().Format("20060102")), 10, 64)
	if err != nil {
		t.Fatalf("TW_DP_SEED: %v", err)
	}
	dur, err := time.ParseDuration(envOr("TW_DP_DURATION", "4m"))
	if err != nil {
		t.Fatalf("TW_DP_DURATION: %v", err)
	}
	if fs := os.Getenv("TW_DP_FAULTS"); fs != "" {
		names = strings.Split(fs, ",")
		for _, n := range names {
			if _, ok := faults[n]; !ok && !routingFault[n] {
				t.Fatalf("TW_DP_FAULTS: unknown fault %q", n)
			}
		}
	}
	if ws := os.Getenv("TW_DP_WORKLOADS"); ws != "" { // narrow which workloads the faults hit
		sc.workloads = strings.Split(ws, ",")
	}
	// TW_DP_BIND_TIMEOUT: how long the client waits for a binding call
	// through A (long, so a slow call is seen to end, and how).
	bindTimeout, err := time.ParseDuration(envOr("TW_DP_BIND_TIMEOUT", "20s"))
	if err != nil {
		t.Fatalf("TW_DP_BIND_TIMEOUT: %v", err)
	}
	t.Logf("routing scenario %s, seed %d, duration %v, faults %v on %v", scenario, seed, dur, names, sc.workloads)

	root, _ := filepath.Abs("results")
	rdir := filepath.Join(root, fmt.Sprintf("%s-%s-%d", time.Now().UTC().Format("20060102T150405Z"), scenario, seed))
	if err := os.MkdirAll(rdir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Logf("artifacts: %s", rdir)
	work, err := hostdtest.ShortDir("tw-rt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(work) })
	pids, err := os.OpenFile(filepath.Join(rdir, "pids"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pids.Close() })
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)

	var devRenamed atomic.Int64
	dedupeStop := make(chan struct{})
	t.Cleanup(func() { close(dedupeStop) })
	go dedupeNames(ctl, dedupeStop, &devRenamed)
	base := &cluster{t: t, bin: cliflow.BuildBinaries(t), dir: rdir, work: work, ctl: ctl, tsURL: fakeTS(t).URL,
		sock: filepath.Join(work, "mgmt.sock"), workerd: wd, hosts: []string{"h1", "h2", "h3"},
		proxies: map[string]*sockProxy{}, pids: pids, procs: map[string]*proc{}, stopped: map[string]bool{}}
	t.Cleanup(base.stopAll)
	base.hc = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", base.sock)
	}}}
	h, err := newRHist(filepath.Join(rdir, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.f.Close()
	c := &rcluster{cluster: base, tgs: map[string]*target{}, bundles: map[string]string{}, h: h}

	nodes := map[string]*nettest.Node{}
	var nmu sync.Mutex
	var wg sync.WaitGroup
	for _, hn := range []string{cliNode, "rt-rec"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := nettest.NewNode(context.Background(), ctl.URL, nettest.DummyKey, hn, filepath.Join(work, hn))
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
	if err := serveGateRecorder(nodes["rt-rec"], h); err != nil {
		t.Fatal(err)
	}
	recURL := "http://" + nodes["rt-rec"].IP4

	keyFile := filepath.Join(work, "api.env")
	cliflow.WriteFakeOAuthFile(t, keyFile)
	c.startMgmt(true)
	var tn struct{ TenantID string }
	c.must("POST", "/v1/tenants", map[string]any{"tailnet": nettest.MagicDNSDomain, "controlURL": ctl.URL, "mintMode": "oauth",
		"oauthClientFile": keyFile, "publicIngress": "allow"}, &tn)
	c.tenantID = tn.TenantID
	for _, id := range c.hosts {
		c.proxies[id] = startProxy(t, filepath.Join(work, id+"-mgmt.sock"), c.sock)
	}
	// Each host's public listeners write their address under <host>/pub.
	c.hostArgs = func(id string) []string { return []string{"-test-public-listen-dir", filepath.Join(work, id, "pub")} }
	for _, id := range c.hosts {
		c.startHost(id)
	}
	c.waitHostsLive(c.hosts...)

	// The bundles: rt-a with and without /gate/* in its public routes.
	build := func(dir string, vars map[string]string) map[string][]byte {
		app, _ := filepath.Abs(filepath.Join("testdata", "routing", dir))
		files, err := hostdtest.Build(app, vars)
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	fa := build("a", map[string]string{"REC": recURL, "VER": "a-gate"})
	c.bundles["rt-a"] = base64.StdEncoding.EncodeToString(hostdtest.MustTar(fa))
	fa = build("a", map[string]string{"REC": recURL, "VER": "a-nogate"})
	tw := string(fa["tailworks.jsonc"])
	if !strings.Contains(tw, gateRoute) {
		t.Fatalf("testdata/routing/a/tailworks.jsonc has no %s route", gateRoute)
	}
	fa["tailworks.jsonc"] = []byte(strings.Replace(tw, ", "+gateRoute, "", 1))
	c.bundles["rt-a-nogate"] = base64.StdEncoding.EncodeToString(hostdtest.MustTar(fa))
	c.bundles["rt-b"] = base64.StdEncoding.EncodeToString(hostdtest.MustTar(build("b", map[string]string{"VER": "b1"})))
	c.bundles["rt-ws"] = base64.StdEncoding.EncodeToString(hostdtest.MustTar(build("ws", map[string]string{"VER": "w1"})))
	c.aGate.Store(true)

	// B first (A's binding names it), then A and the WebSocket object.
	for _, p := range []struct{ w, host string }{{"rt-b", "h2"}, {"rt-a", "h1"}, {"rt-ws", "h3"}} {
		c.tgs[p.w] = &target{seen: map[string]string{}}
		d, st, b, err := c.deployW(p.w, c.bundleOf(p.w), p.host, false, false)
		if err != nil || st != 202 {
			c.dumpLogs()
			t.Fatalf("deploy %s: %d %s %v", p.w, st, b, err)
		}
		c.tgs[p.w].depID = d.DeploymentID
		if d, err = c.waitActive(d.DeploymentID, 120*time.Second); err != nil {
			c.dumpLogs()
			t.Fatal(err)
		}
		c.tgs[p.w].set(d)
		t.Logf("%s on %s at epoch %d (%s)", p.w, d.Host, d.Epoch, d.Endpoint.MagicDNS)
	}

	w := &rworkload{c: c, h: h, cli: nodes[cliNode].HTTPClient(5 * time.Second), bindHC: nodes[cliNode].HTTPClient(bindTimeout), wsHC: nodes[cliNode].HTTPClient(0), stop: make(chan struct{}),
		pub: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}}
	// Wait until every path answers before starting the clock.
	ready := map[string]func() bool{
		"public":     func() bool { return w.public("/pub/hello", "public").OK },
		"bind-fetch": func() bool { return w.tailnetA("/bind/fetch", "bind-fetch").OK },
		"bind-rpc":   func() bool { return w.tailnetA("/bind/rpc", "bind-rpc").OK },
		"tailnet-b":  func() bool { return w.tailnetB().OK },
		"ws":         func() bool { return w.probeWS(c.tgs["rt-ws"].get().Endpoint.MagicDNS, "").OK },
	}
	for _, name := range []string{"public", "bind-fetch", "bind-rpc", "tailnet-b", "ws"} {
		for i := 0; !ready[name](); i++ {
			if i > 300 {
				c.dumpLogs()
				t.Fatalf("%s never answered", name)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	h.mu.Lock()
	h.ops = nil // the readiness polls are not part of the run
	h.mu.Unlock()
	w.watch()
	w.run()

	time.Sleep(10 * time.Second)
	n := &rnemesis{c: c, w: w, rnd: rand.New(rand.NewSource(seed)), workloads: sc.workloads}
	n.dp = &nemesis{c: c.cluster, rnd: n.rnd}
	n.run(names, time.Now().Add(dur), t.Logf)

	// Heal.
	for _, id := range c.hosts {
		c.proxies[id].cut(false)
		c.resume(id)
		if c.pid(id) == 0 {
			c.startHost(id)
		}
	}
	c.waitHostsLive(c.hosts...)
	healed := time.Now()
	t.Logf("healed; letting the clients run 20 s more")
	time.Sleep(20 * time.Second)
	close(w.stop)
	w.wg.Wait()
	w.closeWS()
	time.Sleep(time.Second)

	// N5-converge: every workload active and every path answering.
	conv := result{ID: "N0", Pass: true}
	for _, wl := range []string{"rt-a", "rt-b", "rt-ws"} {
		if _, err := c.waitActive(c.tgs[wl].get().DeploymentID, 60*time.Second); err != nil {
			conv.fail("%s not active after healing: %v", wl, err)
		}
	}
	checks := map[string]func() bool{
		"public":     func() bool { return w.public("/pub/hello", "final").OK },
		"bind-fetch": func() bool { return w.tailnetA("/bind/fetch", "final").OK },
		"bind-rpc":   func() bool { return w.tailnetA("/bind/rpc", "final").OK },
		"tailnet-b":  func() bool { return w.tailnetBPath("final").OK },
		"ws":         func() bool { return w.probeWS(c.tgs["rt-ws"].get().Endpoint.MagicDNS, "final").OK },
	}
	by := time.Now().Add(60 * time.Second)
	for _, name := range []string{"public", "bind-fetch", "bind-rpc", "tailnet-b", "ws"} {
		for !checks[name]() {
			if time.Now().After(by) {
				conv.fail("%s not answering 60 s after healing", name)
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	conv.Summary = fmt.Sprintf("converged %.1fs after healing", time.Since(healed).Seconds())

	// The client's netmap at the end (evidence: stale devices, names).
	if st, err := nodes[cliNode].LC.Status(context.Background()); err == nil {
		var peers []map[string]any
		for _, p := range st.Peer {
			peers = append(peers, map[string]any{"name": p.DNSName, "ips": p.TailscaleIPs, "online": p.Online, "id": p.ID})
		}
		writeJSON(rdir, "peers.json", peers)
	}
	t.Logf("testcontrol: %d renames to keep names unique", devRenamed.Load())

	h.mu.Lock()
	res := []result{conv, checkN1(h), checkN2(h), checkN3(h), checkN4(h), checkN5(h), checkN6(h)}
	h.mu.Unlock()
	writeJSON(rdir, "faults.json", h.faults)
	writeJSON(rdir, "report.json", map[string]any{"scenario": scenario, "seed": seed, "duration": dur.String(), "results": res})
	failed := false
	for _, r := range res {
		mark := "✓"
		if !r.Pass {
			mark, failed = "✗", true
		}
		t.Logf("%s %s: %s", mark, r.ID, r.Summary)
		for _, e := range r.Evidence {
			t.Logf("    %s", e)
		}
		for _, e := range r.Notes {
			t.Logf("    (note) %s", e)
		}
	}
	if failed {
		t.Errorf("invariants violated; seed %d, scenario %s; artifacts in %s", seed, scenario, rdir)
	}
}

// rworkload is the client side.
type rworkload struct {
	c   *rcluster
	h   *rhist
	cli *http.Client // tailnet, as rt-cli
	// bindHC is rt-cli with a longer timeout, so a slow binding call is
	// seen to finish (and how) rather than cut off.
	bindHC *http.Client
	wsHC   *http.Client // tailnet, no timeout (WebSockets)
	pub    *http.Client // the public client (loopback to netd's public listener)
	stop   chan struct{}
	wg     sync.WaitGroup
	seq    atomic.Int64

	wsMu    sync.Mutex
	wsConns map[string]*websocket.Conn
	quiet   atomic.Int64 // unix nanos until which WebSocket clients don't send (hibernate)
}

func (w *rworkload) id(p string) string { return fmt.Sprintf("%s%06d", p, w.seq.Add(1)) }

func (w *rworkload) every(d time.Duration, f func()) {
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

// do runs one GET and fills op from the answer: {a, b, req} from A, an
// ident from B or the WebSocket object.
func (w *rworkload) do(hc *http.Client, u string, op *rtOp, record bool) {
	op.Sent = time.Now()
	resp, err := hc.Get(u)
	if err != nil {
		op.Recv, op.Err = time.Now(), err.Error()
	} else {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		op.Recv, op.Status, op.Code = time.Now(), resp.StatusCode, resp.Header.Get("tw-error")
		var m struct {
			A   *ident     `json:"a"`
			B   *ident     `json:"b"`
			Req *requester `json:"req"`
			Err string     `json:"err"`
			Ms  int64      `json:"ms"`
		}
		switch op.Path {
		case "tailnet-b", "probe-b":
			var id ident
			if json.Unmarshal(b, &id) == nil && id.Host != "" {
				op.B = &id
			}
		case "probe-ws", "ws-get":
			var id ident
			if json.Unmarshal(b, &id) == nil && id.Host != "" {
				op.W = &id
			}
		default:
			if json.Unmarshal(b, &m) == nil {
				op.A, op.B, op.Req, op.Ms = m.A, m.B, m.Req, m.Ms
				if op.Code == "" {
					op.Code = m.Err
				}
			}
		}
		op.OK = resp.StatusCode == 200 && (op.A != nil || op.B != nil || op.W != nil)
		if !op.OK && len(b) > 0 && op.Err == "" {
			op.Err = strings.TrimSpace(string(b[:min(len(b), 200)]))
		}
	}
	if record {
		w.h.add("op", *op)
	}
}

func (w *rworkload) public(path, kind string) rtOp {
	host := w.c.tgs["rt-a"].get().Host
	op := rtOp{Path: kind, Via: host}
	addr := w.c.pubAddr(host)
	if addr == "" {
		op.Sent, op.Recv, op.Err = time.Now(), time.Now(), "no public listener on "+host
		if kind != "final" {
			w.h.add("op", op)
		}
		return op
	}
	if kind == "gate" {
		op.ID = w.id("g")
		path += "?id=" + op.ID
	}
	w.do(w.pub, "http://"+addr+path, &op, kind != "final")
	return op
}

func (w *rworkload) tailnetA(path, kind string) rtOp {
	d := w.c.tgs["rt-a"].get()
	op := rtOp{Path: kind, Via: d.Host}
	hc := w.cli
	if strings.HasPrefix(path, "/bind/") {
		hc = w.bindHC
	}
	w.do(hc, "http://"+d.Endpoint.MagicDNS+path, &op, kind != "final")
	return op
}

func (w *rworkload) tailnetB() rtOp { return w.tailnetBPath("tailnet-b") }

func (w *rworkload) tailnetBPath(kind string) rtOp {
	d := w.c.tgs["rt-b"].get()
	op := rtOp{Path: "tailnet-b", Via: d.Host}
	w.do(w.cli, "http://"+d.Endpoint.MagicDNS+"/hello", &op, kind != "final")
	return op
}

func (w *rworkload) probeWS(dns, kind string) rtOp {
	op := rtOp{Path: "ws-get", Via: w.c.tgs["rt-ws"].get().Host}
	if kind == "probe" {
		op.Path = "probe-ws"
	}
	w.do(w.cli, "http://"+dns+"/ws/r1", &op, kind == "probe")
	return op
}

func (w *rworkload) run() {
	w.every(200*time.Millisecond, func() { w.public("/pub/hello", "public") })
	w.every(400*time.Millisecond, func() { w.public("/pub/bind", "public-bind") })
	w.every(500*time.Millisecond, func() { w.public("/gate/hit", "gate") })
	// A tailnet-only path over the public listener: always 404 not_public.
	w.every(2*time.Second, func() { w.public("/t/hello", "not-public") })
	w.every(300*time.Millisecond, func() { w.tailnetA("/t/hello", "tailnet-a") })
	w.every(300*time.Millisecond, func() { w.tailnetB() })
	w.every(300*time.Millisecond, func() { w.tailnetA("/bind/fetch", "bind-fetch") })
	w.every(300*time.Millisecond, func() { w.tailnetA("/bind/rpc", "bind-rpc") })
	// Probes: every endpoint and public listener ever seen, so a copy that
	// should be fenced is caught answering.
	w.every(time.Second, func() {
		for _, host := range w.c.hosts {
			if addr := w.c.pubAddr(host); addr != "" {
				go func() {
					op := rtOp{Path: "probe-pub", Via: host}
					w.do(w.pub, "http://"+addr+"/pub/hello", &op, true)
				}()
			}
		}
		for dns, host := range w.c.tgs["rt-a"].endpoints() {
			go func() {
				op := rtOp{Path: "probe-a", Via: host}
				w.do(w.cli, "http://"+dns+"/t/hello", &op, true)
			}()
		}
		for dns, host := range w.c.tgs["rt-b"].endpoints() {
			go func() {
				op := rtOp{Path: "probe-b", Via: host}
				w.do(w.cli, "http://"+dns+"/hello", &op, true)
			}()
		}
		// The WebSocket object's probes go quiet with its clients
		// (hibernate): a request would keep it from being evicted.
		if time.Now().UnixNano() >= w.quiet.Load() {
			for dns := range w.c.tgs["rt-ws"].endpoints() {
				go w.probeWS(dns, "probe")
			}
		}
	})
	// What mgmt answers for A's binding now (evidence: where binding calls
	// are sent).
	w.every(time.Second, func() {
		op := rtOp{Path: "resolve", Sent: time.Now()}
		var t struct{ MagicDNS, Status string }
		a := w.c.tgs["rt-a"].get()
		st, b, err := w.c.try("GET", fmt.Sprintf("/v1/tenants/%s/workloads/rt-a/service-bindings/B?epoch=%d&host=%s", w.c.tenantID, a.Epoch, a.Host), nil, &t)
		op.Recv, op.Status = time.Now(), st
		switch {
		case err != nil:
			op.Err = err.Error()
		case st != 200:
			op.Err = strings.TrimSpace(string(b))
		default:
			op.OK, op.Err = true, t.MagicDNS+" "+t.Status
		}
		w.h.add("op", op)
	})
	w.wsConns = map[string]*websocket.Conn{}
	for i := range 3 {
		w.wg.Add(1)
		go w.wsClient(fmt.Sprintf("k%d", i))
	}
}

// wsClient keeps one WebSocket open to the Echo object (reconnecting after
// a close) and sends a sequence number every 200 ms; each connection gets a
// new ID.
func (w *rworkload) wsClient(k string) {
	defer w.wg.Done()
	gen := 0
	for {
		select {
		case <-w.stop:
			return
		default:
		}
		gen++
		conn := fmt.Sprintf("%s.%d", k, gen)
		d := w.c.tgs["rt-ws"].get()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		ws, _, err := websocket.Dial(ctx, "ws://"+d.Endpoint.MagicDNS+"/ws/r1?c="+conn, &websocket.DialOptions{HTTPClient: w.wsHC})
		cancel()
		if err != nil {
			w.h.add("ws", wsEv{Conn: conn, Kind: "dial-fail", At: time.Now(), Via: d.Host, Err: err.Error()})
			select {
			case <-w.stop:
				return
			case <-time.After(time.Second):
			}
			continue
		}
		w.h.add("ws", wsEv{Conn: conn, Kind: "open", At: time.Now(), Via: d.Host})
		w.wsMu.Lock()
		w.wsConns[conn] = ws
		w.wsMu.Unlock()
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				_, b, err := ws.Read(context.Background())
				if err != nil {
					w.h.add("ws", wsEv{Conn: conn, Kind: "close", At: time.Now(), Via: d.Host, Err: err.Error()})
					return
				}
				var m struct {
					S     int    `json:"s"`
					Host  string `json:"host"`
					Epoch int64  `json:"epoch"`
					Boot  string `json:"boot"`
				}
				json.Unmarshal(b, &m)
				w.h.add("ws", wsEv{Conn: conn, Kind: "recv", S: m.S, At: time.Now(), Via: d.Host, Host: m.Host, Epoch: m.Epoch, Boot: m.Boot})
			}
		}()
		s := 0
		t := time.NewTicker(200 * time.Millisecond)
	send:
		for {
			select {
			case <-w.stop:
				break send
			case <-done:
				break send
			case <-t.C:
				if time.Now().UnixNano() < w.quiet.Load() {
					continue
				}
				s++
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := ws.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"s":%d}`, s)))
				cancel()
				if err != nil {
					// Not sent: the connection is going away; the reader
					// records the close.
					w.h.add("ws", wsEv{Conn: conn, Kind: "send-fail", S: s, At: time.Now(), Via: d.Host, Err: err.Error()})
					ws.CloseNow()
					<-done
					break send
				}
				w.h.add("ws", wsEv{Conn: conn, Kind: "send", S: s, At: time.Now(), Via: d.Host})
			}
		}
		t.Stop()
		select {
		case <-w.stop:
			// Give the last replies a moment, then stop (not a close by
			// the server).
			time.Sleep(2 * time.Second)
			w.h.add("ws", wsEv{Conn: conn, Kind: "stop", At: time.Now(), Via: d.Host})
			ws.CloseNow()
			<-done
			return
		default:
		}
		w.wsMu.Lock()
		delete(w.wsConns, conn)
		w.wsMu.Unlock()
		time.Sleep(500 * time.Millisecond)
	}
}

func (w *rworkload) closeWS() {
	w.wsMu.Lock()
	defer w.wsMu.Unlock()
	for _, c := range w.wsConns {
		c.CloseNow()
	}
}

// watch polls mgmt for each workload's latest deployment and records every
// change of host, epoch or version.
func (w *rworkload) watch() {
	for wl, tg := range w.c.tgs {
		var last deployment
		w.every(300*time.Millisecond, func() {
			tg.mu.Lock()
			id := tg.depID
			tg.mu.Unlock()
			var d deployment
			if st, _, err := w.c.try("GET", "/v1/deployments/"+id, nil, &d); err != nil || st != 200 {
				return
			}
			if d.Status != "active" || d.Endpoint.Status != "ready" {
				return
			}
			tg.mu.Lock()
			if tg.depID == id {
				tg.d = d
				tg.seen[d.Endpoint.MagicDNS] = d.Host
			}
			tg.mu.Unlock()
			if d.Host != last.Host || d.Epoch != last.Epoch || d.DeploymentID != last.DeploymentID {
				ver := ""
				if wl == "rt-a" {
					ver = "a-nogate"
					if w.c.aGate.Load() {
						ver = "a-gate"
					}
				}
				w.h.add("deployment", rtDep{Workload: wl, At: time.Now(), Host: d.Host, Epoch: d.Epoch, Ver: ver, DNS: d.Endpoint.MagicDNS})
				last = d
			}
		})
	}
}

// serveGateRecorder is the node A reports /gate hits to.
func serveGateRecorder(n *nettest.Node, h *rhist) error {
	ln, err := n.Listen("tcp", ":80")
	if err != nil {
		return err
	}
	go http.Serve(ln, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gate" {
			rw.WriteHeader(404)
			return
		}
		q := r.URL.Query()
		ep, _ := strconv.ParseInt(q.Get("epoch"), 10, 64)
		h.add("gate", gateHit{ID: q.Get("id"), Ver: q.Get("ver"), Host: q.Get("host"), Epoch: ep, At: time.Now()})
		rw.WriteHeader(204)
	}))
	return nil
}

// The routing nemesis. Host faults hit the host of one of the scenario's
// workloads; faults that exist in TestDataplane's table (kill-host,
// crash-hostd, kill-workerd, pause-short, cut-short, restart-mgmt) run
// from there.
type rnemesis struct {
	c         *rcluster
	w         *rworkload
	dp        *nemesis
	rnd       *rand.Rand
	workloads []string
}

// routingFault are the faults only this loop has (or runs its own way).
var routingFault = map[string]bool{
	"cut-takeover":         true,
	"hibernate":            true,
	"move":                 true,
	"paths":                true,
	"pause-hostd-takeover": true,
	"pause-takeover":       true,
	"revoke-binding":       true,
}

func (n *rnemesis) between(lo, hi time.Duration) time.Duration { return n.dp.between(lo, hi) }

func (n *rnemesis) run(names []string, until time.Time, logf func(string, ...any)) {
	for time.Now().Before(until) {
		time.Sleep(n.between(3*time.Second, 8*time.Second))
		if !time.Now().Before(until) {
			return
		}
		name := names[n.rnd.Intn(len(names))]
		wl := n.workloads[n.rnd.Intn(len(n.workloads))]
		switch name {
		case "paths":
			wl = "rt-a"
		case "revoke-binding":
			wl = "rt-b"
		case "hibernate":
			wl = "rt-ws"
		}
		host := n.c.tgs[wl].get().Host
		start := time.Now()
		var detail string
		if routingFault[name] {
			detail = n.fault(name, wl, host)
		} else {
			detail = faults[name].run(n.dp, host)
		}
		ev := rtFault{faultEv: faultEv{Kind: name, Host: host, Start: start, End: time.Now(), Detail: detail}, Workload: wl}
		n.c.h.add("fault", ev)
		logf("nemesis: %s of %s on %s (%.1fs): %s", name, wl, host, ev.End.Sub(start).Seconds(), detail)
	}
}

func (n *rnemesis) other(host string) string { return n.dp.other(host) }

func (n *rnemesis) fault(name, wl, h string) string {
	c := n.c
	switch name {
	case "move":
		to := n.other(h)
		d, how, err := c.moveW(wl, c.bundleOf(wl), to, true, 60*time.Second)
		if err != nil {
			return "FAILED: " + err.Error()
		}
		return fmt.Sprintf("%s → %s epoch %d, %s", h, d.Host, d.Epoch, how)
	case "pause-takeover":
		return n.takeover(wl, h, func() { c.pause(h, false) }, func() { c.resume(h) })
	case "pause-hostd-takeover":
		return n.takeover(wl, h, func() { c.pause(h, true) }, func() { c.resume(h) })
	case "cut-takeover":
		return n.takeover(wl, h, func() { c.proxies[h].cut(true) }, func() { c.proxies[h].cut(false) })
	case "paths":
		// Redeploy A on its host with /gate/* added to or removed from its
		// public routes.
		gate := !c.aGate.Load()
		b := c.bundles["rt-a-nogate"]
		if gate {
			b = c.bundles["rt-a"]
		}
		c.aGate.Store(gate)
		d, how, err := c.moveW("rt-a", b, h, false, 60*time.Second)
		if err != nil {
			return "FAILED: " + err.Error()
		}
		return fmt.Sprintf("gate listed=%v on %s epoch %d, %s", gate, d.Host, d.Epoch, how)
	case "revoke-binding":
		// Undeploy B (the binding's target), wait, deploy it again on the
		// same host.
		tg := c.tgs["rt-b"]
		id := tg.get().DeploymentID
		st, b, err := c.try("DELETE", "/v1/deployments/"+id+"?reason=chaos", nil, nil)
		at := time.Now()
		if err != nil || st/100 != 2 {
			return fmt.Sprintf("FAILED: undeploy %s: %d %s %v", id, st, b, err)
		}
		time.Sleep(n.between(8*time.Second, 12*time.Second))
		to := h // the host it was undeployed from: another one may still have its purged node (#593)
		d, how, err := c.moveW("rt-b", c.bundles["rt-b"], to, false, 60*time.Second)
		c.h.add("revoke", rtRevoke{At: at, Restored: time.Now()})
		if err != nil {
			return "FAILED: redeploy: " + err.Error()
		}
		return fmt.Sprintf("undeployed at %s; back on %s epoch %d, %s", at.Format("15:04:05.000"), d.Host, d.Epoch, how)
	case "hibernate":
		// The WebSocket clients go quiet past the object's idle eviction
		// (~10 s), then talk again: the sockets stay open across it.
		q := n.between(15*time.Second, 20*time.Second)
		n.w.quiet.Store(time.Now().Add(q).UnixNano())
		time.Sleep(q + 3*time.Second)
		return fmt.Sprintf("quiet %v", q)
	}
	return "unknown fault " + name
}

// takeover breaks h, moves wl elsewhere with --force-takeover as soon as
// mgmt allows, waits a little, and heals h.
func (n *rnemesis) takeover(wl, h string, fail, heal func()) string {
	t0 := time.Now()
	fail()
	to := n.other(h)
	d, how, err := n.c.moveW(wl, n.c.bundleOf(wl), to, false, 120*time.Second)
	if err != nil {
		heal()
		return "FAILED: " + err.Error()
	}
	time.Sleep(n.between(2*time.Second, 8*time.Second))
	heal()
	return fmt.Sprintf("%s → %s epoch %d, takeover %s, healed at t0+%.1fs", h, d.Host, d.Epoch, how, time.Since(t0).Seconds())
}
