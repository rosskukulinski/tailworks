//go:build e2edataplane

package dataplane

// The Workflows and approvals slice of the loop (#418, ledger/workflows.md):
// the same cluster and nemesis as TestDataplane, with testdata/wf deployed
// in place of testdata/dp. It is deployed under the workload name "dp", the
// name the harness's deploy and the nemesis's moves use, so every fault
// applies to it unchanged.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// ---- the history of the Workflows workload ----

type wfCreate struct {
	ID         string
	Sent, Recv time.Time
	Host       string // the host whose endpoint was called
	Status     int
	OK         bool
	Err        string `json:",omitempty"`
}

// wfDo is one step side effect, as the recorder received it.
type wfDo struct {
	Inst, Step, Call string
	N                int // the recorder's count of calls for (Inst, Step), this one included
	At               time.Time
	Node             string
}

// wfNote is one observation run() reported (repeated on every replay).
type wfNote struct {
	Inst, Kind string
	Step, Call string `json:",omitempty"`
	V          int    `json:",omitempty"`
	Deadline   int64  `json:",omitempty"` // unix ms
	Ev         string `json:",omitempty"`
	Approved   bool   `json:",omitempty"`
	NodeID     string `json:",omitempty"`
	Via        string `json:",omitempty"`
	Comment    string `json:",omitempty"`
	Error      string `json:",omitempty"`
	Version    string `json:",omitempty"`
	At         time.Time
	Node       string
}

// wfEvent is one sendEvent the driver made.
type wfEvent struct {
	Inst       string
	Sent, Recv time.Time
	Host       string
	Status     int
	Err        string `json:",omitempty"`
}

// wfDecision is one POST /_tw/approvals/chaos/<inst>.
type wfDecision struct {
	Inst, Client, NodeID, Decision string
	Sent, Recv                     time.Time
	Host                           string
	Status                         int
	Code                           string `json:",omitempty"`
	Err                            string `json:",omitempty"`
}

// wfFinal is an instance's state at the end (status read on the final copy).
type wfFinal struct {
	Inst   string
	Code   int // 200, or 404 when the final copy doesn't know it
	Status string
	Output json.RawMessage `json:",omitempty"`
	Error  json.RawMessage `json:",omitempty"`
}

// wfAudit is one approval_* audit record (mgmt's merged log).
type wfAudit struct {
	Kind, Chain, Instance, Step, Decision, ApproverNodeID string
	At                                                    time.Time
}

// wfListed is one approval mgmt lists (GET /v1/tenants/{id}/approvals).
type wfListed struct {
	Instance string `json:"instance"`
	Host     string `json:"host"`
	Status   string `json:"status"`
}

type wfHist struct {
	h        *history // its mutex and history.jsonl
	creates  []wfCreate
	dos      []wfDo
	notes    []wfNote
	events   []wfEvent
	decs     []wfDecision
	finals   []wfFinal
	audits   []wfAudit
	listed   []wfListed        // pending approvals mgmt lists at the end
	nodeIDs  map[string]string // approver client -> its stable node ID
	finalDep string            // the host of the final copy
}

func (w *wfHist) add(typ string, v any, f func()) {
	w.h.mu.Lock()
	f()
	w.h.log(typ, v)
	w.h.mu.Unlock()
}

// ---- the fault this slice adds ----

// wfAltBundle is a second build of the workload (another VERSION), so a
// redeploy really replaces the code. Empty outside TestDataplaneWorkflows.
var wfAltBundle string

// redeployFault redeploys the workload onto the host it runs on (deploy
// --replace), with the other build when there is one: Workflow instances
// must survive it (docs/approvals.md).
var redeployFault = fault{"redeploy", func(n *nemesis, h string) string {
	if wfAltBundle != "" {
		n.bundle, wfAltBundle = wfAltBundle, n.bundle
	}
	d, how, err := n.moveTo(h, true, 60*time.Second)
	if err != nil {
		return "FAILED: " + err.Error()
	}
	return fmt.Sprintf("on %s, epoch %d, %s", d.Host, d.Epoch, how)
}}

// ---- the test ----

// TestDataplaneWorkflows runs a workflows scenario (TW_DP_SCENARIO
// workflows | workflows-move). With TW_DP_SCENARIO unset (make
// e2e-dataplane, nightly) it runs workflows on an even seed and
// workflows-move on an odd one. TW_DP_DURATION defaults to 3m here.
func TestDataplaneWorkflows(t *testing.T) {
	scenario := os.Getenv("TW_DP_SCENARIO")
	if scenario != "" && !strings.HasPrefix(scenario, "workflows") {
		t.Skip("TW_DP_SCENARIO " + scenario + " is TestDataplane's")
	}
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	if !hostdtest.HasNode() {
		t.Skip("node not available (needed to bundle the app)")
	}
	seed, err := strconv.ParseInt(envOr("TW_DP_SEED", time.Now().UTC().Format("20060102")), 10, 64)
	if err != nil {
		t.Fatalf("TW_DP_SEED: %v", err)
	}
	if scenario == "" {
		scenario = "workflows"
		if seed%2 == 1 {
			scenario = "workflows-move"
		}
	}
	names, ok := scenarios[scenario]
	if !ok {
		t.Fatalf("TW_DP_SCENARIO %q: workflows or workflows-move", scenario)
	}
	dur, err := time.ParseDuration(envOr("TW_DP_DURATION", "3m"))
	if err != nil {
		t.Fatalf("TW_DP_DURATION: %v", err)
	}
	if fs := os.Getenv("TW_DP_FAULTS"); fs != "" {
		names = strings.Split(fs, ",")
		for _, n := range names {
			if _, ok := faults[n]; !ok {
				t.Fatalf("TW_DP_FAULTS: unknown fault %q", n)
			}
		}
	}
	t.Logf("scenario %s, seed %d, duration %v, faults %v", scenario, seed, dur, names)

	root, _ := filepath.Abs("results")
	rdir := filepath.Join(root, fmt.Sprintf("%s-%s-%d", time.Now().UTC().Format("20060102T150405Z"), scenario, seed))
	if err := os.MkdirAll(rdir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Logf("artifacts: %s", rdir)
	work, err := hostdtest.ShortDir("tw-dpwf-")
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
	// Every node may decide the chaos Workflow's approvals.
	ctl.Server.SetGlobalAppCaps(tailcfg.PeerCapMap{
		nettest.TestCap:  []tailcfg.RawMessage{nettest.TestCapValue},
		hostd.ApproveCap: []tailcfg.RawMessage{`{"workflows":["chaos"]}`},
	})

	c := &cluster{t: t, bin: cliflow.BuildBinaries(t), dir: rdir, work: work, ctl: ctl, tsURL: fakeTS(t).URL,
		sock: filepath.Join(work, "mgmt.sock"), workerd: wd, hosts: []string{"h1", "h2", "h3"},
		proxies: map[string]*sockProxy{}, pids: pids, procs: map[string]*proc{}, stopped: map[string]bool{}}
	t.Cleanup(c.stopAll)
	c.hc = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.sock)
	}}}

	// Tailnet nodes: the client, two approvers, the recorder.
	nodes := map[string]*nettest.Node{}
	var nmu sync.Mutex
	var wg sync.WaitGroup
	for _, hn := range []string{"dp-cli", "dp-apa", "dp-apb", "dp-rec"} {
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
	wh := &wfHist{h: h, nodeIDs: map[string]string{}}
	for _, a := range []string{"A", "B"} {
		ctx, cancel := ctxTimeout(10 * time.Second)
		st, err := nodes["dp-ap"+strings.ToLower(a)].LC.Status(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		wh.nodeIDs[a] = string(st.Self.ID)
	}
	h.log("wf-approvers", wh.nodeIDs)
	drv := &wfDriver{wh: wh, stop: make(chan struct{}), busy: map[string]bool{}, last: map[string]time.Time{}, finished: map[string]bool{},
		cli: nodes["dp-cli"].HTTPClient(10 * time.Second), stat: nodes["dp-cli"].HTTPClient(3 * time.Second),
		apps: map[string]*http.Client{"A": nodes["dp-apa"].HTTPClient(10 * time.Second), "B": nodes["dp-apb"].HTTPClient(10 * time.Second)}}
	if err := serveWFRecorder(nodes["dp-rec"], wh, drv); err != nil {
		t.Fatal(err)
	}
	recURL := "http://" + nodes["dp-rec"].IP4

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

	app, _ := filepath.Abs(filepath.Join("testdata", "wf"))
	build := func(version string) string {
		files, err := hostdtest.Build(app, map[string]string{"REC": recURL, "VERSION": version})
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(hostdtest.MustTar(files))
	}
	bundle := build("1")
	wfAltBundle = build("2")
	t.Cleanup(func() { wfAltBundle = "" })
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
	drv.tg = tg
	t.Logf("wf on %s at epoch %d (%s)", d.Host, d.Epoch, d.Endpoint.MagicDNS)
	for i := 0; ; i++ {
		if _, err := getJSON(drv.cli, "http://"+d.Endpoint.MagicDNS+"/ready", nil); err == nil {
			break
		}
		if i > 150 {
			c.dumpLogs()
			t.Fatal("wf never answered")
		}
		time.Sleep(200 * time.Millisecond)
	}
	w := &workload{c: c, h: h, tg: tg, stop: make(chan struct{})}
	w.watch()
	creating := make(chan struct{})
	drv.create(creating)
	drv.nudge()

	time.Sleep(10 * time.Second)
	n := &nemesis{c: c, h: h, tg: tg, rnd: rand.New(rand.NewSource(seed)), bundle: bundle}
	n.run(names, time.Now().Add(dur), t.Logf)

	for _, id := range c.hosts {
		c.proxies[id].cut(false)
		c.resume(id)
		if c.pid(id) == 0 {
			c.startHost(id)
		}
	}
	c.waitHostsLive(c.hosts...)
	healed := time.Now()
	t.Logf("healed; creating for 10 s more, then waiting for every instance to finish")
	time.Sleep(10 * time.Second)
	close(creating)

	// W5: convergence. Every instance ends complete, errored or terminated,
	// or is gone (not found) from the final copy because its storage is on
	// another host.
	w5 := result{ID: "W5", Pass: true}
	cur := tg.get()
	if dd, err := c.waitActive(cur.DeploymentID, 60*time.Second); err != nil {
		w5.fail("deployment not active after healing: %v", err)
	} else {
		cur = dd
		tg.set(dd)
	}
	finals := drv.converge(cur, 180*time.Second)
	close(drv.stop)
	drv.wg.Wait()
	close(w.stop)
	w.wg.Wait()
	time.Sleep(2 * time.Second)
	wh.add("wf-final-host", cur.Host, func() { wh.finalDep = cur.Host })
	for _, f := range finals {
		wh.add("wf-final", f, func() { wh.finals = append(wh.finals, f) })
	}
	w5.Summary = fmt.Sprintf("converged (or timed out) %.1fs after healing", time.Since(healed).Seconds())
	auditErr := wh.fetchAudit(c)
	// What `tw approvals` lists as still waiting, after everything finished.
	var al struct {
		Approvals []wfListed `json:"approvals"`
	}
	if st, b, err := c.try("GET", "/v1/tenants/"+c.tenantID+"/approvals", nil, &al); err != nil || st != 200 {
		t.Logf("listing approvals: %d %s %v", st, b, err)
	}
	for _, a := range al.Approvals {
		wh.add("wf-listed", a, func() { wh.listed = append(wh.listed, a) })
	}

	h.mu.Lock()
	res := []result{checkW1(wh), checkW2(wh, h.faults, h.deps), checkW3(wh, h.faults, auditErr), checkW4(wh, h.deps, h.faults), checkW5(wh, w5)}
	writeJSON(rdir, "faults.json", h.faults)
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

// ---- the driver ----

type wfDriver struct {
	wh   *wfHist
	tg   *target
	cli  *http.Client
	stat *http.Client            // status reads (short timeout)
	apps map[string]*http.Client // the approvers
	stop chan struct{}
	wg   sync.WaitGroup
	mu   sync.Mutex
	busy map[string]bool // kind/inst with a reaction running
	ids  []string        // instances whose create was sent

	last     map[string]time.Time // inst -> its last report (or its create)
	finished map[string]bool      // inst -> its final step reported
}

// touch records a report from inst.
func (d *wfDriver) touch(inst string, final bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.last[inst] = time.Now()
	if final {
		d.finished[inst] = true
	}
}

// nudge covers reports lost in a fault: a run that reached a wait while
// its host's egress was down (a hostd crash) never said so, and isn't run
// again until something arrives. Every 5 s, an unfinished instance quiet
// for 15 s gets its status read; if it waits, the driver reacts as if it
// had reported.
func (d *wfDriver) nudge() {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-d.stop:
				return
			case <-tk.C:
			}
			var quiet []string
			d.mu.Lock()
			for _, id := range d.ids {
				if !d.finished[id] && time.Since(d.last[id]) > 15*time.Second {
					quiet = append(quiet, id)
					d.last[id] = time.Now()
				}
			}
			d.mu.Unlock()
			b, _ := d.base()
			for id, f := range d.statuses(b, quiet) {
				if f.Code == 200 && f.Status == "waiting" {
					d.wh.add("wf-nudge", id, func() {})
					d.react("event", id, func() { d.sendEvent(id) })
					d.react("approval", id, func() { d.decide(id) })
				}
			}
		}
	}()
}

func (d *wfDriver) base() (string, string) {
	dep := d.tg.get()
	return "http://" + dep.Endpoint.MagicDNS, dep.Host
}

// instNum is the instance's sequence number (wf00042 → 42).
func instNum(inst string) int {
	n, _ := strconv.Atoi(strings.TrimLeft(strings.TrimPrefix(inst, "wf"), "0"))
	return n
}

// create starts an instance every 1.5 s until done is closed. Every third
// fails step s1 once; sleeps are 1–5 s.
func (d *wfDriver) create(done chan struct{}) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		tk := time.NewTicker(1500 * time.Millisecond)
		defer tk.Stop()
		for i := 1; ; i++ {
			select {
			case <-done:
				return
			case <-tk.C:
			}
			id := fmt.Sprintf("wf%05d", i)
			fail := -1
			if i%3 == 0 {
				fail = 1
			}
			sleep := 1000 + (i%5)*1000
			d.mu.Lock()
			d.ids = append(d.ids, id)
			d.last[id] = time.Now()
			d.mu.Unlock()
			go func() {
				b, host := d.base()
				o := wfCreate{ID: id, Sent: time.Now(), Host: host}
				code, err := getJSON(d.cli, fmt.Sprintf("%s/create?id=%s&steps=3&fail=%d&sleep=%d", b, id, fail, sleep), nil)
				o.Recv, o.Status, o.OK = time.Now(), code, err == nil
				if err != nil {
					o.Err = err.Error()
				}
				d.wh.add("wf-create", o, func() { d.wh.creates = append(d.wh.creates, o) })
			}()
		}
	}()
}

// react runs f once per (kind, inst) at a time.
func (d *wfDriver) react(kind, inst string, f func()) {
	k := kind + "/" + inst
	d.mu.Lock()
	if d.busy[k] {
		d.mu.Unlock()
		return
	}
	d.busy[k] = true
	d.mu.Unlock()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer func() { d.mu.Lock(); delete(d.busy, k); d.mu.Unlock() }()
		f()
	}()
}

func (d *wfDriver) stopped() bool {
	select {
	case <-d.stop:
		return true
	default:
		return false
	}
}

// sendEvent delivers "go" to inst, retrying until the current copy accepts
// it or says the instance isn't there (3 times).
func (d *wfDriver) sendEvent(inst string) {
	time.Sleep(time.Duration(rand.Intn(1500)) * time.Millisecond)
	notFound := 0
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline) && !d.stopped(); {
		b, host := d.base()
		o := wfEvent{Inst: inst, Sent: time.Now(), Host: host}
		code, err := getJSON(d.cli, b+"/event?id="+inst+"&v=ev-"+inst, nil)
		o.Recv, o.Status = time.Now(), code
		if err != nil {
			o.Err = err.Error()
		}
		d.wh.add("wf-event", o, func() { d.wh.events = append(d.wh.events, o) })
		if code == 200 {
			return
		}
		if code == 404 {
			if notFound++; notFound >= 3 {
				return
			}
		}
		time.Sleep(time.Second)
	}
}

// decide decides inst's approval: every 4th is rejected; every 5th is
// decided twice at once, approve by A and reject by B (only one may win:
// W3); the rest by A or B alternately.
func (d *wfDriver) decide(inst string) {
	time.Sleep(time.Duration(rand.Intn(2000)) * time.Millisecond)
	n := instNum(inst)
	dec := "approve"
	if n%4 == 3 {
		dec = "reject"
	}
	if n%5 == 0 {
		var g sync.WaitGroup
		for _, x := range []struct{ c, dec string }{{"A", "approve"}, {"B", "reject"}} {
			g.Add(1)
			go func() { defer g.Done(); d.decideAs(inst, x.c, x.dec) }()
		}
		g.Wait()
		return
	}
	d.decideAs(inst, map[bool]string{true: "A", false: "B"}[n%2 == 0], dec)
}

func (d *wfDriver) decideAs(inst, client, dec string) {
	hc := d.apps[client]
	gone := 0
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline) && !d.stopped(); {
		b, host := d.base()
		body, _ := json.Marshal(map[string]any{"decision": dec, "comment": client + " " + dec + "s " + inst})
		o := wfDecision{Inst: inst, Client: client, NodeID: d.wh.nodeIDs[client], Decision: dec, Sent: time.Now(), Host: host}
		resp, err := hc.Post(b+"/_tw/approvals/chaos/"+inst, "application/json", bytes.NewReader(body))
		var e struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err == nil {
			rb, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			o.Status = resp.StatusCode
			json.Unmarshal(rb, &e)
			o.Code = e.Code
			if resp.StatusCode != 200 {
				o.Err = strings.TrimSpace(string(rb))
				if len(o.Err) > 300 {
					o.Err = o.Err[:300]
				}
			}
		} else {
			o.Err = err.Error()
		}
		o.Recv = time.Now()
		d.wh.add("wf-decision", o, func() { d.wh.decs = append(d.wh.decs, o) })
		switch {
		case o.Status == 200:
			return
		case o.Code == "approval_not_pending" && !strings.Contains(o.Err, "is waiting here"):
			return // decided (by the other approver, or an earlier try whose answer was lost)
		case o.Code == "approval_not_pending", o.Code == "unknown_object":
			if gone++; gone >= 60 { // not (yet) reported on this copy
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// statuses reads the instances' status from base, 16 at a time, each with
// a 3 s timeout (a stalled copy mustn't hold the driver up).
func (d *wfDriver) statuses(base string, ids []string) map[string]wfFinal {
	out := map[string]wfFinal{}
	var mu sync.Mutex
	var g sync.WaitGroup
	sem := make(chan struct{}, 16)
	for _, id := range ids {
		g.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; g.Done() }()
			var st struct {
				Status string          `json:"status"`
				Output json.RawMessage `json:"output"`
				Error  json.RawMessage `json:"error"`
			}
			code, err := getJSON(d.stat, base+"/status?id="+id, &st)
			f := wfFinal{Inst: id, Code: code, Status: st.Status, Output: st.Output, Error: st.Error}
			if err != nil && code != 404 {
				f.Status = "unreadable: " + err.Error()
			}
			mu.Lock()
			out[id] = f
			mu.Unlock()
		}()
	}
	g.Wait()
	return out
}

// converge polls every instance's status on the final copy until each is
// complete, errored or terminated, or not found there 5 times in a row,
// or the time runs out.
func (d *wfDriver) converge(cur deployment, within time.Duration) []wfFinal {
	d.mu.Lock()
	ids := append([]string(nil), d.ids...)
	d.mu.Unlock()
	last := map[string]wfFinal{}
	missing := map[string]int{}
	done := map[string]bool{}
	base := "http://" + cur.Endpoint.MagicDNS
	for deadline := time.Now().Add(within); time.Now().Before(deadline); {
		left := 0
		var todo []string
		for _, id := range ids {
			if !done[id] {
				todo = append(todo, id)
			}
		}
		for id, f := range d.statuses(base, todo) {
			last[id] = f
			switch {
			case f.Code == 200 && (f.Status == "complete" || f.Status == "errored" || f.Status == "terminated"):
				done[id] = true
			case f.Code == 404:
				if missing[id]++; missing[id] >= 5 {
					done[id] = true
				}
			default:
				missing[id] = 0
				left++
			}
		}
		if left == 0 && len(done) == len(ids) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	out := make([]wfFinal, 0, len(ids))
	for _, id := range ids {
		out = append(out, last[id])
	}
	return out
}

// serveWFRecorder is the recorder node for the Workflows workload: step
// side effects (/wf/do) and run()'s observations (/wf/note). A note that an
// instance waits for its event or its approval starts the driver's
// reaction.
func serveWFRecorder(n *nettest.Node, wh *wfHist, drv *wfDriver) error {
	ln, err := n.Listen("tcp", ":80")
	if err != nil {
		return err
	}
	var mu sync.Mutex
	calls := map[string]int{}
	go http.Serve(ln, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		at := time.Now()
		node := ""
		if who, err := n.LC.WhoIs(r.Context(), r.RemoteAddr); err == nil {
			node = who.Node.Name
		}
		switch r.URL.Path {
		case "/wf/do":
			var x struct{ Inst, Step, Call string }
			if err := json.NewDecoder(r.Body).Decode(&x); err != nil {
				rw.WriteHeader(400)
				return
			}
			mu.Lock()
			calls[x.Inst+"/"+x.Step]++
			c := calls[x.Inst+"/"+x.Step]
			mu.Unlock()
			o := wfDo{Inst: x.Inst, Step: x.Step, Call: x.Call, N: c, At: at, Node: node}
			drv.touch(x.Inst, false)
			wh.add("wf-do", o, func() { wh.dos = append(wh.dos, o) })
			rw.Header().Set("content-type", "application/json")
			json.NewEncoder(rw).Encode(map[string]any{"n": c, "at": at.UnixMilli()})
		case "/wf/note":
			var o wfNote
			if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
				rw.WriteHeader(400)
				return
			}
			o.At, o.Node = at, node
			drv.touch(o.Inst, o.Kind == "seen" && o.Step == "final")
			wh.add("wf-note", o, func() { wh.notes = append(wh.notes, o) })
			switch o.Kind {
			case "waiting":
				drv.react("event", o.Inst, func() { drv.sendEvent(o.Inst) })
			case "approval-wait":
				drv.react("approval", o.Inst, func() { drv.decide(o.Inst) })
			}
			rw.WriteHeader(204)
		default:
			rw.WriteHeader(404)
		}
	}))
	return nil
}

// fetchAudit reads the tenant's approval_* audit records from mgmt (every
// host's chain), waiting for the hosts' batches to arrive.
func (wh *wfHist) fetchAudit(c *cluster) error {
	time.Sleep(5 * time.Second) // the hosts' audit shipping
	var out []wfAudit
	cursor := ""
	for {
		q := url.Values{"tenant": {c.tenantID}, "limit": {"500"}}
		for _, k := range []string{"approval_requested", "approval_decision", "approval_denied", "approval_timeout"} {
			q.Add("kind", k)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page struct {
			Items []struct {
				Kind   string          `json:"kind"`
				Chain  string          `json:"chain"`
				TS     time.Time       `json:"ts"`
				Detail json.RawMessage `json:"detail"`
			} `json:"items"`
			NextCursor *string `json:"nextCursor"`
		}
		st, b, err := c.try("GET", "/v1/audit?"+q.Encode(), nil, &page)
		if err != nil || st != 200 {
			return fmt.Errorf("GET /v1/audit: %d %s %v", st, b, err)
		}
		for _, it := range page.Items {
			var d struct {
				Instance, Step, Decision string
				Approver                 struct {
					NodeID string `json:"nodeId"`
				}
			}
			json.Unmarshal(it.Detail, &d)
			out = append(out, wfAudit{Kind: it.Kind, Chain: it.Chain, Instance: d.Instance, Step: d.Step, Decision: d.Decision, ApproverNodeID: d.Approver.NodeID, At: it.TS})
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
	}
	for _, a := range out {
		wh.add("wf-audit", a, func() { wh.audits = append(wh.audits, a) })
	}
	return nil
}
