package gen_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/gen"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// fakeHost plays tw-hostd's side of the sockets (no tailnet): it verifies
// facet egress MACs and echoes the attribution it would use.
type fakeHost struct {
	mu       sync.Mutex
	ops      []map[string]any
	key      string
	admitted map[string]bool // names admitted for creation (as hostd records them)
}

func (f *fakeHost) serve(t *testing.T, sock string, h http.Handler) {
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
}

func (f *fakeHost) api() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		body["op"] = strings.TrimPrefix(r.URL.Path, "/v1/")
		f.mu.Lock()
		f.ops = append(f.ops, body)
		f.mu.Unlock()
		// Create admission, as hostd decides it: an attested object (as) or a
		// request with a create ticket may admit any name; a public request
		// (no ticket) only names "pub-ok*" (they exist, or public.create
		// allows them). ensure then creates only admitted names: "pub-adm*"
		// stands in for a name a bypassed guard reached without admission
		// (hostd refuses the create itself).
		name := fmtAny(body["name"])
		if as, ok := body["as"].(map[string]any); ok && body["op"] == "ensure" {
			name = fmtAny(as["name"])
		}
		pubOK := strings.HasPrefix(name, "pub-ok")
		_, hasTicket := body["ticket"].(string)
		_, hasAs := body["as"].(map[string]any)
		deny := func(code, msg string) {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(403)
			w.Write([]byte(`{"code":"` + code + `","message":"` + msg + `"}`))
		}
		switch body["op"] {
		case "admit":
			// "pub-adm*": the guard's admit passes but hostd never records an
			// admission, standing in for a guard that was bypassed or lied.
			pubAdm := strings.HasPrefix(name, "pub-adm")
			if !hasTicket && !hasAs && !pubOK && !pubAdm {
				deny("public_create_denied", "no such object for a public request")
				return
			}
			if !pubAdm {
				f.mu.Lock()
				if f.admitted == nil {
					f.admitted = map[string]bool{}
				}
				f.admitted[name] = true
				f.mu.Unlock()
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "existing": pubOK})
			return
		case "ensure":
			f.mu.Lock()
			adm := f.admitted[name]
			f.mu.Unlock()
			if !adm && !pubOK {
				deny("create_not_admitted", "object "+name+" does not exist and no request admitted its creation")
				return
			}
		}
		switch body["op"] {
		case "spawn":
			json.NewEncoder(w).Encode(map[string]any{"objectID": "o-child", "status": "pending", "hostname": "probe-probe-child"})
		case "ready":
			json.NewEncoder(w).Encode(map[string]any{"magicDNS": "probe-probe-child.ttest.ts.net", "status": "ready"})
		case "kill":
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(403)
			w.Write([]byte(`{"code":"cap_missing","message":"no kill for you"}`))
		default:
			json.NewEncoder(w).Encode(map[string]any{"objectID": "o1", "status": "pending"})
		}
	})
}

func fmtAny(v any) string {
	s, _ := v.(string)
	return s
}

func (f *fakeHost) egressObj() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			f.connect(w, r, true) // connect_test.go
			return
		}
		cls, name, ok := gen.VerifyEgress(f.key, r.Header, r.Method, r.RequestURI, time.Now())
		if !ok {
			w.Header().Set("tw-error", "unknown_object")
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]any{"code": "unknown_object", "uri": r.RequestURI, "h": r.Header})
			return
		}
		if egressRedirect(w, r) { // egress_sink_test.go
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"as": cls + "/" + name, "url": r.RequestURI, "headers": r.Header})
	})
}

func (f *fakeHost) egressWL() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			f.connect(w, r, false) // connect_test.go
			return
		}
		if egressRedirect(w, r) { // egress_sink_test.go
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"as": "workload", "url": r.RequestURI, "headers": r.Header})
	})
}

type inst struct {
	t   *testing.T
	hc  *http.Client
	cmd *exec.Cmd
	f   *fakeHost

	cfg string // config.capnp
	log string // workerd's stdout and stderr
}

func start(t *testing.T, appDir string, vars map[string]string) *inst {
	t.Helper()
	if !hostdtest.HasNode() {
		t.Skip("node not available")
	}
	files, err := hostdtest.Build(appDir, vars)
	if err != nil {
		t.Fatal(err)
	}
	return startFiles(t, files)
}

// startFiles runs an already-built bundle (path -> contents).
func startFiles(t *testing.T, files map[string][]byte) *inst {
	t.Helper()
	return startFilesOpts(t, files, nil)
}

// startFilesOpts is startFiles with mod applied to the generator options.
func startFilesOpts(t *testing.T, files map[string][]byte, mod func(*gen.Options)) *inst {
	t.Helper()
	wdBin, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	app, err := bundle.Parse(files)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := hostdtest.ShortDir("tw-gen-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	opts := gen.Options{Dir: filepath.Join(dir, "i1"), DOStorage: filepath.Join(dir, "do"), DeploymentID: "d1", Workload: app.Name}
	if mod != nil {
		mod(&opts)
	}
	res, err := gen.Generate(app, opts)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeHost{key: res.MACKey}
	f.serve(t, res.Sockets.HostAPI, f.api())
	f.serve(t, res.Sockets.EgressObj, f.egressObj())
	f.serve(t, res.Sockets.EgressWL, f.egressWL())
	logf, _ := os.Create(filepath.Join(dir, "workerd.log"))
	cmd := exec.Command(wdBin, append([]string{"serve", res.Config, "--experimental"}, pythonArgs(t, app)...)...) // python_test.go
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() {
			b, _ := os.ReadFile(filepath.Join(dir, "workerd.log"))
			t.Logf("workerd log:\n%s", b)
		}
	})
	hc := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", res.Sockets.Ingress)
	}}}
	in := &inst{t: t, hc: hc, cmd: cmd, f: f}
	in.cfg, in.log = res.Config, filepath.Join(dir, "workerd.log")
	// A Python Worker loads Pyodide before workerd listens: seconds on an
	// idle machine, past 20 s on a loaded CI runner (#428).
	wait := 20 * time.Second
	if app.Python {
		wait = 90 * time.Second
	}
	t0 := time.Now()
	for {
		code, _, _ := in.get("/", map[string]any{"kind": "admin", "op": "health"})
		if code == 200 {
			t.Logf("workerd up in %v", time.Since(t0).Round(time.Millisecond))
			break
		}
		if time.Since(t0) > wait {
			t.Fatalf("workerd did not come up within %v", wait)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return in
}

var testCaller = map[string]any{"nodeId": "nCLIENT", "node": "ttest-client.ttest.ts.net.", "hostname": "ttest-client", "addrs": []string{"100.64.0.9"}, "tags": []string{}, "capMap": map[string]any{}}

// ticketed adds hostd's create ticket to a simulated cf.tw (tailnet
// requests and cron runs carry one; public requests never do).
func (in *inst) ticketed(tw map[string]any) map[string]any {
	out := map[string]any{"ticket": gen.CreateTicket(in.f.key, "tr-test", time.Now())}
	for k, v := range tw {
		out[k] = v
	}
	return out
}

// sealed is cf.tailscale as hostd sends it for a tailnet request: netd's
// caller plus hostd's seal (RT3-08; without it the platform drops the caller).
func (in *inst) sealed() map[string]any {
	cb, _ := json.Marshal(testCaller)
	return map[string]any{"caller": testCaller, "seal": gen.SealCaller(in.f.key, cb, time.Now())}
}

func (in *inst) get(path string, tw map[string]any, hdr ...string) (int, http.Header, map[string]any) {
	if tw["kind"] == "object" {
		// Delivered on the object's own node: in hostd the object exists, so
		// its ensure is a wake. The fake records it as admitted.
		in.f.mu.Lock()
		if in.f.admitted == nil {
			in.f.admitted = map[string]bool{}
		}
		in.f.admitted[fmtAny(tw["name"])] = true
		in.f.mu.Unlock()
	}
	req, _ := http.NewRequest("GET", "http://app.local"+path, nil)
	blob := map[string]any{"tailscale": in.sealed(), "objectId": "o1", "tw": in.ticketed(tw)}
	b, _ := json.Marshal(blob)
	req.Header.Set("x-tw-cf", string(b))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := in.hc.Do(req)
	if err != nil {
		return 0, nil, map[string]any{"err": err.Error()}
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if json.Unmarshal(rb, &out) != nil {
		out = map[string]any{"raw": string(rb)}
	}
	return resp.StatusCode, resp.Header, out
}

func obj(cls, name string) map[string]any {
	return map[string]any{"kind": "object", "class": cls, "name": name}
}

var wl = map[string]any{"kind": "workload"}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestGeneratedWrapperProbe(t *testing.T) {
	in := start(t, hostdtest.Fixture("probe"), nil)

	t.Run("WorkloadDefaultExport", func(t *testing.T) {
		code, _, r := in.get("/", wl)
		if code != 200 || r["kind"] != "worker" || r["hasTailnet"] != true {
			t.Fatalf("%d %v", code, r)
		}
		if c := r["caller"].(map[string]any); c["nodeId"] != "nCLIENT" {
			t.Fatalf("caller %v", c)
		}
	})
	t.Run("WorkloadEgressIsWorkload", func(t *testing.T) {
		code, _, r := in.get("/egress?url=http://echo/x", wl)
		if code != 200 || r["as"] != "workload" || r["url"] != "http://echo/x" {
			t.Fatalf("%d %v", code, r)
		}
		// #347: the Worker can't hand hostd a service binding's wire header.
		code, _, r = in.get("/egress?wire=1&url=http://echo/x", wl)
		if h, _ := r["headers"].(map[string]any); code != 200 || r["as"] != "workload" || h == nil || h["Tailworks-Binding"] != nil {
			t.Fatalf("a tenant-set Tailworks-Binding reached hostd's workload egress: %d %v", code, r)
		}
	})
	t.Run("ObjectRoute", func(t *testing.T) {
		code, _, r := in.get("/", obj("Probe", "a"))
		if code != 200 || r["kind"] != "probe" || r["name"] != "a" {
			t.Fatalf("%d %v", code, r)
		}
		if c := r["caller"].(map[string]any); c["nodeId"] != "nCLIENT" {
			t.Fatalf("caller in facet %v", c)
		}
	})
	t.Run("FacetEgressAttributedWithMAC", func(t *testing.T) {
		for _, n := range []string{"a", "b", "c d"} {
			code, _, r := in.get("/egress?url=http://echo/y", obj("Probe", n))
			if code != 200 || r["as"] != "Probe/"+n {
				t.Fatalf("%s: %d %v", n, code, r)
			}
			h := r["headers"].(map[string]any)
			for k := range h {
				if strings.HasPrefix(strings.ToLower(k), "proxy-auth") {
					t.Fatalf("leaked %s", k)
				}
			}
		}
	})
	t.Run("ForgedEgressHeadersIgnored", func(t *testing.T) {
		code, _, r := in.get("/forge-egress?url=http://echo/z&as=victim", obj("Probe", "a"))
		if code != 200 || r["as"] != "Probe/a" {
			t.Fatalf("%d %v", code, r)
		}
		h := r["headers"].(map[string]any)
		if h["X-Tw-Object"] != nil || h["Proxy-Authorization"] != nil || h["X-Tw-Cf"] != nil || h["Tailworks-Binding"] != nil {
			t.Fatalf("forged headers reached hostd: %v", h)
		}
	})
	t.Run("FacetEnvAndPrivacy", func(t *testing.T) {
		_, _, r := in.get("/env", obj("Probe", "a"))
		if jsonOf(r["env"]) != `["GREETING","PROBE","TICKER"]` || r["hasTailnet"] != true {
			t.Fatalf("facet env %v", r)
		}
		_, _, r = in.get("/", wl)
		for _, k := range r["env"].([]any) {
			if k == "LOADER" || k == "NETD_EGRESS" || k == "HOSTD" || k == "MAC_KEY" {
				t.Fatalf("platform binding %v visible to the tenant worker", k)
			}
		}
	})
	t.Run("TwObjectPathOnWorkload", func(t *testing.T) {
		code, _, r := in.get("/_tw/o/Probe/zz/", wl)
		if code != 200 || r["name"] != "zz" {
			t.Fatalf("%d %v", code, r)
		}
		code, _, r = in.get("/_tw/o/Nope/zz/", wl)
		if code != 404 {
			t.Fatalf("unknown class: %d %v", code, r)
		}
	})
	t.Run("RPCForwardedToFacet", func(t *testing.T) {
		code, _, r := in.get("/rpc?name=r1", wl)
		if code != 200 || r["reply"] != "hello worker from r1" {
			t.Fatalf("%d %v", code, r)
		}
		code, _, r = in.get("/sibling?name=s2", obj("Probe", "s1"))
		if code != 200 || r["reply"] != "hello sibling from s2" {
			t.Fatalf("%d %v", code, r)
		}
	})
	t.Run("Spawn", func(t *testing.T) {
		code, _, r := in.get("/spawn?name=child", obj("Probe", "parent"))
		if code != 200 || r["reply"] != "hello parent from child" || r["before"] != "pending" || r["after"] != "ready" {
			t.Fatalf("%d %v", code, r)
		}
		in.f.mu.Lock()
		var sp map[string]any
		for _, o := range in.f.ops {
			if o["op"] == "spawn" {
				sp = o
			}
		}
		in.f.mu.Unlock()
		if sp == nil || jsonOf(sp["as"]) != `{"class":"Probe","name":"parent"}` || sp["class"] != "Probe" || sp["name"] != "child" {
			t.Fatalf("spawn op %v", sp)
		}
	})
	t.Run("TypedErrorFromHostd", func(t *testing.T) {
		code, _, r := in.get("/kill-self", obj("Probe", "a"))
		if code != 500 && !strings.Contains(jsonOf(r), "cap_missing") {
			t.Fatalf("%d %v", code, r)
		}
	})
	t.Run("PlainDOAlarm", func(t *testing.T) {
		code, _, r := in.get("/arm", obj("Ticker", "t1"))
		if code != 200 {
			t.Fatalf("%d %v", code, r)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, _, r = in.get("/", obj("Ticker", "t1"))
			if r["alarms"] == float64(1) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("alarm never fired: %v", r)
			}
			time.Sleep(50 * time.Millisecond)
		}
		if r["hasTailnet"] != true {
			t.Fatalf("plain DO has no ctx.tailnet: %v", r)
		}
		code, _, r = in.get("/egress?url=http://echo/p", obj("Ticker", "t1"))
		if code != 200 || r["as"] != "workload" {
			t.Fatalf("plain egress: %d %v", code, r)
		}
		// The plain wrapper told hostd the object exists.
		in.f.mu.Lock()
		found := false
		for _, o := range in.f.ops {
			if o["op"] == "ensure" && jsonOf(o["as"]) == `{"class":"Ticker","name":"t1"}` {
				found = true
			}
		}
		in.f.mu.Unlock()
		if !found {
			t.Fatal("no ensure for plain object")
		}
	})
	t.Run("AdminKill", func(t *testing.T) {
		for _, c := range []string{"Probe", "Ticker"} {
			code, _, r := in.get("/", map[string]any{"kind": "admin", "op": "kill", "class": c, "name": "k1", "reason": "test"})
			if code != 200 {
				t.Fatalf("%s kill: %d %v", c, code, r)
			}
			code, h, r := in.get("/", obj(c, "k1"))
			if code != 410 || h.Get("tw-error") != "killed" {
				t.Fatalf("%s after kill: %d %v", c, code, r)
			}
		}
		code, _, r := in.get("/rpc?name=k1", wl)
		if code == 200 {
			t.Fatalf("RPC to killed object succeeded: %v", r)
		}
	})
	t.Run("NoCfIsRefused", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "http://app.local/", nil)
		resp, err := in.hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("%d", resp.StatusCode)
		}
	})
}

func TestGeneratedWrapperAgentTemplate(t *testing.T) {
	in := start(t, hostdtest.Template("agent"), nil)
	// Via the Worker (getAgentByName) and directly on the object.
	for i := 1; i <= 2; i++ {
		code, _, r := in.get("/?agent=alice", wl)
		if code != 200 || r["agent"] != "alice" || jsonOf(r["state"]) != `{"visits":`+string(rune('0'+i))+`}` || r["calls"] != float64(i) {
			t.Fatalf("visit %d via worker: %d %v", i, code, r)
		}
	}
	code, _, r := in.get("/", obj("SupportAgent", "alice"))
	if code != 200 || r["agent"] != "alice" || jsonOf(r["state"]) != `{"visits":3}` {
		t.Fatalf("direct: %d %v", code, r)
	}
	code, _, r = in.get("/?upstream=http://echo/agent", obj("SupportAgent", "bob"))
	up, _ := r["upstream"].(map[string]any)
	if code != 200 || up == nil || up["body"].(map[string]any)["as"] != "SupportAgent/bob" {
		t.Fatalf("agent egress: %d %v", code, r)
	}
	code, _, r = in.get("/spawn?child=lk1", obj("SupportAgent", "bob"))
	if code != 200 || !strings.Contains(jsonOf(r), `"lookup":"lk1"`) {
		t.Fatalf("spawn: %d %v", code, r)
	}
}
