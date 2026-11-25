//go:build e2edataplane

package dataplane

// The cron-trigger and DO-alarm loop (issue #419, LOOP.md "Cron triggers
// and alarms"). TW_DP_SCENARIO=cron or alarms runs it in place of the dp
// workload. Two workloads of their own (testdata/cron), because the
// guarantees differ by kind:
//
//   - cron (stateless): only hostd's heartbeat-freshness rule
//     (hostd/cron.go cronLeaseFresh) keeps a cut-off host from running its
//     triggers; nothing fences it otherwise.
//   - cronobj (stateful): the same triggers, plus alarm chains on a plain
//     DO (Alm), a facet (FAlm, its alarm delegated to the Host DO) and an
//     Agents SDK agent (Sch, scheduleEvery). The DO lease fences it.
//
// The nemesis is the dp loop's, aimed at cronobj's lease holder; a move or
// takeover moves both workloads (cronRig.moveTo). The checkers are in
// check_cron_test.go.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

var cronScenarios = map[string]bool{
	"alarms": true,
	"cron":   true,
}

const (
	cronSL = "cron"    // the stateless workload
	cronSF = "cronobj" // the stateful one
	// The reschedule fault swaps this trigger for cronSwapB and back.
	cronSwapA = "* * * * *"
	cronSwapB = "*/2 * * * *"
)

// The cron loop's history events (history.jsonl types cron, alarm, sched,
// wldep, window).

type cronEv struct {
	WL, Cron, Run, Phase string
	St                   int64 // scheduledTime, ms
	Epoch                int64
	Node                 string
	At                   time.Time
}

type alarmEv struct {
	Cls, Name, Chain, Kind string
	Seq                    int
	Delay                  int64 // ms until the alarm is due (kind set)
	Epoch                  int64
	Node                   string
	At                     time.Time
}

// schedEv is one deployment of a workload's schedule: deployed at Sent,
// seen active at Active.
type schedEv struct {
	WL           string
	Crons        []string
	Sent, Active time.Time
}

// wlDep is a workload seen active on a host (at a new host or epoch).
type wlDep struct {
	WL    string
	Host  string
	Epoch int64
	At    time.Time
}

// cronWindow bounds what the checkers judge.
type cronWindow struct {
	Start, Healed, End time.Time
}

type cronHist struct {
	h      *history
	runs   []cronEv
	alarms []alarmEv
	scheds []schedEv
	deps   []wlDep
	win    cronWindow
}

func (ch *cronHist) add(typ string, v any) {
	ch.h.mu.Lock()
	defer ch.h.mu.Unlock()
	switch e := v.(type) {
	case cronEv:
		ch.runs = append(ch.runs, e)
	case alarmEv:
		ch.alarms = append(ch.alarms, e)
	case schedEv:
		ch.scheds = append(ch.scheds, e)
	case wlDep:
		ch.deps = append(ch.deps, e)
	case cronWindow:
		ch.win = e
	}
	ch.h.log(typ, v)
}

// serveCronRecorder is the tailnet node the cron runs and alarms report to.
func serveCronRecorder(n *nettest.Node, ch *cronHist) error {
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
		q := r.URL.Query()
		num := func(k string) int64 { v, _ := strconv.ParseInt(q.Get(k), 10, 64); return v }
		switch r.URL.Path {
		case "/cron":
			ch.add("cron", cronEv{WL: q.Get("wl"), Cron: q.Get("cron"), Run: q.Get("run"), Phase: q.Get("phase"),
				St: num("st"), Epoch: num("epoch"), Node: node, At: at})
		case "/alarm":
			ch.add("alarm", alarmEv{Cls: q.Get("cls"), Name: q.Get("name"), Chain: q.Get("chain"), Kind: q.Get("kind"),
				Seq: int(num("seq")), Delay: num("delay"), Epoch: num("epoch"), Node: node, At: at})
		default:
			rw.WriteHeader(404)
			return
		}
		rw.WriteHeader(204)
	}))
	return nil
}

// cronWL is one of the two workloads: its bundles (schedule A and B), the
// one deployed now, and where it runs.
type cronWL struct {
	name    string
	bundles [2]string // base64 tars: schedule A, schedule B
	crons   [2][]string
	cur     int
	tg      *target
}

// cronRig is the cron loop's state, shared by the nemesis's moves and the
// reschedule fault.
type cronRig struct {
	c   *cluster
	ch  *cronHist
	mu  sync.Mutex // one deploy at a time
	sl  *cronWL
	sf  *cronWL
	cli *http.Client

	// clock is the hosts' wall-clock offset file (internal/wallclock's
	// TW_TEST_WALL_CLOCK_FILE): every host follows it.
	clock string
}

// setClock steps every host's wall clock to offset (hostd's, and on Linux
// its workerds' too) and waits for them to read it (polled every 100 ms).
func (r *cronRig) setClock(offset time.Duration) {
	tmp := r.clock + ".tmp"
	os.WriteFile(tmp, []byte(offset.String()), 0o600)
	os.Rename(tmp, r.clock)
	time.Sleep(300 * time.Millisecond)
}

// buildCron bundles a cron workload twice: as written (schedule A) and with
// cronSwapA replaced by cronSwapB (schedule B).
func buildCron(t *testing.T, dir, rec string) (bundles [2]string, crons [2][]string) {
	files, err := hostdtest.Build(dir, map[string]string{"REC": rec})
	if err != nil {
		t.Fatal(err)
	}
	var w map[string]any
	if err := bundle.UnmarshalJSONC(files["wrangler.jsonc"], &w); err != nil {
		t.Fatal(err)
	}
	trig := w["triggers"].(map[string]any)
	orig := trig["crons"].([]any)
	for i := range 2 {
		var cs []string
		for _, c := range orig {
			s := c.(string)
			if i == 1 && s == cronSwapA {
				s = cronSwapB
			}
			cs = append(cs, s)
		}
		trig["crons"] = cs
		b, _ := json.MarshalIndent(w, "", "  ")
		files["wrangler.jsonc"] = b
		bundles[i] = base64.StdEncoding.EncodeToString(hostdtest.MustTar(files))
		crons[i] = cs
	}
	return bundles, crons
}

func (c *cluster) deployWL(wl, bundle, host string, replace, force bool) (deployment, int, []byte, error) {
	var d deployment
	st, b, err := c.try("POST", "/v1/deployments", map[string]any{"tenantID": c.tenantID, "workload": wl, "bundle": bundle,
		"host": host, "replace": replace, "forceTakeover": force}, &d)
	return d, st, b, err
}

// deploy puts w's current bundle on host (retrying refusals until within)
// and waits for it to be active. replace/force as in nemesis.moveTo.
func (r *cronRig) deploy(w *cronWL, host string, replace, force bool, within time.Duration) (deployment, string, error) {
	deadline := time.Now().Add(within)
	refused := map[int]int{}
	start := time.Now()
	for {
		d, st, b, err := r.c.deployWL(w.name, w.bundles[w.cur], host, replace, force)
		if err == nil && st == 202 {
			w.tg.mu.Lock()
			w.tg.depID = d.DeploymentID
			w.tg.mu.Unlock()
			d, err = r.c.waitActive(d.DeploymentID, 120*time.Second)
			if err != nil {
				return d, "", err
			}
			w.tg.set(d)
			return d, fmt.Sprintf("%s accepted after %.1fs (refused %v)", w.name, time.Since(start).Seconds(), refused), nil
		}
		refused[st]++
		if time.Now().After(deadline) {
			return d, "", fmt.Errorf("deploy %s to %s: %d %s %v (refused %v)", w.name, host, st, b, err, refused)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// moveTo moves both workloads to host: the stateful one exactly as the dp
// loop moves dp, then the stateless one. A live move uses --replace for
// both. A takeover deploys the stateless one without --replace, so mgmt
// waits for the old host's lease to run out (45 s), as for any deploy to a
// new host (that wait, past FenceAfter, is what keeps a partition from
// running a trigger twice: docs/cron-triggers.md "Delivery").
func (r *cronRig) moveTo(n *nemesis, host string, live bool, within time.Duration) (deployment, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, how, err := r.deploy(r.sf, host, live, true, within)
	if err != nil {
		return d, "", err
	}
	n.tg.set(d)
	_, how2, err := r.deploy(r.sl, host, live, false, within)
	if err != nil {
		return d, "", err
	}
	return d, how + "; " + how2, nil
}

// cronReschedule redeploys both workloads in place with the other schedule.
// The new schedule must take effect from the first minute after the new
// deployment is active, and the old one must not fire after (C4).
func cronReschedule(n *nemesis, h string) string {
	r := n.cron
	if r == nil {
		return "skipped: not the cron loop"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, w := range []*cronWL{r.sf, r.sl} {
		host := w.tg.get().Host
		w.cur = 1 - w.cur
		sent := time.Now()
		_, how, err := r.deploy(w, host, false, false, 60*time.Second)
		if err != nil {
			out = append(out, "FAILED: "+err.Error())
			continue
		}
		r.ch.add("sched", schedEv{WL: w.name, Crons: w.crons[w.cur], Sent: sent, Active: time.Now()})
		out = append(out, fmt.Sprintf("%s on %s → %v (%s)", w.name, host, w.crons[w.cur], how))
	}
	return fmt.Sprint(out)
}

// pauseBoundary SIGSTOPs the host a few seconds before a minute boundary
// and SIGCONTs it after: either under FenceAfter (10–20 s) or past it but
// under the lease (32–40 s), so no takeover. The minute that came due while
// it was stopped runs at most once, late or not at all (C1).
func pauseBoundary(n *nemesis, h string) string {
	now := time.Now()
	next := now.Truncate(time.Minute).Add(time.Minute)
	lead := n.between(2*time.Second, 8*time.Second)
	d := n.between(10*time.Second, 20*time.Second)
	if n.rnd.Intn(2) == 0 {
		d = n.between(32*time.Second, 40*time.Second)
	}
	if wait := next.Add(-lead).Sub(now); wait > 0 {
		time.Sleep(wait)
	}
	n.c.pause(h, false)
	time.Sleep(d)
	n.c.resume(h)
	return fmt.Sprintf("paused %v from %s", d, next.Add(-lead).Format("15:04:05"))
}

// clockBack steps the hosts' wall clock back 70–150 s, across minutes that
// already ran, holds it 70–100 s and steps it forward again. hostd must not
// rerun a minute (cronLast) or backfill the ones it skipped (C1). Half the
// time hostd is SIGKILLed and restarted while the clock is back.
func clockBack(n *nemesis, h string) string {
	return clockStep(n, h, -n.between(70*time.Second, 150*time.Second))
}

// clockForward steps it forward 70–150 s, holds, and steps it back: the
// future minutes that ran early must not run again when the true clock
// reaches them.
func clockForward(n *nemesis, h string) string {
	return clockStep(n, h, n.between(70*time.Second, 150*time.Second))
}

func clockStep(n *nemesis, h string, d time.Duration) string {
	r := n.cron
	if r == nil || r.clock == "" {
		return "skipped: not the cron loop"
	}
	d = d.Round(time.Second)
	hold := n.between(70*time.Second, 100*time.Second)
	restart := n.rnd.Intn(2) == 0
	r.setClock(d)
	detail := fmt.Sprintf("clock %v for %v", d, hold)
	if restart {
		time.Sleep(hold / 2)
		n.c.stop(h, true)
		time.Sleep(time.Second)
		n.c.startHost(h)
		n.c.waitHostsLive(h)
		detail += fmt.Sprintf(", hostd on %s restarted half way", h)
		time.Sleep(hold / 2)
	} else {
		time.Sleep(hold)
	}
	r.setClock(0)
	return detail
}

// arm starts the stateful workload's alarm chains and agent schedule on
// every new epoch's copy (storage doesn't move with the workload).
func (r *cronRig) arm(stop chan struct{}, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		var armed int64
		tk := time.NewTicker(500 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
			}
			d := r.sf.tg.get()
			if d.Epoch == armed || d.Endpoint.MagicDNS == "" {
				continue
			}
			base := "http://" + d.Endpoint.MagicDNS
			ok := true
			for _, p := range []string{"/arm?cls=alm&name=a0", "/arm?cls=falm&name=f0", "/sch/start?name=s0"} {
				if _, err := getJSON(r.cli, base+p, nil); err != nil {
					ok = false
				}
			}
			if ok {
				armed = d.Epoch
			}
		}
	}()
}

// watch records every new host or epoch of both workloads.
func (r *cronRig) watch(stop chan struct{}, wg *sync.WaitGroup) {
	for _, w := range []*cronWL{r.sl, r.sf} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var last deployment
			tk := time.NewTicker(300 * time.Millisecond)
			defer tk.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tk.C:
				}
				w.tg.mu.Lock()
				id := w.tg.depID
				w.tg.mu.Unlock()
				var d deployment
				if st, _, err := r.c.try("GET", "/v1/deployments/"+id, nil, &d); err != nil || st != 200 {
					continue
				}
				if d.Status != "active" || d.Endpoint.Status != "ready" {
					continue
				}
				w.tg.mu.Lock()
				if w.tg.depID == id {
					w.tg.d = d
					w.tg.seen[d.Endpoint.MagicDNS] = d.Host
				}
				w.tg.mu.Unlock()
				if d.Host != last.Host || d.Epoch != last.Epoch {
					r.ch.add("wldep", wlDep{WL: w.name, Host: d.Host, Epoch: d.Epoch, At: time.Now()})
					last = d
				}
			}
		}()
	}
}

// runCron is TestDataplane for the cron scenarios.
func runCron(t *testing.T, wd, scenario string, names []string, seed int64, dur time.Duration) {
	root, _ := filepath.Abs("results")
	rdir := filepath.Join(root, fmt.Sprintf("%s-%s-%d", time.Now().UTC().Format("20060102T150405Z"), scenario, seed))
	if err := os.MkdirAll(rdir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Logf("artifacts: %s", rdir)
	work, err := hostdtest.ShortDir("tw-dpc-")
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
	// A test build of the binaries, so the clock faults can step every
	// host's wall clock (internal/wallclock; production builds can't). One
	// offset file for all hosts: the harness gives every process the same
	// environment. It's hostd's clock that decides when cron is due, so
	// workerd keeps the true one (no TW_TEST_CLOCK_PRELOAD: with it, every
	// hostd would mirror the shared offset to the same .secs file).
	clock := filepath.Join(work, "clock")
	t.Setenv("TW_TEST_WALL_CLOCK_FILE", clock)
	c := &cluster{t: t, bin: cliflow.BuildBinariesTags(t, "twtestclock"), dir: rdir, work: work, ctl: ctl, tsURL: fakeTS(t).URL,
		sock: filepath.Join(work, "mgmt.sock"), workerd: wd, hosts: []string{"h1", "h2", "h3"},
		proxies: map[string]*sockProxy{}, pids: pids, procs: map[string]*proc{}, stopped: map[string]bool{}}
	t.Cleanup(c.stopAll)
	c.hc = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: unixDialer(c.sock)}}

	nodes := map[string]*nettest.Node{}
	for _, hn := range []string{"dpc-cli", "dpc-rec"} {
		n, err := nettest.NewNode(context.Background(), ctl.URL, nettest.DummyKey, hn, filepath.Join(work, hn))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { n.Close() })
		nodes[hn] = n
	}
	h, err := newHistory(filepath.Join(rdir, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	ch := &cronHist{h: h}
	if err := serveCronRecorder(nodes["dpc-rec"], ch); err != nil {
		t.Fatal(err)
	}
	recURL := "http://" + nodes["dpc-rec"].IP4

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

	r := &cronRig{c: c, ch: ch, cli: nodes["dpc-cli"].HTTPClient(5 * time.Second), clock: clock}
	for _, w := range []struct {
		wl  **cronWL
		nm  string
		dir string
	}{{&r.sl, cronSL, "stateless"}, {&r.sf, cronSF, "stateful"}} {
		app, _ := filepath.Abs(filepath.Join("testdata", "cron", w.dir))
		b, cs := buildCron(t, app, recURL)
		*w.wl = &cronWL{name: w.nm, bundles: b, crons: cs, tg: &target{seen: map[string]string{}}}
	}
	for _, w := range []*cronWL{r.sf, r.sl} {
		sent := time.Now()
		d, _, err := r.deploy(w, "h1", false, false, 60*time.Second)
		if err != nil {
			c.dumpLogs()
			t.Fatal(err)
		}
		ch.add("sched", schedEv{WL: w.name, Crons: w.crons[0], Sent: sent, Active: time.Now()})
		t.Logf("%s on %s at epoch %d (%s)", w.name, d.Host, d.Epoch, d.Endpoint.MagicDNS)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	r.watch(stop, &wg)
	r.arm(stop, &wg)
	start := time.Now()

	// Warm up past one minute boundary, so every trigger has run once on
	// a quiet cluster, then the nemesis.
	time.Sleep(time.Until(time.Now().Truncate(time.Minute).Add(time.Minute + 5*time.Second)))
	n := &nemesis{c: c, h: h, tg: r.sf.tg, rnd: rand.New(rand.NewSource(seed)), cron: r}
	n.run(names, time.Now().Add(dur), t.Logf)

	r.setClock(0)
	for _, id := range c.hosts {
		c.proxies[id].cut(false)
		c.resume(id)
		if c.pid(id) == 0 {
			c.startHost(id)
		}
	}
	c.waitHostsLive(c.hosts...)
	healed := time.Now()
	// Convergence: both workloads active within 60 s, then two more minute
	// boundaries on a quiet cluster for the liveness checks.
	conv := result{ID: "C5", Pass: true}
	for _, w := range []*cronWL{r.sf, r.sl} {
		if _, err := c.waitActive(w.tg.get().DeploymentID, 60*time.Second); err != nil {
			conv.fail("%s not active after healing: %v", w.name, err)
		}
	}
	t.Logf("healed; watching two more minutes")
	time.Sleep(time.Until(time.Now().Truncate(time.Minute).Add(2*time.Minute + 20*time.Second)))
	close(stop)
	wg.Wait()
	ch.add("window", cronWindow{Start: start, Healed: healed, End: time.Now()})

	h.mu.Lock()
	res := checkCron(ch)
	h.mu.Unlock()
	res = append(res, convergence(&conv, ch))
	h.mu.Lock()
	writeJSON(rdir, "faults.json", h.faults)
	h.mu.Unlock()
	writeJSON(rdir, "report.json", map[string]any{"scenario": scenario, "seed": seed, "duration": dur.String(), "results": res})
	failed := false
	for _, x := range res {
		mark := "✓"
		if !x.Pass {
			mark, failed = "✗", true
		}
		t.Logf("%s %s: %s", mark, x.ID, x.Summary)
		for _, e := range x.Evidence {
			t.Logf("    %s", e)
		}
		for _, e := range x.Notes {
			t.Logf("    (note) %s", e)
		}
	}
	if failed {
		t.Errorf("invariants violated; seed %d, scenario %s; artifacts in %s", seed, scenario, rdir)
	}
}

// convergence (C5): after healing, every trigger of the final schedules
// ran, and every alarm chain fired again.
func convergence(r *result, ch *cronHist) result {
	ch.h.mu.Lock()
	defer ch.h.mu.Unlock()
	last := map[string][]string{}
	for _, s := range ch.scheds {
		last[s.WL] = s.Crons
	}
	ran := map[string]bool{}
	for _, e := range ch.runs {
		if e.At.After(ch.win.Healed) {
			ran[e.WL+" "+e.Cron] = true
		}
	}
	for wl, cs := range last {
		for _, c := range cs {
			if !ran[wl+" "+c] {
				r.fail("%s %q never ran after healing", wl, c)
			}
		}
	}
	fired := map[string]bool{}
	for _, a := range ch.alarms {
		if a.Kind == "fire" && a.At.After(ch.win.Healed) {
			fired[a.Cls] = true
		}
	}
	for _, cls := range []string{"alm", "falm", "sch"} {
		if !fired[cls] {
			r.fail("no %s alarm fired after healing", cls)
		}
	}
	r.Summary = fmt.Sprintf("%d of %d triggers ran after healing; alarms fired for %v", len(ran), len(last[cronSL])+len(last[cronSF]), slices.Sorted(maps.Keys(fired)))
	return *r
}

func unixDialer(sock string) func(ctx context.Context, _, _ string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}
}
