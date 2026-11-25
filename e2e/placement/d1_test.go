//go:build e2eplacement

package placement

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/tsnet"
)

// TestD1SurvivesRestartAndMove (ADR-038): a D1 database written through the
// binding and through mgmt's operator API survives a restart of its
// hostd, and a lease move to another host (`--replace`), whose workerd
// starts with no DO storage and rebuilds the database from mgmt's
// journal. Real tw-mgmt and two real tw-hostd processes on testcontrol.
func TestD1SurvivesRestartAndMove(t *testing.T) {
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	if !hostdtest.HasNode() {
		t.Skip("node not available (needed to bundle the app)")
	}
	dir, err := hostdtest.ShortDir("tw-d1-")
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
			e.stop(n)
		}
	})
	e.hc = &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", e.sock)
	}}}
	keyFile := filepath.Join(dir, "api.env")
	cliflow.WriteFakeOAuthFile(t, keyFile)
	e.start("mgmt", nil, "-local-dev", "-socket", e.sock, "-host-token-file", filepath.Join(dir, "token"), "-rotate-host-token",
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
	e.startHost("h1", false)
	e.startHost("h2", false)
	for i := 0; ; i++ {
		var pg struct {
			Items []struct{ HostID, Status string }
		}
		e.must("GET", "/v1/hosts", nil, &pg)
		live := 0
		for _, h := range pg.Items {
			if h.Status == "live" {
				live++
			}
		}
		if live == 2 {
			break
		}
		if i > 200 {
			e.dumpLogs()
			t.Fatalf("hosts not live: %+v", pg.Items)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// A client node on the tailnet, to call the app.
	cl := &tsnet.Server{Hostname: "d1-client", ControlURL: ctl.URL, AuthKey: nettest.DummyKey, Ephemeral: true, Store: new(mem.Store),
		Dir: filepath.Join(dir, "client"), Logf: func(string, ...any) {}, UserLogf: func(string, ...any) {}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := cl.Up(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: cl.Dial}}

	app := filepath.Join(dir, "app")
	os.MkdirAll(filepath.Join(app, "src"), 0o755)
	os.WriteFile(filepath.Join(app, "tailworks.jsonc"), []byte(`{"name": "notes", "tailnet": {"tags": ["tag:ttest"]}}`), 0o644)
	os.WriteFile(filepath.Join(app, "wrangler.jsonc"), []byte(`{"name": "notes", "main": "src/index.js", "compatibility_date": "2026-04-01",
  "d1_databases": [{"binding": "DB", "database_name": "notes-db", "database_id": "00000000-0000-4000-8000-0000000000d1"}]}`), 0o644)
	os.WriteFile(filepath.Join(app, "src", "index.js"), []byte(`export default { async fetch(req, env) {
  const u = new URL(req.url);
  try {
    if (u.pathname === "/add") {
      const r = await env.DB.prepare("INSERT INTO notes (body) VALUES (?)").bind(u.searchParams.get("body")).run();
      return Response.json({ id: r.meta.last_row_id });
    }
    const { results } = await env.DB.prepare("SELECT id, body FROM notes ORDER BY id").all();
    return Response.json({ notes: results });
  } catch (e) { return Response.json({ error: String(e.message) }, { status: 500 }); }
} };`), 0o644)
	files, err := hostdtest.Build(app, nil)
	if err != nil {
		t.Fatal(err)
	}
	bundle := base64.StdEncoding.EncodeToString(hostdtest.MustTar(files))
	deploy := func(body map[string]any) deployment {
		t.Helper()
		body["tenantID"], body["bundle"], body["workload"] = tn.TenantID, bundle, "notes"
		var d deployment
		if st, b := e.do("POST", "/v1/deployments", body, &d); st != 202 {
			e.dumpLogs()
			t.Fatalf("deploy %v: %d %s", body, st, b)
		}
		return e.waitActive(d.DeploymentID, 90*time.Second)
	}
	d1 := "/v1/tenants/" + tn.TenantID + "/workloads/notes/d1/notes-db"
	query := func(sql string, params ...any) [][]any {
		t.Helper()
		q := map[string]any{"sql": sql}
		if len(params) > 0 {
			q["params"] = params
		}
		var rs []struct {
			Results struct{ Rows [][]any }
		}
		for i := 0; ; i++ { // right after a restart or move, the host may still be coming up
			st, b := e.do("POST", d1+"/query", map[string]any{"queries": []any{q}}, &rs)
			if st == 200 {
				return rs[0].Results.Rows
			}
			if i > 50 || st == 400 {
				e.dumpLogs()
				t.Fatalf("query %s: %d %s", sql, st, b)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	get := func(host, path string) map[string]any {
		t.Helper()
		for i := 0; ; i++ {
			resp, err := hc.Get("http://" + host + path)
			if err == nil {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				var m map[string]any
				if resp.StatusCode == 200 && json.Unmarshal(b, &m) == nil {
					return m
				}
				err = &net.AddrError{Err: string(b)}
			}
			if i > 50 {
				e.dumpLogs()
				t.Fatalf("GET %s%s: %v", host, path, err)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	notes := func(host string) string {
		t.Helper()
		b, _ := json.Marshal(get(host, "/")["notes"])
		return string(b)
	}

	// On h1: the schema and a row from the operator, a row through the binding.
	a1 := deploy(map[string]any{"host": "h1"})
	if a1.Host != "h1" {
		t.Fatalf("deployed on %s", a1.Host)
	}
	query("CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL)")
	query("INSERT INTO notes (body) VALUES (?)", "from the operator")
	get(a1.Endpoint.MagicDNS, "/add?body=from+the+worker+on+h1")
	want := `[{"body":"from the operator","id":1},{"body":"from the worker on h1","id":2}]`
	if got := notes(a1.Endpoint.MagicDNS); got != want {
		t.Fatalf("on h1: %s", got)
	}

	// h1's hostd restarts: it resumes the workload and the database is there.
	e.stop("h1")
	e.startHost("h1", false)
	for i := 0; e.hostWorkloads("h1")["notes"] != "active"; i++ {
		if i > 300 {
			e.dumpLogs()
			t.Fatalf("after restart: %v", e.hostWorkloads("h1"))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got := notes(a1.Endpoint.MagicDNS); got != want {
		t.Fatalf("after a hostd restart: %s", got)
	}

	// The lease moves to h2 (--replace): its workerd has no DO storage for
	// this workload, so the store object rebuilds the database from the
	// journal; it then takes writes, and h1 stops serving.
	moveAt := time.Now()
	a2 := deploy(map[string]any{"host": "h2", "replace": true, "forceTakeover": true})
	if a2.Host != "h2" || a2.Epoch <= a1.Epoch {
		t.Fatalf("move: on %s at epoch %d (was %d)", a2.Host, a2.Epoch, a1.Epoch)
	}
	if got := notes(a2.Endpoint.MagicDNS); got != want {
		t.Fatalf("on h2 after the move: %s", got)
	}
	readAfter := time.Since(moveAt)
	get(a2.Endpoint.MagicDNS, "/add?body=from+the+worker+on+h2")
	if rows := query("SELECT count(*) FROM notes"); len(rows) != 1 || rows[0][0] != float64(3) {
		t.Fatalf("operator count on h2: %v", rows)
	}
	for i := 0; ; i++ {
		if s := e.hostWorkloads("h1")["notes"]; s != "active" && s != "starting" {
			break
		}
		if i > 100 {
			t.Fatalf("h1 still serves notes after the move: %v", e.hostWorkloads("h1"))
		}
		time.Sleep(100 * time.Millisecond)
	}
	l, err := mgmt.OpenStore(context.Background(), filepath.Join(dir, "mgmt", "mgmt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	lease, err := l.GetLease(context.Background(), mgmt.LeaseName(tn.TenantID, "notes"))
	if err != nil || lease.Holder != "h2" {
		t.Fatalf("lease: %+v %v", lease, err)
	}
	var stores struct {
		Items []struct {
			Kind, Name string
			Head       int64
		}
	}
	e.must("GET", "/v1/tenants/"+tn.TenantID+"/workloads/notes/stores?kind=d1", nil, &stores)
	if len(stores.Items) != 1 || stores.Items[0].Name != "notes-db" || stores.Items[0].Head != 4 {
		t.Fatalf("journal: %+v (want 4 writes)", stores.Items)
	}
	if !strings.Contains(notes(a2.Endpoint.MagicDNS), "from the worker on h2") {
		t.Fatal("the write on h2 is not there")
	}
	t.Logf("MOVE: database readable on h2 %.2fs after --replace (rebuilt from a %d-entry journal)", readAfter.Seconds(), stores.Items[0].Head)
}
