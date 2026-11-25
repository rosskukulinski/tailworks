//go:build e2eheadscale

// Package headscale_test is E2E-HEADSCALE (ADR-015): the Headscale profile
// against a real, pinned Headscale release (`make e2e-headscale`).
//
//   - TestPolicyProfile: the profile's policy is accepted by Headscale's
//     own parser, and what the profile leaves out (autogroup:admin, funnel,
//     svc:, postures) really is rejected by it.
//   - TestNodeLifecycle: the client's mint → join → list → rename →
//     delete path (the kill Tier 2 and reaper mapping), including the
//     node-side rename the pre-joined node pool relies on (ADR-031).
//   - TestTwUp: `tw up --control=headscale`, a deploy, a call from
//     another tailnet node, kill and `tw down`, all through the CLI.
package headscale_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/netd/nettest"
	"github.com/tailscale/hujson"
	"tailscale.com/ipn"
)

// TestTag tags the suite's test client.
const TestTag = "tag:ttest"

// workloadSnippet is a workload's policy on the Headscale profile: its
// tags owned by tag:tw, the test client's tag, an IP grant, the caller
// grant from autogroup:member and a grant with an app capability (caps
// travel in grants only).
func workloadSnippet() policy.Snippet {
	s := policy.Headscale(policy.InfraSnippet(7474))
	s.TagOwners["tag:tw-wl-hello"] = []string{"tag:tw"}
	s.TagOwners["tag:tw-agent-support"] = []string{"tag:tw"}
	s.TagOwners["tag:tw-agent-support-sub"] = []string{"tag:tw", "tag:tw-agent-support"}
	s.TagOwners[TestTag] = []string{"tag:tw"}
	s.Grants = append(s.Grants,
		map[string]any{"src": []string{TestTag}, "dst": []string{"tag:tw-wl-hello", "tag:tw-agent-support"}, "ip": []string{"tcp:80"}},
		// The caller grant (#552): Headscale accepts autogroup:member as a source.
		map[string]any{"src": []string{"autogroup:member"}, "dst": []string{"tag:tw-wl-hello"}, "ip": []string{"tcp:80"}},
		map[string]any{"src": []string{"tag:tw-agent-support"}, "dst": []string{TestTag}, "ip": []string{"tcp:80"},
			"app": map[string]any{"example.com/cap/mcp": []any{map[string]any{"tools": []string{"lookup_order"}, "limits": map[string]any{"refund": map[string]any{"maxUsd": 50}}}}}},
	)
	return s
}

func TestPolicyProfile(t *testing.T) {
	hs := startServer(t)
	ctx := ctxT(t, 2*time.Minute)
	opts := policy.Options{Headscale: true, AllowTags: []string{TestTag}, AllowSrcs: []string{"autogroup:member"}}

	// Applied to a server with no policy yet (read → merge → check → write).
	want := workloadSnippet()
	ar, err := policy.ApplyWith(ctx, hs.Client, want, opts)
	if err != nil {
		t.Fatalf("apply the Headscale profile snippet: %v", err)
	}
	if !ar.Changed {
		t.Fatal("apply: nothing changed on an empty server")
	}
	cur, etag, err := hs.Client.GetPolicyHuJSON(ctx)
	if err != nil || etag == "" {
		t.Fatalf("read back: %v (etag %q)", err, etag)
	}
	if miss, err := policy.Missing(cur, want); err != nil || !miss.Empty() {
		t.Fatalf("after apply, still missing %+v (%v)\n%s", miss, err, cur)
	}
	// Idempotent; and an operator's own rules and comments are kept.
	edited := strings.Replace(string(cur), "{", "{\n  // operator's comment\n  \"groups\": {\"group:ops\": []},", 1)
	if _, err := hs.Client.SetPolicy(ctx, []byte(edited), etag); err != nil {
		t.Fatalf("operator edit: %v", err)
	}
	if ar, err := policy.ApplyWith(ctx, hs.Client, want, opts); err != nil || ar.Changed {
		t.Fatalf("re-apply: changed=%v err=%v", ar.Changed, err)
	}
	if cur, _, _ := hs.Client.GetPolicyHuJSON(ctx); !strings.Contains(string(cur), "operator's comment") {
		t.Fatalf("operator's comment lost:\n%s", cur)
	}
	// A stale ETag is refused (412), never overwrites.
	if _, err := hs.Client.SetPolicy(ctx, []byte("{}"), etag); err == nil || !strings.Contains(err.Error(), "412") {
		t.Fatalf("SetPolicy with a stale ETag: %v", err)
	}
	// The standard-JSON view mgmt's evaluator reads.
	std, _, _, err := hs.Client.GetPolicy(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hujson.Standardize(std); err != nil || strings.Contains(string(std), "//") {
		t.Fatalf("GetPolicy is not plain JSON: %v", err)
	}
	t.Log("PASS profile policy accepted, merged, idempotent, If-Match honoured")

	// What the Tailscale profile emits and Headscale's parser rejects
	// (RFC §7.2). Each fragment is merged into the applied policy and
	// validated by the real server.
	rejected := map[string]string{
		"autogroup:admin owner":  `"tagOwners": {"tag:tw-x": ["autogroup:admin", "tag:tw"]}`,
		"autogroup:admin source": `"grants": [{"src": ["autogroup:admin"], "dst": ["tag:tw"], "ip": ["tcp:443"]}]`,
		"funnel nodeAttr":        `"nodeAttrs": [{"target": ["tag:tw"], "attr": ["funnel"]}]`,
		"svc: destination":       `"grants": [{"src": ["tag:tw"], "dst": ["svc:web"], "ip": ["tcp:443"]}]`,
		"autoApprovers.services": `"autoApprovers": {"services": {"svc:web": ["tag:tw"]}}`,
		"postures":               `"postures": {"posture:tw-live": ["custom:tw-live == true"]}`,
		"srcPosture":             `"grants": [{"src": ["tag:tw"], "dst": ["tag:tw"], "ip": ["*"], "srcPosture": ["posture:x"]}]`,
		"app in nodeAttrs":       `"nodeAttrs": [{"target": ["tag:tw"], "app": {"example.com/cap/x": [{}]}}]`,
	}
	for name, frag := range rejected {
		body := []byte(`{"tagOwners": {"tag:tw": []}, ` + frag + `}`)
		if strings.HasPrefix(frag, `"tagOwners"`) {
			body = []byte(`{` + frag + `}`)
		}
		if err := hs.Client.ValidatePolicy(ctx, body); err == nil {
			t.Errorf("Headscale accepted %s; the profile could use it (update ADR-015 and policy.Headscale)", name)
		} else {
			t.Logf("PASS rejected by Headscale: %s (%s)", name, firstLine(err.Error()))
		}
	}
	// And the profile check refuses the same things before they're sent.
	for _, s := range []policy.Snippet{policy.PublicSnippet(), policy.InfraSnippet(7474)} {
		if err := policy.Check(s, opts); err == nil {
			t.Errorf("policy.Check(Headscale) accepted %+v", s)
		}
	}
}

func TestNodeLifecycle(t *testing.T) {
	hs := startServer(t)
	ctx := ctxT(t, 4*time.Minute)
	if _, err := policy.ApplyWith(ctx, hs.Client, workloadSnippet(), policy.Options{Headscale: true, AllowTags: []string{TestTag}, AllowSrcs: []string{"autogroup:member"}}); err != nil {
		t.Fatal(err)
	}
	// An unused key is expired by DeleteKey (warm-key revoke).
	spare, err := hs.Client.CreateKey(ctx, tsapi.KeyOptions{Tags: []string{"tag:tw-agent-support"}, Preauthorized: true, ExpirySeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	if err := hs.Client.DeleteKey(ctx, spare.ID); err != nil {
		t.Fatalf("expire unused key: %v", err)
	}
	if _, err := nettest.NewNode(ctx, hs.URL, spare.Secret, "tw-e2e-revoked", t.TempDir()); err == nil {
		t.Fatal("a node joined with an expired (revoked) key")
	}
	t.Log("PASS DeleteKey: an expired key can't join")

	st := time.Now()
	k, err := hs.Client.CreateKey(ctx, tsapi.KeyOptions{Tags: []string{"tag:tw-agent-support"}, Preauthorized: true, ExpirySeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	n, err := nettest.NewNode(ctx, hs.URL, k.Secret, "tw-e2e-obj1", t.TempDir())
	if err != nil {
		t.Fatalf("join with a minted key: %v", err)
	}
	defer n.Close()
	t.Logf("join (mint + up) %v", time.Since(st).Round(time.Millisecond))
	self, err := n.LC.StatusWithoutPeers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := string(self.Self.ID)
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		t.Fatalf("StableNodeID %q is not the numeric Headscale node ID", id)
	}
	if self.Self.Tags == nil || self.Self.Tags.Len() != 1 || self.Self.Tags.At(0) != "tag:tw-agent-support" {
		t.Fatalf("tags %v, want [tag:tw-agent-support]", self.Self.Tags)
	}
	if want := "tw-e2e-obj1." + hs.Domain + "."; self.Self.DNSName != want {
		t.Fatalf("MagicDNS %q, want %q", self.Self.DNSName, want)
	}
	t.Logf("PASS joined: StableNodeID %s, %s, tags %v", id, self.Self.DNSName, self.Self.Tags.AsSlice())
	// The key was single-use.
	if _, err := nettest.NewNode(ctx, hs.URL, k.Secret, "tw-e2e-reuse", t.TempDir()); err == nil {
		t.Fatal("a single-use key joined a second node")
	}

	devs, err := hs.Client.ListDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasDevice(devs, id, "tw-e2e-obj1."+hs.Domain) {
		t.Fatalf("ListDevices %+v lacks node %s", devs, id)
	}

	// ADR-031 (variant B): the pool hands a pre-joined node to an object by
	// a node-side hostname change. Does Headscale follow it?
	st = time.Now()
	if _, err := n.LC.EditPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{Hostname: "tw-e2e-renamed"}, HostnameSet: true}); err != nil {
		t.Fatal(err)
	}
	renamed := false
	for time.Since(st) < 20*time.Second {
		s, err := n.LC.StatusWithoutPeers(ctx)
		if err == nil && s.Self.DNSName == "tw-e2e-renamed."+hs.Domain+"." {
			renamed = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if renamed {
		t.Logf("PASS node-side rename: MagicDNS follows in %v (ADR-031 pool works on Headscale)", time.Since(st).Round(time.Millisecond))
	} else {
		s, _ := n.LC.StatusWithoutPeers(ctx)
		t.Logf("NOTE node-side rename: MagicDNS still %q after 20s; the ADR-031 node pool needs --pool-nodes 0 on Headscale", s.Self.DNSName)
	}

	// Kill Tier 2 / the reaper: delete the node by its StableNodeID.
	st = time.Now()
	if err := hs.Client.DeleteDevice(ctx, id); err != nil {
		t.Fatalf("DeleteDevice(%s): %v", id, err)
	}
	if devs, _ := hs.Client.ListDevices(ctx); hasDevice(devs, id, "") {
		t.Fatalf("node %s still listed after DeleteDevice", id)
	}
	if err := hs.Client.DeleteDevice(ctx, id); err != nil {
		t.Fatalf("DeleteDevice of a deleted node: %v (want nil, like a 404 on Tailscale)", err)
	}
	t.Logf("PASS DeleteDevice %v; repeat is a no-op", time.Since(st).Round(time.Millisecond))
}

func hasDevice(devs []tsapi.Device, id, name string) bool {
	for _, d := range devs {
		if d.ID == id && (name == "" || d.Name == name) {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// joinClient joins a tag:ttest test client to the server.
func joinClient(t *testing.T, ctx context.Context, hs *server, name string) *nettest.Node {
	t.Helper()
	k, err := hs.Client.CreateKey(ctx, tsapi.KeyOptions{Tags: []string{TestTag}, Preauthorized: true, Ephemeral: true, ExpirySeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	n, err := nettest.NewNode(ctx, hs.URL, k.Secret, name, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}
