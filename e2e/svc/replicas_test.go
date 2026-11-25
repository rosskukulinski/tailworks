//go:build e2esvc

// E2E-REPLICAS on tailnet 1: one stateless workload with "replicas": 2 on
// two real hosts behind one Tailscale Service. Host A comes from `tw up`;
// host B is a second tw-hostd on this machine joined with `tw host add`
// (its own data dir and tailnet node). The hello app fetches a tag:ttest
// echo node, whose WhoIs of the caller names the serving replica's workload
// node, so each answer says which replica served it.
//
// Checks: two replicas on distinct hosts; the Service answers with the
// caller's identity; `tw kill` of the serving replica's node fails traffic
// over to the other replica; a redeploy with "replicas": 1 removes the
// second replica and the Service keeps answering; tw down → no leftovers.
//
//	TW_E2E_API_KEY_FILE=~/.config/tailnet-agents-design/tailnet1_api.env TW_E2E_TAILNET=tail7906d5.ts.net \
//	  go test -tags e2esvc -run TestE2EReplicas -count=1 -v -timeout 20m ./e2e/svc/
//
// Policy as for E2E-SVC, plus tag:tw-wl-svc → tag:ttest tcp:80 (the echo).
package svc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/netd"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

func TestE2EReplicas(t *testing.T) {
	keyFile, tailnet := os.Getenv("TW_E2E_API_KEY_FILE"), os.Getenv("TW_E2E_TAILNET")
	if keyFile == "" || tailnet == "" {
		t.Skip("TW_E2E_API_KEY_FILE / TW_E2E_TAILNET not set")
	}
	if !allowedTailnets[tailnet] {
		t.Fatalf("refusing to run against tailnet %q (not in the allow list)", tailnet)
	}
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	t.Setenv("TW_WORKERD", wd)
	apiKey, err := tsapi.LoadEnvFile(keyFile, "TS_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	api := &tsapi.Client{Tailnet: tailnet, APIKey: apiKey}
	// tailworks mints with an OAuth client: this run's own, created with the
	// harness's API key and deleted when the test ends.
	oauthFile := cliflow.RealOAuthClientScopes(t, keyFile, tailnet, t.TempDir(), "tw-e2e-replicas-oauth", []string{"services"}) // the workload has a Service
	b := make([]byte, 3)
	rand.Read(b)
	run := hex.EncodeToString(b)
	testPrefix, prefix, _ := cliflow.RunPrefixes("rep", run)
	res := cliflow.NewResults("rep-"+run, tailnet)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if _, err := api.GetService(ctx, svcName); err == nil {
		t.Fatalf("%s already exists on %s (a previous run's leftover?); delete it first", svcName, tailnet)
	}
	quiet := slog.New(netd.NewRedactingHandler(slog.NewTextHandler(io.Discard, nil)))
	minter := tsapi.MintSource(api, quiet)
	tracker := tsapi.NewTracker(api)
	bin := cliflow.BuildBinaries(t)
	state := filepath.Join("/tmp", "tw-e2e-rep-"+run)
	work := t.TempDir()
	hostB := strings.TrimSuffix(prefix, "-") + "-b"
	hostBDir := filepath.Join("/tmp", "tw-e2e-repb-"+run)
	hostBRun := filepath.Join("/tmp", "tw-rb-"+run) // short: unix socket paths
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: state, WorkDir: work, Tailnet: tailnet, OAuthClientFile: oauthFile,
		ControlURL: prodControl, Prefix: prefix, Res: res, ClientTagged: true}
	var nodes []*nettest.Node
	var hostdB *exec.Cmd
	downDone := false
	t.Cleanup(func() {
		if !downDone {
			e.Tw("down", "--purge")
		}
		if hostdB != nil && hostdB.Process != nil {
			hostdB.Process.Signal(syscall.SIGTERM)
			done := make(chan struct{})
			go func() { hostdB.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				hostdB.Process.Kill()
			}
		}
		if t.Failed() {
			for _, f := range []string{filepath.Join(state, "logs", "mgmt.log"), filepath.Join(state, "logs", "hostd.log"), filepath.Join(hostBDir, "hostd.log")} {
				b, _ := os.ReadFile(f)
				t.Logf("%s (tail):\n%s", f, netd.Redact(string(b[max(0, len(b)-5000):])))
			}
		}
		for _, n := range nodes {
			n.Close()
		}
		cctx, cc := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cc()
		devs, keys := cliflow.Leftovers(cctx, api, res)
		for _, id := range res.NodeIDs {
			tracker.Track(id, "tw")
		}
		for _, id := range res.InfraNodeIDs {
			tracker.Track(id, "tw-infra")
		}
		crep := tracker.Cleanup(cctx, testPrefix)
		for _, k := range keys {
			api.DeleteKey(cctx, k)
		}
		minter.RevokeAll(cctx)
		if err := api.DeleteService(cctx, svcName); err != nil {
			t.Errorf("delete service: %v", err)
		}
		os.RemoveAll(state)
		os.RemoveAll(hostBDir)
		os.RemoveAll(hostBRun)
		out, _ := json.MarshalIndent(res, "", "  ")
		t.Logf("results:\n%s", netd.Redact(string(out)))
		if len(devs) > 0 || len(keys) > 0 {
			t.Errorf("after tw down: leftover tw devices %v, valid keys %d", devs, len(keys))
		}
		if crep.Left != 0 || len(crep.Untracked) > 0 {
			t.Errorf("cleanup left devices: %+v", crep)
		}
	})

	// Host A.
	var up struct {
		MgmtNodeID string `json:"mgmtNodeID"`
	}
	e.MustJSON(&up, "up", "--oauth-client-file", oauthFile, "--tailnet", tailnet, "--yes", "--hostname-prefix", prefix,
		"--control-url", prodControl, "--mgmt-hostname", prefix+"mgmt", "--host-id", strings.TrimSuffix(prefix, "-"), "--ephemeral",
		"--separate-host") // the two-process layout this suite was written against (cliflow.Env.SingleProcess)

	// Host B: a second tw-hostd on this machine, joined via `tw host add`.
	var join struct {
		JoinFile   string `json:"joinFile"`
		ControlURL string `json:"controlURL"`
		Mgmt       string `json:"mgmt"`
	}
	os.MkdirAll(hostBDir, 0o700)
	e.MustJSON(&join, "host", "add", hostB, "--out", filepath.Join(hostBDir, "join.env"))
	lf, _ := os.Create(filepath.Join(hostBDir, "hostd.log"))
	hostdB = exec.Command(filepath.Join(bin, "tw-hostd"), "-host-id", hostB, "-mgmt", join.Mgmt, "-tsnet-control-url", join.ControlURL,
		"-join-token-file", join.JoinFile, "-data-dir", filepath.Join(hostBDir, "data"), "-run-dir", hostBRun, "-mgmt-token-file", "",
		"-hostname-prefix", prefix+"b-", "-ephemeral", "-control-url", prodControl, "-workerd", wd, "-heartbeat", "1s")
	hostdB.Stdout, hostdB.Stderr = lf, lf
	if err := hostdB.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		var hosts []struct{ HostID, NodeID, Status string }
		e.MustJSON(&hosts, "host", "ls")
		live := 0
		for _, h := range hosts {
			if h.Status == "live" {
				live++
			}
		}
		if live == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("host B not live: %+v", hosts)
		}
		time.Sleep(time.Second)
	}
	e.TrackInfra(up.MgmtNodeID) // both hosts' own nodes
	res.Pass("two_hosts", strings.TrimSuffix(prefix, "-")+", "+hostB)

	// Test nodes: a client and a private echo the replicas call.
	newNode := func(role string) *nettest.Node {
		k, err := minter.AuthKey(ctx, netd.KeyRequest{ObjectID: role, Tags: []string{cliflow.TestTag}, Ephemeral: true})
		if err != nil {
			t.Fatal(netd.RedactError(err))
		}
		n, err := nettest.NewNode(ctx, prodControl, k, testPrefix+role, filepath.Join(work, role))
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
		st, _ := n.LC.StatusWithoutPeers(ctx)
		tracker.Track(string(st.Self.ID), role)
		return n
	}
	client, echo := newNode("client"), newNode("echo")
	if err := echo.ServeEcho(); err != nil {
		t.Fatal(err)
	}
	cst, _ := client.LC.StatusWithoutPeers(ctx)
	clientID := string(cst.Self.ID)

	dir := filepath.Join(work, "rephello")
	e.MustJSON(nil, "init", dir, "--template", "hello")
	writeCfg := func(n int) {
		cfg := fmt.Sprintf(`{
  "name": "rephello",
  "replicas": %d,
  "tailnet": { "tags": ["%s"], "service": "%s" }
}
`, n, wlTag, strings.TrimPrefix(svcName, "svc:"))
		if err := os.WriteFile(filepath.Join(dir, "tailworks.jsonc"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	type repDep struct {
		Endpoint struct {
			Node    string `json:"node"`
			Service *struct {
				MagicDNS string `json:"magicDNS"`
			} `json:"service"`
		} `json:"endpoint"`
		Replicas []struct{ Workload, DeploymentID, Host string } `json:"replicas"`
	}
	upstream := "UPSTREAM=http://" + testPrefix + "echo/"
	writeCfg(2)
	var dep repDep
	d := e.MustJSON(&dep, "deploy", dir, "--var", upstream)
	res.Numbers["deploy_2_replicas_ms"] = float64(d.Milliseconds())
	e.TrackObjectNodes()
	if len(dep.Replicas) != 2 || dep.Replicas[0].Host == dep.Replicas[1].Host || dep.Replicas[1].Workload != "rephello--r2" {
		t.Fatalf("replicas %+v", dep.Replicas)
	}
	res.Pass("replicas_distinct_hosts", dep.Replicas[0].Host+", "+dep.Replicas[1].Host)

	// Workload node FQDN → replica (from tw ls).
	nodeOf := map[string]string{}
	for _, o := range e.Objects() {
		if o.Namespace == "_workload" && strings.HasPrefix(o.Name, "rephello") {
			nodeOf[strings.TrimSuffix(o.MagicDNS, ".")] = o.Name
		}
	}
	url := "http://" + dep.Endpoint.Service.MagicDNS + "/"
	// served reports which replica answered (fresh connection each time).
	served := func() (string, error) {
		hc := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{DialContext: client.Dial, DisableKeepAlives: true}}
		resp, err := hc.Get(url)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		var body struct {
			Caller   struct{ NodeID string } `json:"caller"`
			Upstream struct {
				Body struct {
					Caller struct{ Node string } `json:"caller"`
				} `json:"body"`
			} `json:"upstream"`
		}
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || json.Unmarshal(raw, &body) != nil {
			return "", fmt.Errorf("%d %.200s", resp.StatusCode, raw)
		}
		if body.Caller.NodeID != clientID {
			return "", fmt.Errorf("caller %s, want %s", body.Caller.NodeID, clientID)
		}
		node := strings.TrimSuffix(body.Upstream.Body.Caller.Node, ".")
		if r, ok := nodeOf[node]; ok {
			return r, nil
		}
		return "", fmt.Errorf("upstream caller %q is not a replica's workload node %v", node, nodeOf)
	}
	until := func(what, want string, n int, d time.Duration) (string, time.Duration) {
		t.Helper()
		t0 := time.Now()
		streak, last := 0, ""
		var lastErr error
		for time.Since(t0) < d {
			r, err := served()
			switch {
			case err == nil && (want == "" || r == want) && (streak == 0 || r == last):
				streak, last = streak+1, r
			case err == nil && (want == "" || r == want):
				streak, last = 1, r
			default:
				streak, lastErr = 0, err
			}
			if streak >= n {
				return last, time.Since(t0)
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("%s: not %d consecutive answers within %v (last err %v)", what, n, d, lastErr)
		return "", 0
	}

	first, dt := until("service up", "", 3, 3*time.Minute)
	res.Numbers["service_first_ok_ms"] = float64(dt.Milliseconds())
	res.Pass("service_caller_identity", "served by "+first+", caller "+clientID)
	t.Logf("%s answering %v after deploy, served by %s", url, dt.Round(time.Millisecond), first)
	// Which replicas does a run of fresh connections reach?
	counts := map[string]int{}
	for range 20 {
		if r, err := served(); err == nil {
			counts[r]++
		}
	}
	t.Logf("20 fresh connections: %v", counts)
	res.Notes = append(res.Notes, fmt.Sprintf("distribution of 20 fresh connections: %v", counts))

	// Failover: stall the serving replica's workerd (SIGSTOP). Its host's
	// health probes fail, it withdraws the Service, and control routes the
	// client to the other replica.
	other := "rephello"
	if first == "rephello" {
		other = "rephello--r2"
	}
	sockOf := map[string]string{
		strings.TrimSuffix(prefix, "-"): filepath.Join(state, "run", "h", "hostd.sock"),
		hostB:                           filepath.Join(hostBRun, "hostd.sock"),
	}
	var firstHost string
	for _, r := range dep.Replicas {
		if r.Workload == first {
			firstHost = r.Host
		}
	}
	ws := hostWorkload(t, sockOf[firstHost], first)
	if ws.PID == 0 || ws.Service == nil || !ws.Service.Advertised {
		t.Fatalf("%s on %s: %+v", first, firstHost, ws)
	}
	if err := syscall.Kill(ws.PID, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	stalled := true
	defer func() {
		if stalled {
			syscall.Kill(ws.PID, syscall.SIGCONT)
		}
	}()
	_, dt = until("failover", other, 5, 3*time.Minute)
	res.Numbers["failover_after_stall_ms"] = float64(dt.Milliseconds())
	if w := hostWorkload(t, sockOf[firstHost], first); w.Service == nil || w.Service.Advertised {
		t.Fatalf("stalled replica %s still advertises the Service: %+v", first, w.Service)
	}
	res.Pass("failover_after_stall", fmt.Sprintf("%s stalled → withdrawn; 5 consecutive answers from %s after %v", first, other, dt.Round(time.Millisecond)))
	t.Logf("stalled %s (workerd SIGSTOP): withdrawn, 5 consecutive answers from %s %v later", first, other, dt.Round(time.Millisecond))
	syscall.Kill(ws.PID, syscall.SIGCONT)
	stalled = false
	back := time.Now()
	for {
		if w := hostWorkload(t, sockOf[firstHost], first); w.Service != nil && w.Service.Advertised {
			break
		}
		if time.Since(back) > time.Minute {
			t.Fatalf("%s did not re-advertise after SIGCONT", first)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("%s re-advertised %v after SIGCONT", first, time.Since(back).Round(time.Millisecond))

	// Scale down to one replica.
	writeCfg(1)
	var one repDep
	e.MustJSON(&one, "deploy", dir, "--var", upstream)
	e.TrackObjectNodes()
	if len(one.Replicas) != 0 {
		t.Fatalf("replicas: 1 deploy reports replicas %+v", one.Replicas)
	}
	for _, o := range e.Objects() {
		if o.Name == "rephello--r2" && o.Namespace == "_workload" && o.Status != "killed" {
			t.Fatalf("rephello--r2 still has a live node after scaling to 1: %+v", o)
		}
	}
	nodeOf = map[string]string{}
	for _, o := range e.Objects() {
		if o.Namespace == "_workload" && o.Name == "rephello" && o.Status == "ready" {
			nodeOf[strings.TrimSuffix(o.MagicDNS, ".")] = o.Name
		}
	}
	_, dt = until("after scale-down", "rephello", 3, 3*time.Minute)
	res.Pass("scale_down", fmt.Sprintf("served by rephello only, %v after the deploy", dt.Round(time.Millisecond)))

	e.Down()
	downDone = true
	time.Sleep(2 * time.Second)
}

// hostWorkload reads one workload's status from a tw-hostd status socket.
func hostWorkload(t *testing.T, sock, workload string) hostd.WorkloadStatus {
	t.Helper()
	hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, err := hc.Get("http://hostd/v1/status")
	if err != nil {
		t.Fatalf("hostd status %s: %v", sock, err)
	}
	defer resp.Body.Close()
	var st hostd.StatusView
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("hostd status %s: %v", sock, err)
	}
	for _, w := range st.Workloads {
		if w.Workload == workload {
			return w
		}
	}
	t.Fatalf("no workload %s on %s", workload, sock)
	return hostd.WorkloadStatus{}
}
