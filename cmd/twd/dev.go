package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rosskukulinski/twd/hostd"
	"github.com/rosskukulinski/twd/hostd/bundle"
	"github.com/rosskukulinski/twd/hostd/fakemgmt"
	"github.com/rosskukulinski/twd/hostd/mgmtapi"
	"github.com/rosskukulinski/twd/netd"
	"github.com/rosskukulinski/twd/netd/nettest"
)

// twd dev: the same twd-hostd (process tier, netd, workerd, per-object
// nodes) as production, against an embedded control plane on 127.0.0.1
// (Tailscale's testcontrol + local DERP/STUN) and an in-memory mgmt. No
// tailnet, no keys. A local proxy on --port forwards to the workload node
// through a dev client node, so getCaller() sees a real (dev) caller.
//
// Not built yet (STATUS.md): the embedded policy evaluator (every dev node
// sees every other and gets the same test caps) and mock peers from
// twd.dev.jsonc.

type devResult struct {
	URL string `json:"url"`
	// Serves is where the app answers: URL, plus the MCP route for an app
	// that mounts an MCP server (the mcp template serves /mcp; / is 404).
	Serves   string             `json:"serves"`
	Workload string             `json:"workload"`
	Node     string             `json:"node"`
	As       string             `json:"as"`
	Smoke    map[string]any     `json:"smoke,omitempty"`
	Timings  map[string]float64 `json:"timingsMs"`

	// D1 databases and the migrations dev applied to them (dev_d1.go).
	D1 []devD1 `json:"d1,omitempty"`
}

func cmdDev(args []string) error {
	f := newFlags("dev")
	port := f.Int("port", 8787, "local port (127.0.0.1)")
	once := f.Bool("once", false, "start, make one request (GET /; for an MCP app an initialize to its route), print it and exit (smoke test)")
	workerdFlag := f.String("workerd", "", "workerd binary")
	verbose := f.Bool("v", false, "hostd logs to stderr")
	testScheduled := f.Bool("test-scheduled", false, "serve /__scheduled?cron=<expr>[&time=<ms>] on the local port: run a cron trigger's scheduled() now")
	ao := addAppFlags(f) // wranglerapp.go: --env, --wrangler, --from-wrangler-out
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	dir := "."
	if len(pos) > 0 {
		dir = pos[0]
	}
	a, err := loadAppWith(dir, "", *ao)
	if err != nil {
		return err
	}
	a.printWarnings(os.Stderr)
	if err := checkDevPort(*port, os.Stderr); err != nil { // devlog.go
		return err
	}
	wd, err := findWorkerd(*workerdFlag)
	if err != nil {
		return err
	}
	t0 := time.Now()
	res := devResult{Workload: a.Name, Timings: map[string]float64{}}
	var log *slog.Logger
	if *verbose {
		log = slog.New(netd.NewRedactingHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	}
	tmp, err := os.MkdirTemp("/tmp", "twd-dev-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	os.Chmod(tmp, 0o700)
	guardDevDir(tmp, wd) // devreap.go: a SIGKILL doesn't orphan workerd or leak tmp

	quietStdLog() // devlog.go: no "STUN server listening" line
	ctl, err := nettest.StartControl()
	if err != nil {
		return err
	}
	defer ctl.Close()
	fm := fakemgmt.New(fakemgmt.Config{StaticKey: nettest.DummyKey, Logger: log})
	sock := filepath.Join(tmp, "mgmt.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	msrv := &http.Server{Handler: fm}
	go msrv.Serve(ln)
	defer msrv.Close()
	mc, _ := mgmtapi.NewClient("unix://" + sock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var tr struct {
		TenantID string `json:"tenantID"`
	}
	if err := mc.Do(ctx, "POST", "/v1/tenants", mgmtapi.Tenant{Tailnet: nettest.MagicDNSDomain, ControlURL: ctl.URL, MintMode: "static"}, &tr); err != nil {
		return err
	}
	hc, _ := mgmtapi.NewClient("unix://" + sock)
	h, err := hostd.New(hostd.Config{HostID: "dev", Mgmt: hc, DataDir: filepath.Join(tmp, "data"), RunDir: filepath.Join(tmp, "run"),
		WorkerdBin: wd, HeartbeatEvery: 300 * time.Millisecond, Ephemeral: true, ControlURL: ctl.URL, Logger: log, HostnamePrefix: "dev-",
		// twd dev is explicitly process-tier: a "tier": "microvm" app runs
		// in a local process here (never on a host serving tenants).
		ForceProcessTier: true})
	if err != nil {
		return err
	}
	if err := h.Start(ctx); err != nil {
		return err
	}
	defer h.Close()
	client, err := nettest.NewNode(ctx, ctl.URL, nettest.DummyKey, "dev-you", filepath.Join(tmp, "you"))
	if err != nil {
		return err
	}
	defer client.Close()
	if st, err := client.LC.Status(ctx); err == nil && st.Self != nil {
		if u, ok := st.User[st.Self.UserID]; ok {
			res.As = u.LoginName
		}
	}

	// KV namespaces (and other platform stores) last between runs (devstores.go).
	if _, err := loadDevStores(fm, tr.TenantID, a.Dir); err != nil {
		return err
	}
	defer func() {
		if err := saveDevStores(fm, tr.TenantID, a.Dir); err != nil {
			fmt.Fprintf(os.Stderr, "twd dev: could not save the KV namespaces to %s: %v\n", devStoresFile(a.Dir), err)
		}
	}()

	var devVars []string // .dev.vars names (devvars.go): the workload's secrets in dev
	var devD1s []devD1   // D1 databases and the migrations dev applied (dev_d1.go)
	deploy := func() (hostd.ObjectStatus, error) {
		if devVars, err = syncDevVars(ctx, mc, tr.TenantID, a.Name, a.Dir); err != nil {
			return hostd.ObjectStatus{}, fail("bad_request", err.Error())
		}
		tb, _, err := a.build(nil)
		if err != nil {
			return hostd.ObjectStatus{}, err
		}
		var dr mgmtapi.DeployResponse
		if err := mc.Do(ctx, "POST", "/v1/deployments", mgmtapi.DeployRequest{TenantID: tr.TenantID, Workload: a.Name, Bundle: tb}, &dr); err != nil {
			return hostd.ObjectStatus{}, err
		}
		dl := time.Now().Add(60 * time.Second)
		for time.Now().Before(dl) {
			for _, w := range h.Status().Workloads {
				if w.Workload == a.Name && w.Status == "failed" {
					return hostd.ObjectStatus{}, fail("deploy_failed", "workerd could not start "+a.Name, strings.Split(w.Error, "\n")...)
				}
				if w.Workload == a.Name && w.Status == "active" && w.DeploymentID == dr.DeploymentID {
					if o, ok := h.Object(tr.TenantID, a.Name, "", a.Name); ok && o.Status == "ready" && o.IP != "" {
						if devD1s, err = devD1Migrate(ctx, h, tr.TenantID, a); err != nil {
							return hostd.ObjectStatus{}, fail("d1_error", "applying D1 migrations: "+err.Error())
						}
						return o, nil
					}
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		return hostd.ObjectStatus{}, fail("identity_pending", "dev deploy not ready after 60s")
	}
	wl, err := deploy()
	if err != nil {
		return err
	}
	res.Node = wl.Hostname
	res.D1 = devD1s
	res.Timings["ready"] = ms(time.Since(t0))
	var cur atomic.Value
	cur.Store(wl.IP)
	rp := &httputil.ReverseProxy{
		Transport: client.HTTPClient(0).Transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			ip := cur.Load().(string)
			pr.Out.URL.Scheme, pr.Out.URL.Host, pr.Out.Host = "http", ip, ip
		},
	}
	pl, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return fail("bad_request", fmt.Sprintf("can't listen on 127.0.0.1:%d", *port), err.Error()).withFix("twd dev --port <other>")
	}
	var handler http.Handler = rp
	if *testScheduled {
		handler = devScheduled(h, tr.TenantID, a.Name, rp)
	}
	psrv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go psrv.Serve(pl)
	defer psrv.Close()
	res.URL = "http://" + pl.Addr().String()
	route := mcpRoute(a)
	res.Serves = res.URL + route
	if *once {
		// Smoke: GET /. An MCP app answers 404 there (its server is
		// mounted on a route, /mcp in the template), so ask it what an
		// MCP client asks first instead: a JSON-RPC initialize.
		res.Smoke = devGet(res.URL + "/")
		if route != "" && fmt.Sprint(res.Smoke["status"]) == "404" {
			res.Smoke = devMCPInitialize(res.Serves)
		}
		emit(f, res, func() { fmt.Printf("twd dev · %s → %v\n", res.Serves, res.Smoke) })
		return nil
	}
	// --json: NDJSON events on stdout (agentjson.go) instead of the text.
	var hout io.Writer = os.Stdout
	ev := newEvents(*f.json, os.Stdout)
	if ev != nil {
		hout = io.Discard
		ev.emit("ready", devReadyFields(res, a))
	}
	fmt.Fprintf(hout, "twd dev · process tier · embedded control plane (127.0.0.1) · no policy evaluator yet: every dev node sees every other\n")
	if a.Cfg.Tier == "microvm" {
		fmt.Fprintf(hout, "  twd.jsonc asks for \"tier\": \"microvm\"; twd dev runs it in the process tier (deployed hosts refuse it without a microVM backend)\n")
	}
	fmt.Fprintf(hout, "  node %-28s %s\n", wl.Hostname, strings.Join(a.Cfg.Tailnet.Tags, ","))
	if len(devVars) > 0 {
		fmt.Fprintf(hout, "  secrets %-25s from %s\n", strings.Join(devVars, ","), DevVarsFile)
	}
	for _, d := range devD1s {
		fmt.Printf("  d1    %-27s %s (in memory for this run; %d migration(s) applied)\n", d.Binding, d.Database, len(d.Applied))
	}
	for _, c := range a.Cfg.Classes() {
		if c.Class != "_workload" {
			fmt.Fprintf(hout, "  class %-27s %s (a node per object, on first use)\n", c.Class, strings.Join(c.Tags, ","))
		}
	}
	for _, w := range h.Status().Workloads {
		if w.Workload == a.Name {
			for _, c := range w.Crons {
				hint := ""
				if *testScheduled {
					hint = "  · " + res.URL + "/__scheduled?cron=" + url.QueryEscape(c)
				}
				fmt.Fprintf(hout, "  cron  %-27s UTC%s\n", c, hint)
			}
		}
	}
	where := res.URL
	if route != "" {
		where = res.Serves + "  (MCP server; / answers 404)"
	}
	fmt.Fprintf(hout, "ready on %s  (as user %s) in %.1fs · edits reload · Ctrl-C to stop\n", where, res.As, res.Timings["ready"]/1000)
	go followDevRecords(ctx, h.RequestLogPath(tr.TenantID, a.Name), hout, ev) // devrecords.go

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	last := snapshot(a.Dir)
	// 150 ms: a reload shows within ~0.2 s (500 ms added 0.25 s on average).
	// snapshot skips node_modules, dist and dot dirs, so a poll is cheap.
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-sig:
			ev.emit("stopping", nil)
			fmt.Fprintln(hout, "\nstopping")
			return nil
		case <-tick.C:
			now := snapshot(a.Dir)
			if now == last {
				continue
			}
			last = now
			rs := time.Now()
			if na, err := loadAppWith(a.Dir, a.Name, a.Opts); err == nil {
				a = na
			}
			o, err := deploy()
			if err != nil {
				if ev != nil {
					ev.error(err)
				} else {
					printErr(err)
				}
				continue
			}
			cur.Store(o.IP)
			ev.emit("reload", map[string]any{"workload": a.Name, "node": o.Hostname, "ms": ms(time.Since(rs))})
			fmt.Fprintf(hout, "↻ reloaded in %.0f ms\n", ms(time.Since(rs)))
		}
	}
}

// devScheduled serves wrangler's /__scheduled test route on the dev proxy
// (127.0.0.1 only, and only with --test-scheduled): it runs one of the
// app's declared cron triggers now. Everything else goes to the app.
func devScheduled(h *hostd.Host, tenantID, workload string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/__scheduled" {
			next.ServeHTTP(w, r)
			return
		}
		q := r.URL.Query()
		cron := q.Get("cron")
		at := time.Now()
		if ts := q.Get("time"); ts != "" {
			n, err := strconv.ParseInt(ts, 10, 64)
			if err != nil {
				writeDevJSON(w, 400, map[string]any{"code": "bad_request", "message": "time: want epoch milliseconds"})
				return
			}
			at = time.UnixMilli(n)
		}
		if cron == "" {
			for _, ws := range h.Status().Workloads {
				if ws.Workload == workload && len(ws.Crons) > 0 {
					cron = ws.Crons[0]
				}
			}
		}
		res, err := h.RunCron(r.Context(), tenantID, workload, cron, at)
		if err != nil {
			writeDevJSON(w, 400, map[string]any{"code": "bad_request", "message": err.Error()})
			return
		}
		writeDevJSON(w, 200, res)
	})
}

func writeDevJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func devGet(u string) map[string]any {
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Get(u)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	m := map[string]any{}
	if json.Unmarshal(b, &m) != nil {
		m = map[string]any{"body": string(b)}
	}
	m["status"] = resp.StatusCode
	return m
}

// mcpRouteRE matches the route option of createMcpHandler(server, { route:
// "/mcp" }) or @twd/mcp's withTailnet(handler, { path: "/mcp" }).
var mcpRouteRE = regexp.MustCompile(`(?:route|path)\s*:\s*"(/[^"]*)"`)

// mcpRoute reports where the app mounts an MCP server, read from its entry
// module (wrangler.jsonc / twd.jsonc "main"): the route passed to
// createMcpHandler or withTailnet, "/mcp" if it has one without a route,
// "" for an app that isn't MCP-shaped.
func mcpRoute(a *app) string {
	main, _ := a.Wrangler["main"].(string)
	if main == "" {
		var top struct {
			Main string `json:"main"`
		}
		bundle.UnmarshalJSONC(a.TwdRaw, &top)
		main = top.Main
	}
	if main == "" {
		return ""
	}
	src, err := os.ReadFile(filepath.Join(a.Dir, main))
	if err != nil || !bytes.Contains(src, []byte("createMcpHandler")) && !bytes.Contains(src, []byte("withTailnet(")) {
		return ""
	}
	if m := mcpRouteRE.FindSubmatch(src); m != nil {
		return string(m[1])
	}
	return "/mcp"
}

// devMCPInitialize is the smoke request for an MCP app: a JSON-RPC
// initialize (what an MCP client sends first) to its route. The handler
// answers with JSON or with one SSE event; either way the first data line
// is the JSON-RPC response.
func devMCPInitialize(u string) map[string]any {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"twd dev","version":"0"}}}`
	req, err := http.NewRequest("POST", u, strings.NewReader(body))
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json, text/event-stream")
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	defer resp.Body.Close()
	m := map[string]any{"status": resp.StatusCode, "method": "initialize"}
	payload := ""
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "data:") {
			payload = strings.TrimSpace(strings.TrimPrefix(l, "data:"))
			break
		}
		if payload == "" && l != "" && !strings.HasPrefix(l, "event:") && !strings.HasPrefix(l, "id:") {
			payload = l // plain JSON, or the first line of a non-MCP answer
			if strings.HasPrefix(l, "{") {
				break
			}
		}
	}
	var rpc struct {
		Result map[string]any `json:"result"`
		Error  any            `json:"error"`
	}
	switch {
	case json.Unmarshal([]byte(payload), &rpc) == nil && rpc.Result != nil:
		m["result"] = rpc.Result
	case json.Unmarshal([]byte(payload), &rpc) == nil && rpc.Error != nil:
		m["error"] = rpc.Error
	default:
		m["body"] = payload
	}
	return m
}

// snapshot is a cheap change detector: newest mtime + file count of the
// app's sources (node_modules, dist and dot-dirs excluded).
func snapshot(dir string) string {
	var newest time.Time
	n := 0
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != dir && (d.Name() == "node_modules" || d.Name() == "dist" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		if info, err := d.Info(); err == nil && !d.IsDir() {
			n++
			if info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		return nil
	})
	return fmt.Sprintf("%d/%d", n, newest.UnixNano())
}
