//go:build e2edataplane

package dataplane

import (
	"context"
	"encoding/base64"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// TestDataplane runs one scenario (TW_DP_SCENARIO: queues | fencing | d1 |
// mixed, default mixed) for TW_DP_DURATION (default 4m) with seed TW_DP_SEED
// (default: today's date, so a nightly run is reproducible from its date),
// then heals everything, waits for convergence and checks the invariants.
// Artifacts: e2e/dataplane/results/<stamp>-<scenario>-<seed>/.
func TestDataplane(t *testing.T) {
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	if !hostdtest.HasNode() {
		t.Skip("node not available (needed to bundle the app)")
	}
	scenario := envOr("TW_DP_SCENARIO", "mixed")
	if strings.HasPrefix(scenario, "r2-") {
		t.Skipf("TestR2 runs %s (r2_test.go)", scenario)
	}
	if strings.HasPrefix(scenario, "workflows") {
		t.Skip("TestDataplaneWorkflows runs " + scenario)
	}
	names, ok := scenarios[scenario]
	if !ok {
		t.Fatalf("TW_DP_SCENARIO %q: one of queues, fencing, d1, mixed", scenario)
	}
	seed, err := strconv.ParseInt(envOr("TW_DP_SEED", time.Now().UTC().Format("20060102")), 10, 64)
	if err != nil {
		t.Fatalf("TW_DP_SEED: %v", err)
	}
	dur, err := time.ParseDuration(envOr("TW_DP_DURATION", "4m"))
	if err != nil {
		t.Fatalf("TW_DP_DURATION: %v", err)
	}
	if fs := os.Getenv("TW_DP_FAULTS"); fs != "" { // a comma-separated subset, for a focused rerun
		names = strings.Split(fs, ",")
		for _, n := range names {
			if _, ok := faults[n]; !ok {
				t.Fatalf("TW_DP_FAULTS: unknown fault %q", n)
			}
		}
	}
	clockLib, names := setupSkew(t, names) // skew_test.go
	t.Logf("scenario %s, seed %d, duration %v, faults %v", scenario, seed, dur, names)
	if cronScenarios[scenario] { // cron_test.go: its own workloads and checkers
		runCron(t, wd, scenario, names, seed, dur)
		return
	}
	if strings.HasPrefix(scenario, "deploys") { // deploys_test.go (#421)
		testDeploys(t, scenario, names, seed, dur)
		return
	}

	root, _ := filepath.Abs("results")
	rdir := filepath.Join(root, fmt.Sprintf("%s-%s-%d", time.Now().UTC().Format("20060102T150405Z"), scenario, seed))
	if err := os.MkdirAll(rdir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Logf("artifacts: %s", rdir)
	work, err := hostdtest.ShortDir("tw-dp-")
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

	c := &cluster{t: t, bin: cliflow.BuildBinariesTags(t, "twtestclock"), dir: rdir, work: work, ctl: ctl, tsURL: fakeTS(t).URL,
		sock: filepath.Join(work, "mgmt.sock"), workerd: wd, hosts: []string{"h1", "h2", "h3"},
		proxies: map[string]*sockProxy{}, pids: pids, procs: map[string]*proc{}, stopped: map[string]bool{},
		clocks: &hostClocks{lib: clockLib, off: map[string]time.Duration{}}}
	t.Cleanup(c.stopAll)
	c.hc = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.sock)
	}}}

	// Tailnet nodes: the client, two lock clients, the fenced store, the recorder.
	nodes := map[string]*nettest.Node{}
	var nmu sync.Mutex
	var wg sync.WaitGroup
	for _, hn := range []string{"dp-cli", "dp-lka", "dp-lkb", "dp-store", "dp-rec"} {
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
	h, err := newHistory(filepath.Join(rdir, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	fs, err := hostdtest.ServeFencedStore(nodes["dp-store"])
	if err != nil {
		t.Fatal(err)
	}
	if err := serveRecorder(nodes["dp-rec"], h); err != nil {
		t.Fatal(err)
	}
	recURL := "http://" + nodes["dp-rec"].IP4

	// mgmt (SQLite) and three hosts, each reaching mgmt through its own
	// cuttable proxy.
	keyFile := filepath.Join(work, "api.env")
	cliflow.WriteFakeOAuthFile(t, keyFile)
	c.startMgmt(true)
	var tn struct{ TenantID string }
	c.must("POST", "/v1/tenants", map[string]any{"tailnet": nettest.MagicDNSDomain, "controlURL": ctl.URL, "mintMode": "oauth", "oauthClientFile": keyFile}, &tn)
	c.tenantID = tn.TenantID
	for _, id := range c.hosts {
		c.proxies[id] = startProxy(t, filepath.Join(work, id+"-mgmt.sock"), c.sock)
		c.startHost(id)
	}
	c.waitHostsLive(c.hosts...)

	app, _ := filepath.Abs(filepath.Join("testdata", "dp"))
	files, err := hostdtest.Build(app, map[string]string{"REC": recURL})
	if err != nil {
		t.Fatal(err)
	}
	bundle := base64.StdEncoding.EncodeToString(hostdtest.MustTar(files))
	tg := &target{seen: map[string]string{}}
	d, st, b, err := c.deploy(bundle, "h1", false, false)
	if err != nil || st != 202 {
		c.dumpLogs()
		t.Fatalf("deploy: %d %s %v", st, b, err)
	}
	tg.depID = d.DeploymentID
	if d, err = c.waitActive(d.DeploymentID, 120*time.Second); err != nil {
		c.dumpLogs()
		t.Fatal(err)
	}
	tg.set(d)
	t.Logf("dp on %s at epoch %d (%s)", d.Host, d.Epoch, d.Endpoint.MagicDNS)

	w := &workload{c: c, h: h, tg: tg, cli: nodes["dp-cli"].HTTPClient(5 * time.Second), recURL: recURL, stop: make(chan struct{}),
		lockers: map[string]*http.Client{"A": nodes["dp-lka"].HTTPClient(5 * time.Second), "B": nodes["dp-lkb"].HTTPClient(5 * time.Second)},
		store:   nodes["dp-store"].HTTPClient(5 * time.Second)}
	// Wait until the workload answers before starting the clock.
	for i := 0; ; i++ {
		if _, err := getJSON(w.cli, "http://"+d.Endpoint.MagicDNS+"/reg/get", nil); err == nil {
			break
		}
		if i > 150 {
			c.dumpLogs()
			t.Fatal("dp never answered")
		}
		time.Sleep(200 * time.Millisecond)
	}
	checkSkewHook(t, c, w.cli, d.Host, d.Endpoint.MagicDNS) // skew_test.go
	w.watch()
	w.run(150 * time.Millisecond)
	w.runKV() // kv_test.go

	// Warm up, then the nemesis.
	time.Sleep(10 * time.Second)
	n := &nemesis{c: c, h: h, tg: tg, rnd: rand.New(rand.NewSource(seed)), bundle: bundle}
	n.run(names, time.Now().Add(dur), t.Logf)

	// Heal: everything resumed and reconnected, mgmt up, every host live.
	for _, id := range c.hosts {
		c.proxies[id].cut(false)
		c.resume(id)
		if c.pid(id) == 0 {
			c.startHost(id)
		}
	}
	c.waitHostsLive(c.hosts...)
	healed := time.Now()
	t.Logf("healed; letting the clients run 15 s more")
	time.Sleep(15 * time.Second)
	close(w.stop)
	w.wg.Wait()
	time.Sleep(2 * time.Second) // in-flight sends land

	// D5: convergence after the nemesis stops.
	d5 := result{ID: "D5", Pass: true}
	cur := tg.get()
	if dd, err := c.waitActive(cur.DeploymentID, 60*time.Second); err != nil {
		d5.fail("deployment not active after healing: %v", err)
	} else {
		cur = dd
	}
	serveBy := time.Now().Add(60 * time.Second)
	for {
		if _, err := getJSON(w.cli, "http://"+cur.Endpoint.MagicDNS+"/reg/get", nil); err == nil {
			break
		}
		if time.Now().After(serveBy) {
			d5.fail("dp on %s not serving 60 s after healing", cur.Host)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	drainBy := time.Now().Add(120 * time.Second)
	for _, q := range []string{"jobs", "jobs-dlq"} {
		for {
			bl, err := c.queueBacklog(q)
			if err == nil && bl == 0 {
				break
			}
			if time.Now().After(drainBy) {
				d5.fail("queue %s not drained 120 s after healing: backlog %d %v", q, bl, err)
				break
			}
			time.Sleep(time.Second)
		}
	}
	time.Sleep(3 * time.Second) // the last deliveries' reports
	// A fresh lock client can take the lock (nothing stuck).
	lockBy := time.Now().Add(30 * time.Second)
	for {
		st, _, err := lockCall(w.cli, "http://"+cur.Endpoint.MagicDNS, "acquire", map[string]any{"ttlMs": 1000})
		if err == nil && (st == 200 || st == 409) {
			break
		}
		if time.Now().After(lockBy) {
			d5.fail("lock service not answering 30 s after healing: %d %v", st, err)
			break
		}
		time.Sleep(time.Second)
	}
	d5.Summary = fmt.Sprintf("converged %.1fs after healing", time.Since(healed).Seconds())

	// The final D1 read.
	final := map[string]bool{}
	var ferr error
	var all struct{ IDs []string }
	for i := 0; ; i++ {
		if _, ferr = getJSON(w.cli, "http://"+cur.Endpoint.MagicDNS+"/d1/all", &all); ferr == nil {
			break
		}
		if i > 30 {
			break
		}
		time.Sleep(time.Second)
	}
	for _, id := range all.IDs {
		final[id] = true
	}
	w.kvFinal() // kv_test.go

	// D7: no workerd outlives its hostd's restart (#432).
	d7 := result{ID: "D7", Pass: true}
	c.mu.Lock()
	for _, p := range c.orphans {
		d7.fail("workerd pid %d still running after its hostd crashed and restarted", p)
	}
	d7.Summary = fmt.Sprintf("%d workerds outlived a hostd restart", len(c.orphans))
	c.mu.Unlock()

	h.mu.Lock()
	res := []result{checkD1(h), checkReg(h), checkD2(h), checkD3(h), checkD4(h, final, ferr), checkKV(h), d5, checkD6(h), d7}
	writeJSON(rdir, "faults.json", h.faults)
	writeJSON(rdir, "store.json", fs.Snapshot())
	h.mu.Unlock()
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
