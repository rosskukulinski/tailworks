//go:build e2eheadscale

package headscale_test

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestTwUp is the user flow on the Headscale profile, through the CLI:
// tw up (single process) with the profile's policy applied, a deploy, a
// call from another node of the Headscale tailnet with a verified caller,
// the Service refusal, kill (the node is deleted from Headscale) and down.
func TestTwUp(t *testing.T) {
	t.Run("single", func(t *testing.T) { testTwUp(t, nil) })
	// tw-mgmt and tw-hostd as separate processes: the host's own node
	// joins with a key minted through the Headscale API.
	t.Run("separate-host", func(t *testing.T) { testTwUp(t, []string{"--separate-host"}) })
}

func testTwUp(t *testing.T, extra []string) {
	// tw up starts workerd; a clean runner has no installed tw to find it in.
	wd, err := nettest.WorkerdBinary()
	if err != nil {
		t.Fatalf("workerd (run make deps): %v", err)
	}
	t.Setenv("TW_WORKERD", wd)
	hs := startServer(t)
	ctx := ctxT(t, 10*time.Minute)
	e := &cliflow.Env{T: t, BinDir: cliflow.BuildBinaries(t), StateDir: shortDir(t, "tw-hs-state-"), WorkDir: t.TempDir()}
	t.Cleanup(func() {
		if t.Failed() {
			for _, c := range []string{"mgmt", "hostd", "supervisor"} {
				b, _ := os.ReadFile(filepath.Join(e.StateDir, "logs", c+".log"))
				if len(b) > 6000 {
					b = b[len(b)-6000:]
				}
				t.Logf("%s.log (tail):\n%s", c, b)
			}
		}
		e.Tw("down", "--purge")
	})

	// The profile's refusals.
	if _, se, _, err := e.Tw("up", "--yes", "--control=headscale", "--tailnet", hs.Domain, "--api-key-file", hs.KeyFile); err == nil || !strings.Contains(se, "--control-url") {
		t.Fatalf("tw up --control=headscale without --control-url: %v\n%s", err, se)
	}
	if _, se, _, err := e.Tw("up", "--yes", "--control=headscale", "--control-url", hs.URL, "--oauth-client-file", "/nonexistent/oauth"); err == nil || !strings.Contains(se, "profile_unsupported") {
		t.Fatalf("tw up --control=headscale --oauth-client-file: %v\n%s", err, se)
	}

	// Without --apply-policy, an undefined tag:tw-mgmt stops tw up before
	// anything is minted, with a snippet Headscale accepts.
	if _, se, _, err := e.Tw("up", "--yes", "--json", "--control=headscale", "--control-url", hs.URL, "--tailnet", hs.Domain, "--api-key-file", hs.KeyFile); err == nil ||
		!strings.Contains(se, "policy_missing_tag_owners") || !strings.Contains(se, "tag:tw-mgmt") || strings.Contains(se, "autogroup:admin") {
		t.Fatalf("tw up --control=headscale without the policy: %v\n%s", err, se)
	}
	// The same without --control=headscale: --api-key-file with a
	// non-default --control-url selects the profile (#539).
	if _, se, _, err := e.Tw("up", "--yes", "--json", "--control-url", hs.URL, "--tailnet", hs.Domain, "--api-key-file", hs.KeyFile); err == nil ||
		!strings.Contains(se, "policy_missing_tag_owners") || strings.Contains(se, "mint_mode_removed") {
		t.Fatalf("tw up --control-url <headscale> --api-key-file (profile inferred) without the policy: %v\n%s", err, se)
	}
	if devs, _ := hs.Client.ListDevices(ctx); len(devs) != 0 {
		t.Fatalf("nodes joined before the policy was in place: %+v", devs)
	}
	e.Tw("down", "--purge")
	os.RemoveAll(e.StateDir)
	os.MkdirAll(e.StateDir, 0o700)

	var up struct {
		TenantID   string             `json:"tenantID"`
		MintMode   string             `json:"mintMode"`
		MgmtNodeID string             `json:"mgmtNodeID"`
		PolicyOK   bool               `json:"policyOK"`
		Applied    bool               `json:"policyApplied"`
		Timings    map[string]float64 `json:"timingsMs"`
	}
	d := e.MustJSON(&up, append([]string{"up", "--yes", "--control=headscale", "--control-url", hs.URL, "--tailnet", hs.Domain,
		"--api-key-file", hs.KeyFile, "--apply-policy", "--ephemeral", "--hostname-prefix", "tw-e2e-",
		"--mgmt-hostname", "tw-e2e-mgmt", "--host-id", "e2e", "--allow-sibling-egress"}, extra...)...)
	if !up.PolicyOK || !up.Applied || up.MintMode != "headscale" {
		t.Fatalf("tw up: %+v", up)
	}
	t.Logf("PASS tw up --control=headscale in %v (mgmt node %s, policy applied)", d.Round(time.Millisecond), up.MgmtNodeID)
	// The applied snippet is the profile's (no autogroup:admin).
	pol, _, err := hs.Client.GetPolicyHuJSON(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if miss, _ := policy.Missing(pol, policy.Headscale(policy.InfraSnippet(7474))); !miss.Empty() || strings.Contains(string(pol), "autogroup:admin") {
		t.Fatalf("applied policy:\n%s", pol)
	}
	// The workload's and the test client's tags and grants (what the
	// operator adds for their app).
	if _, err := policy.ApplyWith(ctx, hs.Client, workloadSnippet(), policy.Options{Headscale: true, AllowTags: []string{TestTag}, AllowSrcs: []string{"autogroup:member"}}); err != nil {
		t.Fatal(err)
	}

	app := filepath.Join(e.WorkDir, "hello")
	e.MustJSON(nil, "init", app, "--template", "hello")
	editFile(t, filepath.Join(app, "tailworks.jsonc"), func(s string) string {
		return strings.Replace(s, `"connects": ["tag:db"]`, `"connects": []`, 1)
	})
	var dep struct {
		Endpoint struct {
			ObjectID string `json:"objectID"`
			MagicDNS string `json:"magicDNS"`
		} `json:"endpoint"`
		Preflight struct {
			OK bool `json:"ok"`
		} `json:"preflight"`
	}
	d = e.MustJSON(&dep, "deploy", app)
	if !dep.Preflight.OK || !strings.HasSuffix(dep.Endpoint.MagicDNS, "."+hs.Domain) {
		t.Fatalf("deploy: %+v", dep)
	}
	t.Logf("PASS deploy in %v: %s (preflight read the Headscale policy)", d.Round(time.Millisecond), dep.Endpoint.MagicDNS)

	client := joinClient(t, ctx, hs, "tw-e2e-client")
	cst, _ := client.LC.StatusWithoutPeers(ctx)
	ip := cliflow.PeerIP(t, client, dep.Endpoint.MagicDNS, 90*time.Second)
	hc := client.HTTPClient(45 * time.Second)
	resp, err := hc.Get("http://" + ip + "/")
	if err != nil {
		t.Fatalf("GET the workload over Headscale: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var body struct {
		Caller struct {
			NodeID string   `json:"nodeId"`
			Tags   []string `json:"tags"`
		} `json:"caller"`
	}
	json.Unmarshal(b, &body)
	if resp.StatusCode != 200 || body.Caller.NodeID != string(cst.Self.ID) || len(body.Caller.Tags) != 1 || body.Caller.Tags[0] != TestTag {
		t.Fatalf("workload answered %d %s; want the verified caller %s %s", resp.StatusCode, b, cst.Self.ID, TestTag)
	}
	t.Logf("PASS call over the Headscale tailnet: caller nodeId %s tags %v", body.Caller.NodeID, body.Caller.Tags)

	// A new app deployed with --apply-policy gets the caller grant
	// (autogroup:member → its tag, #552): Headscale accepts that source.
	callers := filepath.Join(e.WorkDir, "callers")
	e.MustJSON(nil, "init", callers, "--template", "hello")
	editFile(t, filepath.Join(callers, "tailworks.jsonc"), func(s string) string {
		return strings.Replace(s, `"connects": ["tag:db"]`, `"connects": []`, 1)
	})
	e.MustJSON(nil, "deploy", callers, "--apply-policy", "--yes")
	pol, _, err = hs.Client.GetPolicyHuJSON(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cg := policy.Snippet{Grants: []map[string]any{{"src": []string{"autogroup:member"}, "dst": []string{"tag:tw-wl-callers"}, "ip": []string{"tcp:80"}}}}
	if miss, err := policy.Missing(pol, cg); err != nil || !miss.Empty() {
		t.Fatalf("caller grant not written by deploy --apply-policy (missing %+v, %v):\n%s", miss, err, pol)
	}
	t.Log("PASS deploy --apply-policy wrote the autogroup:member caller grant")

	// tailnet.service needs Tailscale Services.
	svc := filepath.Join(e.WorkDir, "svc")
	e.MustJSON(nil, "init", svc, "--template", "hello")
	editFile(t, filepath.Join(svc, "tailworks.jsonc"), func(s string) string {
		s = strings.Replace(s, `"connects": ["tag:db"]`, `"connects": []`, 1)
		return strings.Replace(s, `"tailnet": {`, `"tailnet": { "service": true,`, 1)
	})
	if _, se, _, err := e.Tw("deploy", svc, "--json"); err == nil || !strings.Contains(se, "profile_unsupported") {
		t.Fatalf("deploy with tailnet.service: %v\n%s", err, se)
	}
	t.Log("PASS tailnet.service refused (profile_unsupported)")

	// Kill: Tier 1 at once, Tier 2 deletes the Headscale node.
	var nodeID string
	devs, _ := hs.Client.ListDevices(ctx)
	for _, d := range devs {
		if d.Name == dep.Endpoint.MagicDNS {
			nodeID = d.ID
		}
	}
	if nodeID == "" {
		t.Fatalf("workload node %s not among %+v", dep.Endpoint.MagicDNS, devs)
	}
	st := time.Now()
	e.MustJSON(nil, "kill", dep.Endpoint.MagicDNS, "--reason", "e2e")
	for {
		devs, err := hs.Client.ListDevices(ctx)
		if err == nil && !hasDevice(devs, nodeID, "") {
			break
		}
		if time.Since(st) > 60*time.Second {
			t.Fatalf("killed node %s still in Headscale after 60s", nodeID)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("PASS kill: Headscale node %s deleted %v after the kill (Tier 2)", nodeID, time.Since(st).Round(time.Millisecond))
	e.MustJSON(nil, "down", "--purge")
}

// TestRefusesOldHeadscale: a release without grants (< v0.29) is refused
// before anything is minted, naming the minimum version.
func TestRefusesOldHeadscale(t *testing.T) {
	if os.Getenv("TW_HEADSCALE_URL") != "" {
		t.Skip("external server")
	}
	hs := startServerImage(t, OldImage)
	e := &cliflow.Env{T: t, BinDir: cliflow.BuildBinaries(t), StateDir: shortDir(t, "tw-hs-state-"), WorkDir: t.TempDir()}
	_, se, _, err := e.Tw("up", "--yes", "--json", "--control=headscale", "--control-url", hs.URL, "--tailnet", hs.Domain, "--api-key-file", hs.KeyFile, "--apply-policy")
	if err == nil || !strings.Contains(se, "profile_unsupported") || !strings.Contains(se, "v0.29") {
		t.Fatalf("tw up on %s: %v\n%s", OldImage, err, se)
	}
	if devs, _ := hs.Client.ListDevices(ctxT(t, time.Minute)); len(devs) != 0 {
		t.Fatalf("nodes joined: %+v", devs)
	}
	t.Logf("PASS %s refused: profile_unsupported (needs v0.29+)", OldImage)
}

func editFile(t *testing.T, path string, edit func(string) string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(edit(string(b))), 0o644); err != nil {
		t.Fatal(err)
	}
}

// shortDir is a temp dir with a short path (unix socket length limit).
func shortDir(t *testing.T, prefix string) string {
	d, err := os.MkdirTemp("/tmp", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}
