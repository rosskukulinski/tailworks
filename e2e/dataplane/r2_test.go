//go:build e2edataplane

package dataplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/chaoskit"
	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// R2 under faults (issue #420, ledger/r2.md). The same cluster as
// TestDataplane (mgmt and three hosts, each through a cuttable proxy) runs
// testdata/r2 with one of two blob backends: fs (each host's own
// directory) or s3 (one Garage container, which every host reaches
// through a proxy of its own, so the nemesis can cut or slow one host's
// path to it). Scenarios (nemesis_test.go):
//
//	r2-fs        one fenced copy (a DO class), fs: moves, takeovers, crashes, mgmt cut-offs
//	r2-s3        the same on s3, plus S3 cut-offs and slowness
//	r2-replicas  "replicas": 2 (stateless, no fence), s3: both replicas serve the same bucket
//
// Run one: TW_DP_SCENARIO=r2-s3 TW_DP_SEED=1 go test -tags e2edataplane
// -run TestR2 ./e2e/dataplane/. Without TW_DP_SCENARIO, all three.

// garageImage is the S3 backend. MinIO no longer publishes images; Garage
// is one of the S3-compatible services docs/r2.md names.
const garageImage = "dxflrs/garage:v2.1.0"

// Garage's test credentials (key IDs are "GK" + 24 hex).
const (
	garageKeyID  = "GK0123456789abcdef01234567"
	garageSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	garageBucket = "tw-r2"
)

// r2rig is the running R2 scenario, for its faults (nemesis_test.go has
// only the cluster and the target).
var r2rig *r2Rig

type r2Rig struct {
	t        *testing.T
	backend  string // fs | s3
	replicas bool
	c        *cluster
	h        *history
	rh       *r2Hist
	w        *r2Workload
	tg       *target
	s3       map[string]*chaoskit.Proxy // host → its path to Garage
	garage   *chaoskit.Container

	mu   sync.Mutex
	reps []deployment // r2-replicas: every replica's deployment
}

func TestR2(t *testing.T) {
	scen := os.Getenv("TW_DP_SCENARIO")
	list := []string{"r2-fs", "r2-s3", "r2-replicas"}
	if scen != "" {
		if !strings.HasPrefix(scen, "r2-") {
			t.Skipf("TW_DP_SCENARIO=%s is TestDataplane's", scen)
		}
		list = []string{scen}
	}
	for _, s := range list {
		if _, ok := scenarios[s]; !ok {
			t.Fatalf("unknown scenario %s", s)
		}
		t.Run(s, func(t *testing.T) { runR2(t, s) })
	}
}

// startGarage starts a one-node Garage with the bucket and key the hosts
// use, and returns its S3 address (127.0.0.1:port).
func startGarage(t *testing.T, rdir string, c *cluster) (*chaoskit.Container, string) {
	ctx := chaoskit.DockerContext()
	if err := chaoskit.HasDocker(ctx); err != nil {
		t.Skipf("the s3 scenarios need Docker: %v", err)
	}
	conf := filepath.Join(rdir, "garage.toml")
	secret := make([]byte, 32)
	rand.New(rand.NewSource(time.Now().UnixNano())).Read(secret)
	os.WriteFile(conf, []byte(`metadata_dir = "/var/lib/garage/meta"
data_dir = "/var/lib/garage/data"
db_engine = "sqlite"
replication_factor = 1
rpc_bind_addr = "[::]:3901"
rpc_public_addr = "127.0.0.1:3901"
rpc_secret = "`+fmt.Sprintf("%x", secret)+`"
[s3_api]
s3_region = "us-east-1"
api_bind_addr = "[::]:3900"
`), 0o644)
	g, err := chaoskit.Run(ctx, "-p", "127.0.0.1::3900", "-v", conf+":/etc/garage.toml:ro", garageImage)
	if err != nil {
		t.Fatal(err)
	}
	c.record("container/garage", 0)
	fmt.Fprintf(c.pids, "container %s\n", g.ID)
	t.Cleanup(func() {
		os.WriteFile(filepath.Join(rdir, "garage.log"), g.Logs(), 0o600)
		g.Remove()
	})
	var node string
	for i := 0; ; i++ {
		out, err := g.Exec("/garage", "node", "id", "-q")
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			node, _, _ = strings.Cut(lines[len(lines)-1], "@")
			break
		}
		if i > 60 {
			t.Fatalf("garage did not start: %s", out)
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, args := range [][]string{
		{"layout", "assign", "-z", "dc1", "-c", "1G", node},
		{"layout", "apply", "--version", "1"},
		{"bucket", "create", garageBucket},
		{"key", "import", "--yes", "-n", "tw", garageKeyID, garageSecret},
		{"bucket", "allow", "--read", "--write", "--owner", garageBucket, "--key", "tw"},
	} {
		if out, err := g.Exec(append([]string{"/garage"}, args...)...); err != nil {
			t.Fatalf("garage %v: %v: %s", args, err, out)
		}
	}
	addr, err := g.Port("3900/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return g, addr
}

// replicaBundle is testdata/r2 as a stateless workload with two replicas:
// no DO class, "replicas": 2.
func replicaBundle(t *testing.T, files map[string][]byte) {
	var w map[string]any
	if err := bundle.UnmarshalJSONC(files["wrangler.jsonc"], &w); err != nil {
		t.Fatal(err)
	}
	delete(w, "durable_objects")
	delete(w, "migrations")
	files["wrangler.jsonc"], _ = json.MarshalIndent(w, "", "  ")
	files["tailworks.jsonc"], _ = json.MarshalIndent(map[string]any{"name": "r2", "replicas": 2, "tailnet": map[string]any{"tags": []string{"tag:ttest"}}}, "", "  ")
}

func runR2(t *testing.T, scenario string) {
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	if !hostdtest.HasNode() {
		t.Skip("node not available (needed to bundle the app)")
	}
	names := scenarios[scenario]
	seed, err := strconv.ParseInt(envOr("TW_DP_SEED", time.Now().UTC().Format("20060102")), 10, 64)
	if err != nil {
		t.Fatalf("TW_DP_SEED: %v", err)
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
	backend := "s3"
	if scenario == "r2-fs" {
		backend = "fs"
	}
	replicas := scenario == "r2-replicas"
	t.Logf("scenario %s, seed %d, duration %v, backend %s, faults %v", scenario, seed, dur, backend, names)

	root, _ := filepath.Abs("results")
	rdir := filepath.Join(root, fmt.Sprintf("%s-%s-%d", time.Now().UTC().Format("20060102T150405Z"), scenario, seed))
	if err := os.MkdirAll(rdir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Logf("artifacts: %s", rdir)
	work, err := hostdtest.ShortDir("tw-r2-")
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

	c := &cluster{t: t, bin: cliflow.BuildBinaries(t), dir: rdir, work: work, ctl: ctl, tsURL: fakeTS(t).URL,
		sock: filepath.Join(work, "mgmt.sock"), workerd: wd, hosts: []string{"h1", "h2", "h3"},
		proxies: map[string]*sockProxy{}, pids: pids, procs: map[string]*proc{}, stopped: map[string]bool{}, workload: "r2"}
	t.Cleanup(c.stopAll)
	c.hc = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.sock)
	}}}
	rig := &r2Rig{t: t, backend: backend, replicas: replicas, c: c, s3: map[string]*chaoskit.Proxy{}}
	if backend == "s3" {
		g, addr := startGarage(t, rdir, c)
		rig.garage = g
		creds := filepath.Join(work, "r2.env")
		os.WriteFile(creds, []byte("TW_R2_S3_ACCESS_KEY_ID="+garageKeyID+"\nTW_R2_S3_SECRET_ACCESS_KEY="+garageSecret+"\n"), 0o600)
		for _, id := range c.hosts {
			p, err := chaoskit.Listen(addr)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(p.Close)
			rig.s3[id] = p
		}
		c.hostArgs = func(id string) []string {
			return []string{"-r2-backend", "s3", "-r2-s3-endpoint", "http://" + rig.s3[id].Addr(), "-r2-s3-bucket", garageBucket, "-r2-s3-credentials-file", creds}
		}
	}

	cliNode, err := nettest.NewNode(context.Background(), ctl.URL, nettest.DummyKey, "dp-cli", filepath.Join(work, "dp-cli"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cliNode.Close() })
	h, err := newHistory(filepath.Join(rdir, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	rig.h = h
	rig.rh = &r2Hist{h: h}

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

	app, _ := filepath.Abs(filepath.Join("testdata", "r2"))
	files, err := hostdtest.Build(app, nil)
	if err != nil {
		t.Fatal(err)
	}
	if replicas {
		replicaBundle(t, files)
	}
	bundleB64 := base64.StdEncoding.EncodeToString(hostdtest.MustTar(files))
	tg := &target{seen: map[string]string{}}
	rig.tg = tg
	host := "h1"
	if replicas {
		host = ""
	}
	d, st, b, err := c.deploy(bundleB64, host, false, false)
	if err != nil || st != 202 {
		c.dumpLogs()
		t.Fatalf("deploy: %d %s %v", st, b, err)
	}
	if replicas {
		var resp struct {
			Replicas []struct{ Workload, DeploymentID, Host string }
		}
		json.Unmarshal(b, &resp)
		if len(resp.Replicas) != 2 {
			t.Fatalf("deploy: want 2 replicas: %s", b)
		}
		for _, r := range resp.Replicas {
			rd, err := c.waitActive(r.DeploymentID, 120*time.Second)
			if err != nil {
				c.dumpLogs()
				t.Fatal(err)
			}
			rig.reps = append(rig.reps, rd)
			tg.mu.Lock()
			tg.seen[rd.Endpoint.MagicDNS] = rd.Host
			tg.mu.Unlock()
			h.dep(depEv{At: time.Now(), Host: rd.Host, Epoch: rd.Epoch, DNS: rd.Endpoint.MagicDNS})
		}
		tg.set(rig.reps[0])
		t.Logf("r2 replicas on %s and %s", rig.reps[0].Host, rig.reps[1].Host)
	} else {
		tg.depID = d.DeploymentID
		if d, err = c.waitActive(d.DeploymentID, 120*time.Second); err != nil {
			c.dumpLogs()
			t.Fatal(err)
		}
		tg.set(d)
		h.dep(depEv{At: time.Now(), Host: d.Host, Epoch: d.Epoch, DNS: d.Endpoint.MagicDNS})
		t.Logf("r2 on %s at epoch %d (%s)", d.Host, d.Epoch, d.Endpoint.MagicDNS)
	}

	w := &r2Workload{rh: rig.rh, cli: cliNode.HTTPClient(10 * time.Second), big: cliNode.HTTPClient(60 * time.Second),
		stop: make(chan struct{}), seen: tg.endpoints, current: rig.current}
	rig.w = w
	// Wait until the workload answers everywhere before starting the clock.
	for _, dns := range rig.current() {
		for i := 0; ; i++ {
			if o := w.head("warmup", dns, "k0"); o.OK {
				break
			}
			if i > 150 {
				c.dumpLogs()
				t.Fatalf("r2 at %s never answered", dns)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	// Sentinels: written once, never again, so the end checks that an
	// acknowledged put outlives every crash and move (s3), or that after a
	// move the index keeps it but the bytes are gone (fs).
	for i := 0; i < r2Sentinels; i++ {
		for try := 0; ; try++ {
			o := w.put(w.cli, "sentinel", rig.current()[0], fmt.Sprintf("s%d", i), 100+i, 0)
			rig.rh.add(o)
			if o.OK {
				break
			}
			if try > 20 {
				t.Fatalf("sentinel put: %s", o.Err)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	r2rig = rig
	defer func() { r2rig = nil }()
	watchStop := make(chan struct{})
	if replicas {
		go rig.rotate(watchStop)
	} else {
		dw := &workload{c: c, h: h, tg: tg, stop: watchStop}
		dw.watch()
	}
	w.run(seed)

	time.Sleep(10 * time.Second)
	n := &nemesis{c: c, h: h, tg: tg, rnd: rand.New(rand.NewSource(seed)), bundle: bundleB64}
	n.run(names, time.Now().Add(dur), t.Logf)

	// Heal: everything resumed and reconnected, mgmt up, every host live.
	for _, id := range c.hosts {
		c.proxies[id].cut(false)
		if p := rig.s3[id]; p != nil {
			p.Set("pass", 0)
		}
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
	close(watchStop)

	// Convergence, then the reads at quiescence.
	conv := result{ID: "R-conv", Pass: true}
	var curDeps []deployment
	if replicas {
		curDeps = rig.reps
	} else {
		curDeps = []deployment{tg.get()}
	}
	var final []string
	for _, dd := range curDeps {
		got, err := c.waitActive(dd.DeploymentID, 60*time.Second)
		if err != nil {
			conv.fail("deployment not active after healing: %v", err)
			continue
		}
		serveBy := time.Now().Add(60 * time.Second)
		for {
			if o := w.head("conv", got.Endpoint.MagicDNS, "k0"); o.OK {
				break
			}
			if time.Now().After(serveBy) {
				conv.fail("r2 on %s not serving 60 s after healing", got.Host)
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		final = append(final, got.Endpoint.MagicDNS)
	}
	conv.Summary = fmt.Sprintf("converged %.1fs after healing", time.Since(healed).Seconds())
	time.Sleep(3 * time.Second) // waitUntil blob deletes land
	keys := []string{}
	for i := 0; i < r2Keys; i++ {
		keys = append(keys, fmt.Sprintf("k%d", i))
	}
	keys = append(keys, r2MpuKeys...)
	for i := 0; i < r2Sentinels; i++ {
		keys = append(keys, fmt.Sprintf("s%d", i))
	}
	for _, dns := range final {
		lo := w.list("final", dns)
		lo.Final = true
		rig.rh.add(lo)
		for _, k := range keys {
			for _, o := range []r2Op{w.head("final", dns, k), w.get(w.big, "final", dns, k)} {
				o.Final = true
				rig.rh.add(o)
			}
		}
	}
	blobs, blobErr := rig.countBlobs()

	h.mu.Lock()
	fl := append([]faultEv(nil), h.faults...)
	deps := append([]depEv(nil), h.deps...)
	writeJSON(rdir, "faults.json", h.faults)
	h.mu.Unlock()
	in := r2Input{ops: rig.rh.all(), faults: fl, deps: deps, backend: backend, replicas: replicas, blobs: blobs, blobErr: blobErr}
	res := append(checkR2All(in), conv)
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

// current is the endpoints the clients use now: the lease holder's, or
// every replica's.
func (r *r2Rig) current() []string {
	if r.replicas {
		r.mu.Lock()
		defer r.mu.Unlock()
		var out []string
		for _, d := range r.reps {
			out = append(out, d.Endpoint.MagicDNS)
		}
		return out
	}
	if d := r.tg.get(); d.Endpoint.MagicDNS != "" {
		return []string{d.Endpoint.MagicDNS}
	}
	return nil
}

// rotate points the nemesis (which targets tg's host) at replica i mod 2
// after its i-th fault, so both replicas' hosts get faults and a seed
// replays the same way.
func (r *r2Rig) rotate(stop chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-time.After(200 * time.Millisecond):
		}
		r.h.mu.Lock()
		n := len(r.h.faults)
		r.h.mu.Unlock()
		r.mu.Lock()
		d := r.reps[n%len(r.reps)]
		r.mu.Unlock()
		if r.tg.get().DeploymentID != d.DeploymentID {
			r.tg.set(d)
		}
	}
}

// countBlobs is how many blobs the backend holds (every host's directory
// with fs), for R5's orphan report.
func (r *r2Rig) countBlobs() (int, error) {
	if r.backend == "fs" {
		n := 0
		for _, id := range r.c.hosts {
			filepath.Walk(filepath.Join(r.c.work, id, "data", "r2"), func(p string, fi os.FileInfo, err error) error {
				if err == nil && !fi.IsDir() && len(fi.Name()) == 32 {
					n++
				}
				return nil
			})
		}
		return n, nil
	}
	out, err := r.garage.Exec("/garage", "bucket", "info", garageBucket)
	if err != nil {
		return 0, fmt.Errorf("garage bucket info: %v: %s", err, out)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "Objects:" {
			return strconv.Atoi(f[1])
		}
	}
	return 0, fmt.Errorf("garage bucket info: no object count in %q", out)
}

// ---- faults (registered in nemesis_test.go) ----

// r2CutS3 cuts host h off from the S3 backend for 3–20 s: connections
// held without a byte (blackhole) or dropped and refused (reset).
func r2CutS3(n *nemesis, h string) string {
	if r2rig == nil || r2rig.s3[h] == nil {
		return "no s3 backend"
	}
	p := r2rig.s3[h]
	if p == nil {
		return "no s3 backend"
	}
	mode := "blackhole"
	if n.rnd.Intn(2) == 0 {
		mode = "reset"
	}
	d := n.between(3*time.Second, 20*time.Second)
	p.Set(mode, 0)
	time.Sleep(d)
	p.Set("pass", 0)
	return fmt.Sprintf("%s %v", mode, d)
}

// r2SlowS3 delays every read on h's path to the S3 backend by 100 ms–1 s
// for 5–20 s.
func r2SlowS3(n *nemesis, h string) string {
	if r2rig == nil || r2rig.s3[h] == nil {
		return "no s3 backend"
	}
	p := r2rig.s3[h]
	if p == nil {
		return "no s3 backend"
	}
	lat := n.between(100*time.Millisecond, time.Second)
	d := n.between(5*time.Second, 20*time.Second)
	p.Set("delay", lat)
	time.Sleep(d)
	p.Set("pass", 0)
	return fmt.Sprintf("delay %v for %v", lat, d)
}

// r2KillMidPut sends a large put to h and SIGKILLs h (hostd and its
// workerds) 0–600 ms later: between the blob write and the journal
// append, or during either. The put's outcome is unknown; the checkers
// hold it to "all or nothing, and never an index row without its blob".
func r2KillMidPut(n *nemesis, h string) string {
	rig := r2rig
	if rig == nil {
		return "no r2 workload"
	}
	var dns string
	for d, host := range rig.tg.endpoints() {
		if host == h {
			for _, cur := range rig.current() {
				if cur == d {
					dns = d
				}
			}
		}
	}
	if dns == "" {
		return "no endpoint on " + h
	}
	key := fmt.Sprintf("k%d", n.rnd.Intn(r2Keys))
	size := 1<<20 + n.rnd.Intn(3<<20)
	after := n.between(0, 600*time.Millisecond)
	done := make(chan r2Op, 1)
	go func() { done <- rig.w.put(rig.w.big, "nemesis", dns, key, size, 0) }()
	time.Sleep(after)
	n.c.stop(h, true)
	o := <-done
	rig.rh.add(o)
	time.Sleep(n.between(time.Second, 3*time.Second))
	n.c.startHost(h)
	n.c.waitHostsLive(h)
	return fmt.Sprintf("put %s of %s (%d bytes), killed after %v: ok=%v %s", o.Val, key, size, after, o.OK, trunc(o.Err, 120))
}

// r2Sorted is a sorted copy (reports).
func r2Sorted(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
