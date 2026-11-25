//go:build e2equeues

// Package queues is the hermetic multi-host Queues e2e (ADR-039): real
// tw-mgmt and two real tw-hostd processes on testcontrol + a local DERP.
// A producer workload on one host sends to a queue that a consumer
// workload on the other host consumes; the consumer reports every
// delivery to a recorder node on the tailnet, so the record survives
// whatever happens to the hosts. Checked: cross-workload, cross-host
// delivery; nothing lost across a consumer-host hostd restart, a crash of
// hostd and workerd mid-batch, and a lease move (deploy --replace).
// Nothing talks to the internet; no real keys.
package queues

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
	"sort"
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
	"tailscale.com/ipn/store/mem"
	"tailscale.com/tsnet"
)

const fakeToken = "fake-api-token"

// fakeTS is the smallest Tailscale API mgmt needs.
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
	mu       sync.Mutex
	procs    map[string]*proc
	workerd  string
}

func (e *env) start(name string, args ...string) {
	e.t.Helper()
	lf, err := os.OpenFile(filepath.Join(e.dir, name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		e.t.Fatal(err)
	}
	bin := "tw-hostd"
	if name == "mgmt" {
		bin = "tw-mgmt"
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

// stop ends a process and its group (workerd children): SIGTERM, or
// SIGKILL at once when crash.
func (e *env) stop(name string, crash bool) {
	e.mu.Lock()
	p := e.procs[name]
	delete(e.procs, name)
	e.mu.Unlock()
	if p == nil {
		return
	}
	sig := syscall.SIGTERM
	if crash {
		sig = syscall.SIGKILL
		// hostd starts each workerd in a process group of its own, so the
		// group signal below doesn't reach them: kill them too (a crash of
		// hostd *and* workerd, #432).
		if out, err := exec.Command("pgrep", "-P", strconv.Itoa(p.cmd.Process.Pid)).Output(); err == nil {
			for _, f := range strings.Fields(string(out)) {
				if pid, err := strconv.Atoi(f); err == nil {
					syscall.Kill(-pid, syscall.SIGKILL)
				}
			}
		}
	}
	syscall.Kill(-p.cmd.Process.Pid, sig)
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
	}
}

func (e *env) startHost(id string) {
	hd := filepath.Join(e.dir, id)
	e.start(id, "-local-dev", "-mgmt", "unix://"+e.sock, "-mgmt-token-file", filepath.Join(e.dir, "token"),
		"-host-id", id, "-data-dir", filepath.Join(hd, "data"), "-run-dir", filepath.Join(hd, "run"),
		"-control-url", e.ctl.URL, "-ephemeral", "-heartbeat", "500ms", "-queue-visibility", "5s",
		"-sandbox", "none", "-workerd", e.workerd, "-hostname-prefix", id+"-", "-log-level", "debug")
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

func (e *env) must(method, path string, body, out any) {
	e.t.Helper()
	if st, b := e.do(method, path, body, out); st/100 != 2 {
		e.dumpLogs()
		e.t.Fatalf("%s %s: %d %s", method, path, st, b)
	}
}

func (e *env) dumpLogs() {
	for _, name := range []string{"mgmt", "h1", "h2"} {
		b, _ := os.ReadFile(filepath.Join(e.dir, name+".log"))
		lines := strings.Split(string(b), "\n")
		if len(lines) > 80 {
			lines = lines[len(lines)-80:]
		}
		e.t.Logf("---- %s.log (tail)\n%s", name, strings.Join(lines, "\n"))
	}
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
		e.must("GET", "/v1/deployments/"+id, nil, &d)
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

func (e *env) waitHosts(ids ...string) {
	e.t.Helper()
	for i := 0; ; i++ {
		var pg struct {
			Items []struct{ HostID, Status string }
		}
		e.must("GET", "/v1/hosts", nil, &pg)
		live := 0
		for _, h := range pg.Items {
			for _, id := range ids {
				if h.HostID == id && h.Status == "live" {
					live++
				}
			}
		}
		if live == len(ids) {
			return
		}
		if i > 300 {
			e.dumpLogs()
			e.t.Fatalf("hosts %v not live: %+v", ids, pg.Items)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// recorder collects the consumer's delivery reports.
type recorder struct {
	mu   sync.Mutex
	seen map[string][]int // key -> attempts of each delivery
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var recs []struct {
		K        string
		Attempts int
	}
	if err := json.NewDecoder(req.Body).Decode(&recs); err != nil {
		w.WriteHeader(400)
		return
	}
	r.mu.Lock()
	for _, x := range recs {
		r.seen[x.K] = append(r.seen[x.K], x.Attempts)
	}
	r.mu.Unlock()
	w.WriteHeader(204)
}

func (r *recorder) attempts(k string) []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.seen[k]...)
}

const consJS = `export default {
  async fetch() { return new Response("cons ok\n"); },
  async queue(batch, env) {
    const recs = batch.messages.map((m) => ({ k: m.body.k, attempts: m.attempts }));
    const r = await fetch(env.REC, { method: "POST", body: JSON.stringify(recs) });
    if (!r.ok) throw new Error("recorder answered " + r.status);
    if (batch.messages.some((m) => m.body.hang && m.attempts === 1)) await new Promise((res) => setTimeout(res, 300000));
  },
};
`

const prodJS = `export default {
  async fetch(req, env) {
    const u = new URL(req.url);
    const k = u.searchParams.get("k");
    const n = Number(u.searchParams.get("n") ?? 1);
    const hang = u.searchParams.has("hang");
    const msgs = [];
    for (let i = 0; i < n; i++) msgs.push({ body: { k: n === 1 ? k : k + "-" + i, hang } });
    for (let i = 0; i < msgs.length; i += 100) await env.JOBS.sendBatch(msgs.slice(i, i + 100));
    return Response.json({ sent: n });
  },
};
`

func TestQueuesMultiHost(t *testing.T) {
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	if !hostdtest.HasNode() {
		t.Skip("node not available (needed to bundle the apps)")
	}
	dir, err := hostdtest.ShortDir("tw-q-")
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
	e := &env{t: t, bin: cliflow.BuildBinaries(t), dir: dir, ctl: ctl, sock: filepath.Join(dir, "mgmt.sock"), procs: map[string]*proc{}, workerd: wd}
	t.Cleanup(func() {
		for _, n := range []string{"h1", "h2", "mgmt"} {
			e.stop(n, false)
		}
	})
	e.hc = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", e.sock)
	}}}

	// The recorder: a tailnet node the consumer reports deliveries to,
	// and the client that calls the producer.
	rec := &recorder{seen: map[string][]int{}}
	rn := &tsnet.Server{Hostname: "rec-1", ControlURL: ctl.URL, AuthKey: nettest.DummyKey, Ephemeral: true, Store: new(mem.Store),
		Dir: filepath.Join(dir, "rec"), AdvertiseTags: []string{"tag:ttest"}, Logf: func(string, ...any) {}, UserLogf: func(string, ...any) {}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := rn.Up(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rn.Close() })
	rln, err := rn.Listen("tcp", ":80")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(rln, rec)
	recURL := "http://" + st.TailscaleIPs[0].String() + "/rec"

	keyFile := filepath.Join(dir, "api.env")
	cliflow.WriteFakeOAuthFile(t, keyFile)
	e.start("mgmt", "-local-dev", "-socket", e.sock, "-host-token-file", filepath.Join(dir, "token"), "-rotate-host-token",
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
	e.must("POST", "/v1/tenants", map[string]any{"tailnet": nettest.MagicDNSDomain, "controlURL": ctl.URL, "mintMode": "oauth", "oauthClientFile": keyFile}, &tn)
	e.startHost("h1")
	e.startHost("h2")
	e.waitHosts("h1", "h2")

	build := func(name, js, wrangler string) string {
		t.Helper()
		app := filepath.Join(dir, name)
		os.MkdirAll(filepath.Join(app, "src"), 0o755)
		os.WriteFile(filepath.Join(app, "tailworks.jsonc"), []byte(`{"name": "`+name+`", "tailnet": {"tags": ["tag:ttest"]}}`), 0o644)
		os.WriteFile(filepath.Join(app, "wrangler.jsonc"), []byte(wrangler), 0o644)
		os.WriteFile(filepath.Join(app, "src", "index.js"), []byte(js), 0o644)
		files, err := hostdtest.Build(app, nil)
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(hostdtest.MustTar(files))
	}
	prodBundle := build("prod", prodJS, `{"name": "prod", "main": "src/index.js", "compatibility_date": "2026-04-01",
		"queues": {"producers": [{"binding": "JOBS", "queue": "jobs"}]}}`)
	consBundle := build("cons", consJS, `{"name": "cons", "main": "src/index.js", "compatibility_date": "2026-04-01",
		"vars": {"REC": "`+recURL+`"},
		"queues": {"consumers": [{"queue": "jobs", "max_batch_size": 10, "max_batch_timeout": 1, "max_retries": 5}]}}`)
	deploy := func(body map[string]any) deployment {
		t.Helper()
		body["tenantID"] = tn.TenantID
		var d deployment
		if st, b := e.do("POST", "/v1/deployments", body, &d); st != 202 {
			e.dumpLogs()
			t.Fatalf("deploy %v: %d %s", body["workload"], st, b)
		}
		return e.waitActive(d.DeploymentID, 120*time.Second)
	}
	prod := deploy(map[string]any{"workload": "prod", "host": "h1", "bundle": prodBundle})
	cons := deploy(map[string]any{"workload": "cons", "host": "h2", "bundle": consBundle})
	t.Logf("producer on %s, consumer on %s", prod.Host, cons.Host)

	hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: rn.Dial}}
	send := func(k string, n int, hang bool) {
		t.Helper()
		u := fmt.Sprintf("http://%s/?k=%s&n=%d", prod.Endpoint.MagicDNS, k, n)
		if hang {
			u += "&hang=1"
		}
		for i := 0; ; i++ {
			resp, err := hc.Get(u)
			if err == nil {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode == 200 {
					return
				}
				err = fmt.Errorf("%d %s", resp.StatusCode, b)
			}
			if i > 50 {
				e.dumpLogs()
				t.Fatalf("send %s: %v", k, err)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	keys := func(k string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%s-%d", k, i)
		}
		return out
	}
	waitAll := func(what string, ks []string, timeout time.Duration) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for {
			var missing []string
			for _, k := range ks {
				if len(rec.attempts(k)) == 0 {
					missing = append(missing, k)
				}
			}
			if len(missing) == 0 {
				return
			}
			if time.Now().After(deadline) {
				sort.Strings(missing)
				e.dumpLogs()
				t.Fatalf("%s: %d of %d messages never delivered: %v", what, len(missing), len(ks), missing)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// 1. Cross-workload, cross-host: producer on h1, consumer on h2.
	a := keys("a", 25)
	send("a", 25, false)
	waitAll("cross-host", a, 60*time.Second)
	t.Log("✓ 25 messages from prod@h1 delivered to cons@h2")

	// 2. The consumer's hostd restarts: messages sent while it is down
	// wait in mgmt and arrive after it is back.
	e.stop("h2", false)
	b := keys("b", 25)
	send("b", 25, false)
	time.Sleep(time.Second)
	e.startHost("h2")
	e.waitHosts("h2")
	waitAll("across a hostd restart", b, 90*time.Second)
	t.Log("✓ 25 messages sent while the consumer's hostd was down delivered after its restart")

	// 3. hostd and workerd crash mid-batch (SIGKILL of the process group):
	// the batch's leases expire and it is delivered again.
	send("c", 1, true)
	deadline := time.Now().Add(60 * time.Second)
	for len(rec.attempts("c")) == 0 {
		if time.Now().After(deadline) {
			e.dumpLogs()
			t.Fatal("the hanging message never reached the consumer")
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.stop("h2", true)
	e.startHost("h2")
	e.waitHosts("h2")
	deadline = time.Now().Add(90 * time.Second)
	for {
		at := rec.attempts("c")
		if len(at) >= 2 && at[len(at)-1] >= 2 {
			t.Logf("✓ the batch in flight when h2 crashed was delivered again (attempts %v)", at)
			break
		}
		if time.Now().After(deadline) {
			e.dumpLogs()
			t.Fatalf("crashed batch not redelivered: attempts %v", at)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// 4. A lease move: the consumer is re-placed on h1 while messages
	// flow; every message arrives, consumed on the new host.
	d := keys("d", 30)
	send("d", 30, false)
	moved := deploy(map[string]any{"workload": "cons", "host": "h1", "replace": true, "bundle": consBundle})
	if moved.Host != "h1" {
		t.Fatalf("consumer moved to %s", moved.Host)
	}
	f := keys("f", 30)
	send("f", 30, false)
	waitAll("across a lease move", append(d, f...), 90*time.Second)
	t.Logf("✓ 60 messages sent around a lease move h2 → h1 delivered")

	// Nothing is left: every message was acked.
	deadline = time.Now().Add(30 * time.Second)
	for {
		var q mgmt.QueueInfo
		e.must("GET", "/v1/tenants/"+tn.TenantID+"/queues/jobs", nil, &q)
		if q.Backlog == 0 {
			t.Logf("✓ queue jobs drained (consumer %s)", q.Consumer)
			break
		}
		if time.Now().After(deadline) {
			e.dumpLogs()
			t.Fatalf("queue not drained: %+v", q)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// At-least-once: count redeliveries per phase (a settle the old lease
	// holder couldn't make after the move, for example, means another
	// delivery).
	for _, g := range []struct {
		name string
		ks   []string
	}{{"cross-host", a}, {"hostd restart", b}, {"lease move", append(d, f...)}} {
		dups := 0
		for _, k := range g.ks {
			dups += len(rec.attempts(k)) - 1
		}
		t.Logf("at-least-once: %s: %d redeliveries of %d messages", g.name, dups, len(g.ks))
	}
}
