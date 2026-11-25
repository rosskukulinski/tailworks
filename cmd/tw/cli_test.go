package main_test

// Hermetic end-to-end test of the tailworks CLI: the real binaries (tailworks,
// tw-mgmt, tw-hostd) against testcontrol + local DERP and a fake
// Tailscale API. It runs the same scenario as E2E-MAC (e2e/cliflow).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/internal/procsb"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"tailscale.com/tailcfg"
)

// fakeTS is the subset of the Tailscale API tailworks uses.
type fakeTS struct {
	mu        sync.Mutex
	policy    []byte
	etag      int
	keys      map[string]bool // id → revoked
	devDel    map[string]bool
	n         int
	setPolicy int
	// OAuth (M12): the one client this fake knows, and its tokens.
	oauthID, oauthSecret string
	oauthTokens          map[string]bool
	oauthExchanges       int
	bearers              map[string]int // bearer → requests (which credential minted)
}

func newFakeTS(t *testing.T) *fakeTS {
	base := `{
  "tagOwners": {"tag:tw": ["autogroup:admin"], "tag:ttest": ["autogroup:admin"]},
  "grants": [{"src": ["tag:ttest"], "dst": ["tag:ttest"], "ip": ["*"], "app": {"example.com/cap/ttest": [{"role": "tester"}]}}],
}`
	p, err := policy.MergeWith([]byte(base), cliflow.Policy(), policy.Options{AllowTags: []string{cliflow.TestTag}})
	if err != nil {
		t.Fatal(err)
	}
	return &fakeTS{policy: p, keys: map[string]bool{}, devDel: map[string]bool{}, oauthTokens: map[string]bool{}, bearers: map[string]int{},
		oauthID: fakeOAuthID, oauthSecret: fakeOAuthSecret}
}

func (f *fakeTS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	if p == "/api/v2/oauth/token" {
		r.ParseForm()
		if f.oauthSecret == "" || r.Form.Get("client_id") != f.oauthID || r.Form.Get("client_secret") != f.oauthSecret {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		f.oauthExchanges++
		tok := fmt.Sprintf("oauth-access-%d", f.oauthExchanges)
		f.oauthTokens[tok] = true
		json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "expires_in": 3600})
		return
	}
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.bearers[bearer]++
	if strings.HasPrefix(bearer, "oauth-access-") && !f.oauthTokens[bearer] {
		w.WriteHeader(401)
		return
	}
	switch {
	case r.Method == "GET" && strings.HasSuffix(p, "/devices"):
		w.WriteHeader(403) // the OAuth client has no devices:core read: tw falls back to the netmap / --tailnet
	case r.Method == "GET" && strings.HasSuffix(p, "/acl"):
		w.Header().Set("ETag", fmt.Sprintf(`"e%d"`, f.etag))
		w.Write(f.policy)
	case r.Method == "POST" && strings.HasSuffix(p, "/acl/validate"):
		w.Write([]byte(`{}`))
	case r.Method == "POST" && strings.HasSuffix(p, "/acl"):
		f.setPolicy++
		w.WriteHeader(500)
	case r.Method == "POST" && strings.HasSuffix(p, "/keys"):
		f.n++
		id := fmt.Sprintf("k%d", f.n)
		f.keys[id] = false
		json.NewEncoder(w).Encode(map[string]string{"id": id, "key": nettest.DummyKey})
	case strings.Contains(p, "/keys/"):
		id := p[strings.LastIndex(p, "/")+1:]
		revoked, ok := f.keys[id]
		switch {
		case !ok || (r.Method == "GET" && revoked):
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"not found"}`))
		case r.Method == "DELETE":
			f.keys[id] = true
		default:
			// single-use keys are consumed by the join
			json.NewEncoder(w).Encode(map[string]any{"id": id, "invalid": true})
		}
	case strings.HasPrefix(p, "/api/v2/device/"):
		id := strings.TrimPrefix(p, "/api/v2/device/")
		if r.Method == "DELETE" {
			f.devDel[id] = true
			return
		}
		if f.devDel[id] {
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"not found"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": id, "nodeId": id})
	default:
		w.WriteHeader(404)
	}
}

func TestCLIEndToEnd(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	ctl.Server.SetGlobalAppCaps(tailcfg.PeerCapMap{
		nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue},
		cliflow.MCPCap:  []tailcfg.RawMessage{`{"tools":["lookup_order"]}`},
	})
	fts := newFakeTS(t)
	api := httptest.NewServer(fts)
	defer api.Close()

	bin := cliflow.BuildBinaries(t)
	state, _ := hostdtest.ShortDir("tw-cli-")
	work := t.TempDir()
	keyFile := filepath.Join(work, "api.env")
	writeFakeOAuthFile(t, keyFile)
	res := cliflow.NewResults("hermetic", "ttest.ts.net")
	var nodes []*nettest.Node
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: state, WorkDir: work, Tailnet: "ttest.ts.net", OAuthClientFile: keyFile,
		ControlURL: ctl.URL, TSAPIURL: api.URL, Prefix: "tw-t-", Res: res,
		NewNode: func(role string) *nettest.Node {
			n, err := nettest.NewNode(context.Background(), ctl.URL, nettest.DummyKey, "ttest-"+role, filepath.Join(work, role))
			if err != nil {
				t.Fatal(err)
			}
			nodes = append(nodes, n)
			return n
		}}
	t.Cleanup(func() {
		if os.Getenv("TW_KEEP") != "" {
			t.Logf("TW_KEEP: state left running in %s", state)
			return
		}
		if _, _, _, err := e.Tw("status"); err == nil || t.Failed() {
			for _, c := range []string{"supervisor", "mgmt", "hostd"} {
				if t.Failed() {
					b, _ := os.ReadFile(filepath.Join(state, "logs", c+".log"))
					t.Logf("%s log (tail):\n%s", c, hostdtest.Trim(string(b[max(0, len(b)-5000):]), 5000))
				}
			}
			e.Tw("down")
		}
		for _, n := range nodes {
			n.Close()
		}
		os.RemoveAll(state)
	})
	e.Run()
	downRevokesWarmKeys(t, e, fts)
	// `tw down --purge && tw up && tw deploy` on the same state dir.
	e.RedeployAfterPurge()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	devs, keys := cliflow.Leftovers(ctx, &tsapi.Client{Tailnet: "ttest.ts.net", APIKey: "fake", BaseURL: api.URL}, res)
	if len(res.NodeIDs) < 5 || len(devs) > 0 || len(keys) > 0 {
		t.Fatalf("after tw down: %d node IDs tracked, leftover devices %v, keys %v", len(res.NodeIDs), devs, keys)
	}
	if fts.setPolicy != 0 {
		t.Fatalf("tw wrote the policy without --apply-policy (%d times)", fts.setPolicy)
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	t.Logf("results:\n%s", b)
}

// TestCLILink is M10 hermetically: `tw up` in oauth mode with mgmt's own
// tsnet node on testcontrol (unattended join), then a second state dir on
// the same machine acts as a developer box: `tw link` through its own
// tsnet node (the test transport), remote deploy / ls / kill / audit with
// deployedBy = the WhoIs principal, the workload's `logs` (the daemons'
// `logs --component` refused), a non-admin refused
// with the grant snippet, and --unlink.
func TestCLILink(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	// Every peer holds the admin cap toward every other (global caps): the
	// linked CLI is an admin. The non-admin path is covered below with a
	// fake WhoIs, since testcontrol can't grant per peer.
	ctl.Server.SetGlobalAppCaps(tailcfg.PeerCapMap{
		nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue},
		cliflow.MCPCap:  []tailcfg.RawMessage{`{"tools":["lookup_order"]}`},
		mgmt.AdminCap:   []tailcfg.RawMessage{`{}`},
	})
	fts := newFakeTS(t)
	api := httptest.NewServer(fts)
	defer api.Close()
	bin := cliflow.BuildBinaries(t)
	state, _ := hostdtest.ShortDir("tw-lk-")
	work := t.TempDir()
	keyFile := filepath.Join(work, "api.env")
	writeFakeOAuthFile(t, keyFile)
	authFile := filepath.Join(work, "auth.env")
	os.WriteFile(authFile, []byte("TS_AUTHKEY="+nettest.DummyKey+"\n"), 0o600)
	host := &cliflow.Env{T: t, BinDir: bin, StateDir: state, WorkDir: work, Tailnet: "ttest.ts.net", OAuthClientFile: keyFile,
		ControlURL: ctl.URL, TSAPIURL: api.URL, Prefix: "tw-l-", Res: cliflow.NewResults("hermetic-link", "ttest.ts.net")}
	devState := filepath.Join(work, "dev-state")
	dev := &cliflow.Env{T: t, BinDir: bin, StateDir: devState, WorkDir: work, Res: host.Res}
	t.Cleanup(func() {
		if t.Failed() {
			for _, c := range []string{"supervisor", "mgmt", "hostd"} {
				b, _ := os.ReadFile(filepath.Join(state, "logs", c+".log"))
				t.Logf("%s log (tail):\n%s", c, hostdtest.Trim(string(b[max(0, len(b)-5000):]), 5000))
			}
		}
		host.Tw("down", "--purge")
		os.RemoveAll(state)
	})
	var up struct {
		MgmtNode, MgmtIP, MintMode string
		Timings                    map[string]float64 `json:"timingsMs"`
	}
	host.MustJSON(&up, "up", "--oauth-client-file", keyFile, "--tailnet", "ttest.ts.net", "--yes", "--ephemeral", "--control-url", ctl.URL,
		"--tsapi-url", api.URL, "--hostname-prefix", "tw-l-", "--mgmt-hostname", "tw-mgmt", "--mgmt-authkey-file", authFile)
	if up.MgmtIP == "" || up.MgmtNode == "" || up.MintMode != "oauth" {
		t.Fatalf("tw up with a mgmt node: %+v", up)
	}
	if _, se, _, err := host.Tw("up", "--control", "tailscale", "--api-key-file", keyFile, "--yes", "--control-url", ctl.URL); err == nil || !strings.Contains(se, "mint_mode_removed") {
		t.Fatalf("tw up --api-key-file (Tailscale) should be mint_mode_removed: %v %s", err, se)
	}
	// A second tw up (supervisor running) must not touch the node.
	host.MustJSON(nil, "up", "--oauth-client-file", keyFile, "--tailnet", "ttest.ts.net", "--yes", "--ephemeral", "--control-url", ctl.URL,
		"--tsapi-url", api.URL, "--hostname-prefix", "tw-l-", "--mgmt-hostname", "tw-mgmt")
	// tw status names the saved --ephemeral (#456); nothing unsafe is saved.
	if st := checkStatus(t, host, "› saved       --ephemeral"); st["unsafeFlags"] == nil || len(st["unsafeFlags"].([]any)) != 0 {
		t.Fatalf("host status unsafeFlags: %v", st["unsafeFlags"])
	}

	// The developer box: linked through its own tsnet node (test transport;
	// the product path is the machine's Tailscale). No HTTPS on testcontrol.
	ep := "http://" + up.MgmtIP
	// Auto-discovery resolves `tw-mgmt` through this machine's MagicDNS;
	// a developer Mac may run a real tw-mgmt there, so point it at a name
	// that can't resolve (the test never talks to the machine's tailnet).
	t.Setenv("TW_TEST_LINK_NAME", "tw-mgmt-hermetic.invalid")
	if _, se, _, err := dev.Tw("link"); err == nil || !strings.Contains(se, "mgmt_not_found") {
		t.Fatalf("link auto-discovery without MagicDNS should fail mgmt_not_found: %v %s", err, se)
	}
	var lk struct {
		Endpoint, Login, Actor string
		Admin                  bool
		Tenants                []struct{ TenantID, MintMode string }
	}
	d := dev.MustJSON(&lk, "link", ep, "--tsnet", "--tsnet-dir", filepath.Join(work, "dev-node"), "--tsnet-hostname", "ttest-dev", "--control-url", ctl.URL, "--tsnet-authkey-file", authFile)
	if !lk.Admin || lk.Endpoint != ep || len(lk.Tenants) != 1 || lk.Tenants[0].MintMode != "oauth" || lk.Actor == "" {
		t.Fatalf("tw link: %+v", lk)
	}
	host.Res.Numbers["link_ms"] = float64(d.Milliseconds())
	if _, se, _, err := dev.Tw("logs", "--component", "mgmt"); err == nil || !strings.Contains(se, "remote_unsupported") {
		t.Fatalf("remote logs: %v %s", err, se)
	}
	if _, se, _, err := dev.Tw("tenant", "add", "x.ts.net", "--oauth-client-file", keyFile); err == nil || !strings.Contains(se, "remote_unsupported") {
		t.Fatalf("remote tenant add: %v %s", err, se)
	}
	// Remote deploy (bundled locally, uploaded), ls with deployedBy, kill, audit.
	agentDir := filepath.Join(work, "agent")
	dev.MustJSON(nil, cliflow.InitArgs(agentDir, "agent")...)
	var dep struct {
		DeploymentID string
		Endpoint     struct{ Node, MagicDNS string }
	}
	d = dev.MustJSON(&dep, "deploy", agentDir)
	host.Res.Numbers["remote_deploy_ms"] = float64(d.Milliseconds())
	if dep.Endpoint.Node == "" {
		t.Fatalf("remote deploy: %+v", dep)
	}
	var ls struct {
		Deployments []struct{ Workload, DeployedBy string }
		Objects     []struct{ ObjectID, Status string }
	}
	dev.MustJSON(&ls, "ls")
	if len(ls.Deployments) != 1 || ls.Deployments[0].DeployedBy != lk.Actor || !strings.Contains(ls.Deployments[0].DeployedBy, "ttest-dev") && !strings.Contains(ls.Deployments[0].DeployedBy, "@") {
		t.Fatalf("remote ls deployedBy: %+v (actor %q)", ls.Deployments, lk.Actor)
	}
	// Linked tw status: a count per tenant, and the same $defs/status (#455).
	if st := checkStatus(t, dev, "1 deployment (tw ls)"); st["stateDir"] == "" || st["linked"] == nil {
		t.Fatalf("linked status: stateDir %v linked %v", st["stateDir"], st["linked"])
	}
	// The workload's log, read by mgmt from the host over the push channel.
	if _, se, _, err := dev.Tw("logs", ls.Deployments[0].Workload, "-n", "5"); err != nil {
		t.Fatalf("remote logs of the workload: %v %s", err, se)
	}
	var kr struct {
		Result struct{ Killed []string }
	}
	dev.MustJSON(&kr, "kill", dep.Endpoint.Node, "--cascade", "--reason", "m10 remote kill")
	if len(kr.Result.Killed) != 1 {
		t.Fatalf("remote kill: %+v", kr)
	}
	var aud struct {
		Verified bool
		Records  []struct{ Kind, ActingFor string }
	}
	dev.MustJSON(&aud, "audit", "--all")
	var sawDeploy, sawKill bool
	for _, r := range aud.Records {
		if r.Kind == "deploy" && r.ActingFor == lk.Actor {
			sawDeploy = true
		}
		if r.Kind == "kill" {
			sawKill = true
		}
	}
	if !aud.Verified || !sawDeploy || !sawKill {
		t.Fatalf("remote audit: verified=%v deploy=%v kill=%v (%d records)", aud.Verified, sawDeploy, sawKill, len(aud.Records))
	}
	// The host's own CLI still works locally (unix socket + token) and sees the remote deploy.
	host.MustJSON(&ls, "ls", "--all")
	if len(ls.Deployments) != 1 {
		t.Fatalf("local ls after remote deploy: %+v", ls.Deployments)
	}
	// Non-admin: a fake mgmt whose WhoIs grants nothing → not_admin + snippet.
	srv := httptest.NewServer((&mgmt.Server{M: nil}).TailnetHandler(func(ctx context.Context, addr string) (mgmt.WhoIsInfo, error) {
		return mgmt.WhoIsInfo{NodeID: "nEVE", Login: "eve@example.com"}, nil
	}, http.NotFoundHandler()))
	defer srv.Close()
	noadmin := &cliflow.Env{T: t, BinDir: bin, StateDir: filepath.Join(work, "noadmin-state"), WorkDir: work, Res: host.Res}
	_, se, _, err := noadmin.Tw("link", srv.URL)
	if err == nil || !strings.Contains(se, "not_admin") || !strings.Contains(se, mgmt.AdminCap) || !strings.Contains(se, `"dst": ["`+mgmt.MgmtTag+`"]`) || !strings.Contains(se, "eve@example.com") {
		t.Fatalf("non-admin link should print not_admin with the grant snippet: %v\n%s", err, se)
	}
	if _, err := os.Stat(filepath.Join(work, "noadmin-state", "link.json")); !os.IsNotExist(err) {
		t.Fatal("a refused link must not be saved")
	}
	// Unlink.
	dev.MustJSON(nil, "link", "--unlink")
	if _, se, _, err := dev.Tw("ls"); err == nil || !strings.Contains(se, "not_up") {
		t.Fatalf("after unlink, ls should be not_up: %v %s", err, se)
	}
	host.Down()
	b, _ := json.MarshalIndent(host.Res, "", "  ")
	t.Logf("results:\n%s", b)
}

func TestDevOnce(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	bin := cliflow.BuildBinaries(t)
	work := t.TempDir()
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: filepath.Join(work, "state"), WorkDir: work}
	e.MustJSON(nil, "init", filepath.Join(work, "hello"), "--template", "hello")
	var r struct {
		Smoke struct {
			Status float64 `json:"status"`
			Caller struct {
				NodeID string `json:"nodeId"`
			} `json:"caller"`
		} `json:"smoke"`
	}
	e.MustJSON(&r, "dev", filepath.Join(work, "hello"), "--once", "--port", "0")
	if r.Smoke.Status != 200 || r.Smoke.Caller.NodeID == "" {
		t.Fatalf("tw dev --once: %+v", r)
	}
}

// TestCLIMultiTenant is E2E-MULTI hermetically: one host (tw up) serves
// tenant A (testcontrol A) and tenant B (`tw tenant add`, testcontrol B)
// with the same agent app; e2e/cliflow MultiEnv checks (a)–(f).
func TestCLIMultiTenant(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	ctls := map[string]*nettest.Control{}
	for _, k := range []string{"a", "b"} {
		c, err := nettest.StartControl()
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.Server.SetGlobalAppCaps(tailcfg.PeerCapMap{
			nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue},
			cliflow.MCPCap:  []tailcfg.RawMessage{`{"tools":["lookup_order"]}`}, // check (d): the narrowed child keeps lookup_order
		})
		ctls[k] = c
	}
	fts := newFakeTS(t)
	api := httptest.NewServer(fts)
	defer api.Close()
	bin := cliflow.BuildBinaries(t)
	state, _ := hostdtest.ShortDir("tw-mt-")
	work := t.TempDir()
	var nodes []*nettest.Node
	tenant := func(k string) *cliflow.Tenant {
		kf := writeFakeOAuthFile(t, filepath.Join(work, k+"-oauth.env"))
		return &cliflow.Tenant{Tailnet: k + ".ttest.ts.net", OAuthClientFile: kf, ControlURL: ctls[k].URL,
			Res: cliflow.NewResults("hermetic-"+k, k+".ttest.ts.net"),
			NewNode: func(role string) *nettest.Node {
				n, err := nettest.NewNode(context.Background(), ctls[k].URL, nettest.DummyKey, "ttest-"+role, filepath.Join(work, role))
				if err != nil {
					t.Fatal(err)
				}
				nodes = append(nodes, n)
				return n
			}}
	}
	m := &cliflow.MultiEnv{Env: &cliflow.Env{T: t, BinDir: bin, StateDir: state, WorkDir: work, TSAPIURL: api.URL, Prefix: "tw-m-",
		Res: cliflow.NewResults("hermetic-multi", "a+b")}, A: tenant("a"), B: tenant("b"), RequireSandbox: sandboxAvailable()}
	t.Cleanup(func() {
		if os.Getenv("TW_KEEP") != "" {
			t.Logf("TW_KEEP: state left running in %s", state)
			return
		}
		if t.Failed() {
			for _, c := range []string{"supervisor", "mgmt", "hostd"} {
				b, _ := os.ReadFile(filepath.Join(state, "logs", c+".log"))
				t.Logf("%s log (tail):\n%s", c, hostdtest.Trim(string(b[max(0, len(b)-5000):]), 5000))
			}
		}
		m.Tw("down")
		for _, n := range nodes {
			n.Close()
		}
		os.RemoveAll(state)
	})
	m.Run()
	m.Down()
	if fts.setPolicy != 0 {
		t.Fatalf("tw wrote a policy without --apply-policy (%d times)", fts.setPolicy)
	}
	b, _ := json.MarshalIndent(m.Res, "", "  ")
	t.Logf("results:\n%s", b)
}

func TestDoctor(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip("needs workerd (make deps)")
	}
	bin := cliflow.BuildBinaries(t)
	work := t.TempDir()
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: filepath.Join(work, "state"), WorkDir: work}
	var r struct {
		OK     bool `json:"ok"`
		Checks []struct {
			Name, Status, Detail string
		} `json:"checks"`
	}
	e.MustJSON(&r, "doctor")
	got := map[string]string{}
	for _, c := range r.Checks {
		got[c.Name] = c.Status
	}
	want := map[string]string{"binaries": "ok", "workerd": "ok", "bundler": "ok", "tw_home_writable": "ok",
		"state_dir_writable": "ok", "mint_mode": "skip", "transport": "skip", "api_key": "skip", "https_certs": "skip", "sandbox": "skip", "host_sockets": "skip", "worker_internet": "skip", "node_egress": "skip", "system_tailscaled_not_used": "ok",
		"store_sizes": "skip",
		"containers":  "skip",
		"node_budget": "skip",
		"install":     "info", // a test build: not an install.sh prefix or npm package (npm_install_test.go covers those)
	}
	// data_disk depends on the machine's free space: present, ok or warn.
	if st := got["data_disk"]; st != "ok" && st != "warn" {
		t.Fatalf("doctor: data_disk = %q: %v", st, r.Checks)
	}
	delete(got, "data_disk")
	// workerd_age depends on today's date: ok while the pin is under 30 days old, warn after (doctor_workerdage.go).
	if st := got["workerd_age"]; st != "ok" && st != "warn" {
		t.Fatalf("doctor: workerd_age = %q: %v", st, r.Checks)
	}
	delete(got, "workerd_age")
	// side_channels depends on the host's CPU and kernel: on Linux ok or
	// warn, never a failure (doctor_sidechannel.go); elsewhere skip.
	want["side_channels"] = "skip"
	if runtime.GOOS == "linux" {
		if st := got["side_channels"]; st != "ok" && st != "warn" {
			t.Fatalf("doctor: side_channels = %q: %v", st, r.Checks)
		}
		delete(got, "side_channels")
		delete(want, "side_channels")
	}
	if runtime.GOOS == "linux" {
		want["host_sockets"] = "ok"
		if hs := procsb.ScanHostSockets(); len(hs.Hard) > 0 {
			want["host_sockets"] = "warn"
		} else if hs.Tailscaled != "" {
			want["host_sockets"] = "info" // blocked for workloads by seccomp (tailworks is not up here)
			if !sandboxAvailable() {
				want["host_sockets"] = "warn"
			}
		}
	}
	if sandboxAvailable() {
		want["sandbox"] = "ok" // the Seatbelt / Landlock probe ran and enforced a deny
	} else if runtime.GOOS == "linux" {
		want["sandbox"] = "warn" // Linux without Landlock: loud warning
	}
	if runtime.GOOS == "linux" {
		// Linux host checks: present, never failing on a supported host
		// (kvm/firecracker may skip, systemd/landlock may warn). No microVM
		// guest sits next to this test build, so microvm_tier is warn (the
		// tier is off; L3's helper status is what it reports).
		for _, n := range []string{"glibc", "kernel", "landlock", "user_namespaces", "kvm", "firecracker", "cgroups_v2", "systemd", "microvm_tier"} {
			st, ok := got[n]
			if !ok || st == "fail" || (n == "microvm_tier" && st != "warn") {
				t.Fatalf("doctor on linux: %s = %q: %v", n, st, r.Checks)
			}
			delete(got, n)
		}
		if st := r.Checks[1]; st.Name != "glibc" || st.Status != "ok" {
			t.Fatalf("doctor on linux: glibc must run before workerd and be ok: %+v", st)
		}
	}
	if !r.OK || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("doctor: ok=%v %v", r.OK, r.Checks)
	}
	if _, err := os.Stat(filepath.Join(work, "state")); !os.IsNotExist(err) {
		t.Fatalf("doctor created the state dir")
	}
}

// sandboxAvailable: this host runs process-tier workloads sandboxed
// (Seatbelt on macOS, Landlock on Linux).
func sandboxAvailable() bool {
	sb, _ := hostd.ResolveSandbox(hostd.SandboxAuto)
	return sb == hostd.SandboxSeatbelt || sb == hostd.SandboxLandlock
}

// TestCLIOAuth is M12 hermetically: `tw up --oauth-client-file` (mint mode
// oauth: no browser, no Funnel, no API key anywhere) against testcontrol
// and a fake Tailscale API with an OAuth token endpoint. Refusals first
// (no mode → the three options; a group-readable file; a policy without
// tag:tw as owner of tag:tw-mgmt/tag:tw-host → policy_missing_tag_owners
// before anything is minted), then the whole E2E-MAC scenario (a)–(f) in
// oauth mode, with every key minted by OAuth access tokens.
func TestCLIOAuth(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	ctl.Server.SetGlobalAppCaps(tailcfg.PeerCapMap{
		nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue},
		cliflow.MCPCap:  []tailcfg.RawMessage{`{"tools":["lookup_order"]}`},
	})
	fts := newFakeTS(t)
	fts.oauthID, fts.oauthSecret = "kTestOAuthCNTRL", "tskey-"+"client-kTestOAuthCNTRL-"+strings.Repeat("S", 24)
	api := httptest.NewServer(fts)
	defer api.Close()
	bin := cliflow.BuildBinaries(t)
	state, _ := hostdtest.ShortDir("tw-oa-")
	work := t.TempDir()
	cred := filepath.Join(work, "oauth.env")
	os.WriteFile(cred, []byte("TS_OAUTH_CLIENT_ID="+fts.oauthID+"\nTS_OAUTH_CLIENT_SECRET="+fts.oauthSecret+"\n"), 0o644)
	os.Chmod(cred, 0o644)
	res := cliflow.NewResults("hermetic-oauth", "ttest.ts.net")
	var nodes []*nettest.Node
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: state, WorkDir: work, Tailnet: "ttest.ts.net", OAuthClientFile: cred,
		ControlURL: ctl.URL, TSAPIURL: api.URL, Prefix: "tw-o-", Res: res,
		NewNode: func(role string) *nettest.Node {
			n, err := nettest.NewNode(context.Background(), ctl.URL, nettest.DummyKey, "ttest-"+role, filepath.Join(work, role))
			if err != nil {
				t.Fatal(err)
			}
			nodes = append(nodes, n)
			return n
		}}
	t.Cleanup(func() {
		if t.Failed() {
			for _, c := range []string{"supervisor", "mgmt", "hostd"} {
				b, _ := os.ReadFile(filepath.Join(state, "logs", c+".log"))
				t.Logf("%s log (tail):\n%s", c, hostdtest.Trim(string(b[max(0, len(b)-5000):]), 5000))
			}
		}
		e.Tw("down", "--purge")
		for _, n := range nodes {
			n.Close()
		}
		os.RemoveAll(state)
	})
	up := []string{"up", "--oauth-client-file", cred, "--tailnet", "ttest.ts.net", "--yes", "--control-url", ctl.URL, "--tsapi-url", api.URL,
		"--hostname-prefix", "tw-o-", "--mgmt-hostname", "tw-o-mgmt", "--host-id", "tw-o", "--ephemeral", "--allow-sibling-egress"}
	// 1. A group/world-readable client file (still 0644 here) is tightened
	// to 0600, not refused: it is the operator's own file. Checked after 2.
	// 2. Policy lacks tag:tw as an owner of the infra tags: typed error with
	// the snippet, BEFORE any key is minted or node joined.
	fts.mu.Lock()
	good := fts.policy
	fts.policy = []byte(`{"tagOwners": {"tag:tw": ["autogroup:admin"], "tag:tw-mgmt": ["autogroup:admin"]}}`)
	minted := fts.n
	fts.mu.Unlock()
	_, se, _, err := e.Tw(up...)
	if err == nil || !strings.Contains(se, "policy_missing_tag_owners") || !strings.Contains(se, `"tag:tw-mgmt": ["autogroup:admin", "tag:tw"]`) {
		t.Fatalf("policy without tag:tw owners: %v %s", err, se)
	}
	upApplyPolicyRefusals(t, e, up, func() (int, int) { // up_policyapply_ext_test.go
		fts.mu.Lock()
		defer fts.mu.Unlock()
		return fts.n - minted, fts.setPolicy
	})
	fts.mu.Lock()
	if fts.n != minted {
		t.Fatalf("%d keys minted before the policy was fixed", fts.n-minted)
	}
	fts.policy = good
	fts.mu.Unlock()
	if _, err := os.Stat(filepath.Join(state, "mgmt", "tsnet", "tailscaled.state")); err == nil {
		t.Fatal("mgmt's node joined before the policy was fixed")
	}
	if st, err := os.Stat(cred); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("0644 client file not tightened to 0600: %v %v", st.Mode(), err)
	}
	res.Pass("oauth_refusals", "0644 → tightened to 0600; no tag:tw owner → policy_missing_tag_owners, nothing minted")

	// 3. The scenario (a)–(f) in oauth mode.
	e.Run()
	var cfg struct {
		MintMode, APIKeyFile, OAuthClientFile, OAuthClientID, IssuerURL string
	}
	b, _ := os.ReadFile(filepath.Join(state, "tw.json"))
	json.Unmarshal(b, &cfg)
	if cfg.MintMode != "oauth" || cfg.APIKeyFile != "" || cfg.OAuthClientFile != cred || cfg.OAuthClientID != fts.oauthID || cfg.IssuerURL != "" ||
		strings.Contains(string(b), "tskey-") {
		t.Fatalf("tw.json in oauth mode: %+v", cfg)
	}
	var doc struct {
		Checks []struct{ Name, Status, Detail string }
	}
	e.MustJSON(&doc, "doctor")
	for _, c := range doc.Checks {
		if c.Name == "mint_mode" && (c.Status != "ok" || !strings.HasPrefix(c.Detail, "oauth: OAuth client "+fts.oauthID)) {
			t.Fatalf("doctor mint_mode: %s %s", c.Status, c.Detail)
		}
		if c.Name == "api_key" && c.Status != "skip" {
			t.Fatalf("doctor api_key in oauth mode: %s %s", c.Status, c.Detail)
		}
	}
	os.Chmod(cred, 0o640)
	e.MustJSON(&doc, "doctor")
	for _, c := range doc.Checks {
		if c.Name == "mint_mode" && (c.Status != "warn" || !strings.Contains(c.Detail, "0640")) {
			t.Fatalf("doctor mint_mode with a loose file: %s %s", c.Status, c.Detail)
		}
	}
	os.Chmod(cred, 0o600)
	e.Down()
	fts.mu.Lock()
	defer fts.mu.Unlock()
	if fts.oauthExchanges == 0 || fts.bearers["fake-not-a-key"] != 0 {
		t.Fatalf("oauth exchanges %d, API-key requests %d", fts.oauthExchanges, fts.bearers["fake-not-a-key"])
	}
	for _, c := range []string{"mgmt", "hostd", "supervisor"} {
		lb, _ := os.ReadFile(filepath.Join(state, "logs", c+".log"))
		if strings.Contains(string(lb), "SSSSSSSS") || strings.Contains(string(lb), "oauth-access-") {
			t.Fatalf("%s log carries the OAuth secret or an access token", c)
		}
	}
	res.Numbers["oauth_exchanges"] = float64(fts.oauthExchanges)
	out, _ := json.MarshalIndent(res, "", "  ")
	t.Logf("results:\n%s", out)
}

// TestCLISingleProcess is M13 hermetically: `tw up` in its default
// single-process layout (no --separate-host): one supervisor process runs
// mgmt (its own tsnet node, the only infrastructure device) with the host
// agent embedded, talking to mgmt in-process as the host principal and
// receiving kills through the in-process push. The whole E2E-MAC scenario
// (a)–(f) runs on it, then the layout is asserted: no tw-host-* node, no
// join key, status/doctor describe it, --separate-host on this state is a
// state_conflict, and tw down stops everything.
func TestCLISingleProcess(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	ctl.Server.SetGlobalAppCaps(tailcfg.PeerCapMap{
		nettest.TestCap: []tailcfg.RawMessage{nettest.TestCapValue},
		cliflow.MCPCap:  []tailcfg.RawMessage{`{"tools":["lookup_order"]}`},
	})
	fts := newFakeTS(t)
	// The policy an OAuth-mode tw up checks for: the fixture plus the
	// grant that makes autogroup:admin tailworks admins over the tailnet.
	withAdmin, err := policy.MergeWith(fts.policy, policy.Snippet{Grants: []map[string]any{policy.AdminGrant("autogroup:admin", mgmt.MgmtTag, mgmt.AdminCap)}},
		policy.Options{AllowSrcs: []string{"autogroup:admin"}})
	if err != nil {
		t.Fatal(err)
	}
	fts.policy = withAdmin
	api := httptest.NewServer(fts)
	defer api.Close()
	bin := cliflow.BuildBinaries(t)
	state, _ := hostdtest.ShortDir("tw-sp-")
	work := t.TempDir()
	keyFile := filepath.Join(work, "api.env")
	writeFakeOAuthFile(t, keyFile)
	res := cliflow.NewResults("hermetic-single", "ttest.ts.net")
	var nodes []*nettest.Node
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: state, WorkDir: work, Tailnet: "ttest.ts.net", OAuthClientFile: keyFile,
		ControlURL: ctl.URL, TSAPIURL: api.URL, Prefix: "tw-s-", Res: res, SingleProcess: true,
		NewNode: func(role string) *nettest.Node {
			n, err := nettest.NewNode(context.Background(), ctl.URL, nettest.DummyKey, "ttest-"+role, filepath.Join(work, role))
			if err != nil {
				t.Fatal(err)
			}
			nodes = append(nodes, n)
			return n
		}}
	t.Cleanup(func() {
		if os.Getenv("TW_KEEP") != "" {
			t.Logf("TW_KEEP: state left running in %s", state)
			return
		}
		if t.Failed() {
			for _, c := range []string{"supervisor", "mgmt", "hostd"} {
				b, _ := os.ReadFile(filepath.Join(state, "logs", c+".log"))
				t.Logf("%s log (tail):\n%s", c, hostdtest.Trim(string(b[max(0, len(b)-5000):]), 5000))
			}
		}
		e.Tw("down", "--purge")
		for _, n := range nodes {
			n.Close()
		}
		os.RemoveAll(state)
	})
	e.Run() // (a)–(f): deploy → ready, requests reach the objects, kill --cascade (in-process push), audit
	// One infrastructure device: mgmt's node (TrackInfra added the host's, which has none).
	if len(res.InfraNodeIDs) != 1 {
		t.Fatalf("infrastructure nodes %v: single-process mode must create mgmt's node only", res.InfraNodeIDs)
	}
	var cfg struct{ Mode, HostID string }
	b, _ := os.ReadFile(filepath.Join(state, "tw.json"))
	json.Unmarshal(b, &cfg)
	if cfg.Mode != "single" {
		t.Fatalf("tw.json mode %q", cfg.Mode)
	}
	// Status and the human output describe the layout.
	var st struct {
		Mode, Transport string
		Healthy         bool
		Host            bool
	}
	e.MustJSON(&st, "status")
	if !st.Healthy || !st.Host || st.Mode != "single" || !strings.Contains(st.Transport, "host embedded") {
		t.Fatalf("status: %+v", st)
	}
	so, _, _, err := e.Tw("status")
	if err != nil || !strings.Contains(so, "embedded in the mgmt process") {
		t.Fatalf("status text: %v\n%s", err, so)
	}
	again := []string{"up", "--oauth-client-file", keyFile, "--tailnet", "ttest.ts.net", "--yes", "--control-url", ctl.URL, "--tsapi-url", api.URL,
		"--hostname-prefix", "tw-s-", "--mgmt-hostname", "tw-s-mgmt", "--host-id", "tw-s", "--ephemeral", "--allow-sibling-egress"}
	so, se, _, err := e.Tw(again...)
	if err != nil || !strings.Contains(so, "embedded in tw-mgmt (single process") || !strings.Contains(so, "one process: mgmt + embedded host") || !strings.Contains(so, "✓ policy") {
		// (the fixture policy already has tag:tw-host and the host ⇄ mgmt grants, so no snippet is printed here)
		t.Fatalf("second tw up (running) should describe the embedded host: %v\n%s\n%s", err, so, se)
	}
	if _, se, _, err := e.Tw(append(again, "--separate-host")...); err == nil || !strings.Contains(se, "state_conflict") {
		t.Fatalf("--separate-host on a single-process state: %v %s", err, se)
	}
	// The supervisor is the only tailworks process: no tw-mgmt / tw-hostd children.
	var pid struct{ Supervisor bool }
	e.MustJSON(&pid, "status")
	pb, _ := os.ReadFile(filepath.Join(state, "supervisor.pid"))
	if kids := childPIDsOf(strings.TrimSpace(string(pb))); len(kids) != 0 {
		t.Fatalf("single-process supervisor has child daemons: %v", kids)
	}
	mlog, _ := os.ReadFile(filepath.Join(state, "logs", "mgmt.log"))
	hlog, _ := os.ReadFile(filepath.Join(state, "logs", "hostd.log"))
	if !strings.Contains(string(mlog), "tailnet node") || !strings.Contains(string(hlog), "transport=embedded") {
		t.Fatalf("component logs: mgmt %d bytes, hostd %d bytes (hostd must say transport=embedded)", len(mlog), len(hlog))
	}
	backupRestoreRunning(t, e, work) // issue #120
	e.Down()
	if fts.setPolicy != 0 {
		t.Fatalf("tw wrote the policy without --apply-policy (%d times)", fts.setPolicy)
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	t.Logf("results:\n%s", out)
}

// childPIDsOf lists the direct children of pid that are tailworks daemons
// (pgrep -P; the tailworks binary's own sandbox launcher helpers don't count).
func childPIDsOf(pid string) []string {
	out, err := exec.Command("pgrep", "-P", pid, "-l").Output()
	if err != nil {
		return nil
	}
	var kids []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(l, "tw-mgmt") || strings.Contains(l, "tw-hostd") {
			kids = append(kids, l)
		}
	}
	return kids
}
