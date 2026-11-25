//go:build e2eupgrade

package upgrade

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

const vpkg = "github.com/rosskukulinski/tailworks/internal/version"

// build compiles tw-mgmt and tw-hostd from dir, stamped with v.
func build(t *testing.T, dir, v string) string {
	t.Helper()
	out := t.TempDir()
	ld := "-X " + vpkg + ".Version=" + v + " -X main.version=" + v
	cmd := exec.Command("go", "build", "-ldflags", ld, "-o", out+"/", "./cmd/tw-mgmt", "./cmd/tw-hostd")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build in %s: %v\n%s", dir, err, b)
	}
	return out
}

// previous builds the previous release (TW_UPGRADE_FROM) and returns its
// binaries and version.
func previous(t *testing.T, root string) (string, string) {
	t.Helper()
	ref := os.Getenv("TW_UPGRADE_FROM")
	if ref == "" || ref == "self" {
		v := os.Getenv("TW_UPGRADE_FROM_VERSION")
		if v == "" {
			v = "0.0.1"
		}
		t.Logf("previous release: this tree, stamped %s", v)
		return build(t, root, v), v
	}
	wt := filepath.Join(t.TempDir(), "prev")
	if b, err := exec.Command("git", "-C", root, "worktree", "add", "--detach", wt, ref).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add %s: %v\n%s", ref, err, b)
	}
	t.Cleanup(func() { exec.Command("git", "-C", root, "worktree", "remove", "--force", wt).Run() })
	vb, _ := os.ReadFile(filepath.Join(wt, "VERSION"))
	v := strings.TrimSpace(string(vb))
	t.Logf("previous release: %s (%s)", ref, v)
	return build(t, wt, v), v
}

func TestUpgradePreviousToCandidate(t *testing.T) {
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	if !hostdtest.HasNode() {
		t.Skip("node not available (needed to bundle the app)")
	}
	root, err := nettest.FindRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	oldBin, oldV := previous(t, root)
	vb, _ := os.ReadFile(filepath.Join(root, "VERSION"))
	newV := os.Getenv("TW_UPGRADE_TO_VERSION")
	if newV == "" {
		newV = strings.TrimSpace(string(vb))
	}
	newBin := build(t, root, newV)
	aheadBin := build(t, root, "9.0.0") // a host far outside the window

	dir, err := hostdtest.ShortDir("tw-upg-")
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
	e := &env{t: t, bin: oldBin, dir: dir, ctl: ctl, sock: filepath.Join(dir, "mgmt.sock"), procs: map[string]*proc{}, workerd: wd,
		binOf: map[string]string{}}
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
	startMgmt := func(bin string, rotate bool) {
		t.Helper()
		e.binOf["mgmt"] = bin
		os.Remove(e.sock)
		args := []string{"-local-dev", "-socket", e.sock, "-host-token-file", filepath.Join(dir, "token"),
			"-state-dir", filepath.Join(dir, "mgmt"), "-tsapi-url", ts.URL, "-reap-interval", "1h"}
		if rotate {
			args = append(args, "-rotate-host-token")
		}
		e.start("mgmt", nil, args...)
		for i := 0; ; i++ {
			b, err := os.ReadFile(filepath.Join(dir, "token"))
			if _, serr := os.Stat(e.sock); err == nil && len(bytes.TrimSpace(b)) == 64 && serr == nil {
				e.token = string(bytes.TrimSpace(b))
				if st, _ := e.do("GET", "/v1/healthz", nil, nil); st == 200 {
					return
				}
			}
			if i > 200 {
				e.dumpLogs()
				t.Fatal("mgmt did not start")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	waitLive := func(id string) {
		t.Helper()
		for i := 0; ; i++ {
			var h struct{ Status string }
			if st, _ := e.do("GET", "/v1/hosts/"+id, nil, &h); st == 200 && h.Status == "live" {
				return
			}
			if i > 300 {
				e.dumpLogs()
				t.Fatalf("host %s not live", id)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// ---- the previous release: mgmt, one host, a workload with DO storage.
	startMgmt(oldBin, true)
	var tn struct{ TenantID string }
	e.must("POST", "/v1/tenants", map[string]any{"tailnet": nettest.MagicDNSDomain, "controlURL": ctl.URL, "mintMode": "oauth", "oauthClientFile": keyFile}, &tn)
	e.binOf["h1"] = oldBin
	e.startHost("h1", false)
	waitLive("h1")

	cl := &tsnet.Server{Hostname: "upg-client", ControlURL: ctl.URL, AuthKey: nettest.DummyKey, Ephemeral: true, Store: new(mem.Store),
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
	os.WriteFile(filepath.Join(app, "tailworks.jsonc"), []byte(`{"name": "counter", "tailnet": {"tags": ["tag:ttest"], "objects": {"Counter": {"tags": ["tag:ttest"]}}}}`), 0o644)
	os.WriteFile(filepath.Join(app, "wrangler.jsonc"), []byte(`{"name": "counter", "main": "src/index.js", "compatibility_date": "2026-04-01",
  "durable_objects": {"bindings": [{"name": "COUNTER", "class_name": "Counter"}, {"name": "TALLY", "class_name": "Tally"}]}}`), 0o644)
	os.WriteFile(filepath.Join(app, "src", "index.js"), []byte(`import { DurableObject } from "cloudflare:workers";
export class Counter extends DurableObject {
  async fetch() { const n = ((await this.ctx.storage.get("n")) ?? 0) + 1; await this.ctx.storage.put("n", n); return Response.json({ count: n }); }
}
export class Tally extends Counter {}
export default { async fetch(req, env) {
  const ns = new URL(req.url).pathname === "/tally" ? env.TALLY : env.COUNTER;
  return ns.getByName("c").fetch(new Request("http://counter/"));
} };`), 0o644)
	// Tally is not in tailnet.objects. Packed by hostdtest.Build, as by a
	// tw before #492, it has no "node": false, so hostd runs it as a
	// nodeless facet (storage under Host_Tally); a hostd upgrade must keep
	// that storage (only a redeploy with a newer tw makes it a plain DO).
	files, err := hostdtest.Build(app, nil)
	if err != nil {
		t.Fatal(err)
	}
	bundle := base64.StdEncoding.EncodeToString(hostdtest.MustTar(files))
	var d deployment
	if st, b := e.do("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "counter", "bundle": bundle, "host": "h1"}, &d); st != 202 {
		e.dumpLogs()
		t.Fatalf("deploy: %d %s", st, b)
	}
	d = e.waitActive(d.DeploymentID, 90*time.Second)
	node := d.Endpoint.Node
	host := d.Endpoint.MagicDNS
	bumpAt := func(path string, want int) {
		t.Helper()
		var got struct{ Count int }
		last := ""
		for i := 0; ; i++ {
			resp, err := hc.Get("http://" + host + path)
			if err == nil {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode == 200 && json.Unmarshal(b, &got) == nil {
					break
				}
				last = resp.Status + " " + string(b)
			} else {
				last = err.Error()
			}
			if i > 100 {
				e.dumpLogs()
				t.Fatalf("GET %s%s: %s", host, path, last)
			}
			time.Sleep(200 * time.Millisecond)
		}
		if got.Count != want {
			t.Fatalf("GET %s: count %d, want %d (Durable Object storage lost?)", path, got.Count, want)
		}
	}
	bump := func(want int) {
		t.Helper()
		bumpAt("/", want)
		bumpAt("/tally", want) // the unlisted class
	}
	bump(1)
	bump(2)
	t.Logf("previous release %s serving: count 2 on %s", oldV, host)

	// ---- mgmt first: the candidate on the same store.
	e.stop("mgmt")
	startMgmt(newBin, false)
	var st struct {
		OK       bool
		Version  string
		Versions mgmt.VersionStatus
	}
	e.must("GET", "/v1/status", nil, &st)
	if st.Versions.Schema == nil || len(st.Versions.Schema.Pending) != 0 || st.Versions.Schema.Applied != st.Versions.Schema.Latest {
		t.Fatalf("schema after the mgmt upgrade: %+v", st.Versions.Schema)
	}
	waitLive("h1") // the previous release's host keeps working with the new mgmt
	bump(3)

	// ---- an app with wrangler "assets" (ADR-050) needs the
	// "assets" host feature: the new mgmt doesn't place it on a host that
	// lacks it (an older hostd would load assets/*.js as modules).
	siteFiles := map[string][]byte{
		"tailworks.jsonc":   []byte(`{"name": "site", "tailnet": {"tags": ["tag:ttest"]}}`),
		"wrangler.jsonc":    []byte(`{"name": "site", "main": "index.js", "compatibility_date": "2026-04-01", "assets": {"directory": "public"}}`),
		"index.js":          []byte(`export default { fetch() { return new Response("worker"); } };`),
		"assets/hello.txt":  []byte("hello from an asset"),
		"assets/index.html": []byte("<h1>site</h1>"),
	}
	siteBundle := base64.StdEncoding.EncodeToString(hostdtest.MustTar(siteFiles))
	var h1old struct{ Features []string }
	e.must("GET", "/v1/hosts/h1", nil, &h1old)
	if !slices.Contains(h1old.Features, "assets") {
		code, b := e.do("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "site", "bundle": siteBundle, "host": "h1"}, nil)
		if code == 202 || !strings.Contains(string(b), "assets") {
			t.Fatalf("an assets deploy to a host without the feature: %d %s, want a refusal naming it", code, b)
		}
	}

	// ---- then the host.
	e.stop("h1")
	e.binOf["h1"] = newBin
	e.startHost("h1", false)
	waitLive("h1")
	d = e.waitActive(d.DeploymentID, 90*time.Second)
	if d.Endpoint.Node != node {
		t.Fatalf("the workload's node changed across the host upgrade: %s → %s (it must rejoin from saved state, not re-register)", node, d.Endpoint.Node)
	}
	bump(4)
	var h1 struct {
		Version  string
		Features []string
	}
	e.must("GET", "/v1/hosts/h1", nil, &h1)
	if h1.Version != newV || len(h1.Features) == 0 {
		t.Fatalf("upgraded host reports %+v", h1)
	}
	// The upgraded host serves the assets app.
	var sd deployment
	if code, b := e.do("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "site", "bundle": siteBundle, "host": "h1"}, &sd); code != 202 {
		e.dumpLogs()
		t.Fatalf("assets deploy after the host upgrade: %d %s", code, b)
	}
	sd = e.waitActive(sd.DeploymentID, 90*time.Second)
	for i := 0; ; i++ {
		resp, err := hc.Get("http://" + sd.Endpoint.MagicDNS + "/hello.txt")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && string(b) == "hello from an asset" && strings.HasPrefix(resp.Header.Get("content-type"), "text/plain") {
				break
			}
			err = fmt.Errorf("%s %q", resp.Status, b)
		}
		if i > 100 {
			e.dumpLogs()
			t.Fatalf("GET /hello.txt on the assets app: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// ---- the previous mgmt still opens the expanded store (a rollback
	// without a restore works while no contract migration ran).
	e.stop("mgmt")
	startMgmt(oldBin, false)
	bump(5)
	e.stop("mgmt")
	startMgmt(newBin, false)
	bump(6)

	// ---- a host outside the version window is refused.
	e.binOf["h2"] = aheadBin
	e.startHost("h2", false)
	deadline := time.Now().Add(20 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(dir, "h2.log"))
		if strings.Contains(string(b), "version_skew") {
			break
		}
		if time.Now().After(deadline) {
			e.dumpLogs()
			t.Fatalf("a 9.0.0 host was not refused:\n%s", b)
		}
		time.Sleep(200 * time.Millisecond)
	}
	var h2 struct{ Status string }
	if code, _ := e.do("GET", "/v1/hosts/h2", nil, &h2); code == 200 && h2.Status == "live" {
		t.Fatal("the 9.0.0 host is live")
	}
	e.stop("h2")

	// ---- the fleet plan from mgmt's catalog: nothing left to upgrade.
	var plan mgmt.UpgradePlan
	if code, b := e.do("POST", "/v1/upgrades", map[string]any{"to": newV, "dryRun": true}, &plan); code == 200 {
		for _, s := range plan.Steps {
			if s.Kind == "host" && s.Host == "h1" && s.Status != "skipped" {
				t.Fatalf("h1 still needs an upgrade: %+v", s)
			}
		}
	} else if !strings.Contains(string(b), "not in the catalog") {
		t.Fatalf("plan: %d %s", code, b)
	}
	t.Logf("upgraded %s → %s: DO storage, node identity and the store survived", oldV, newV)
}
