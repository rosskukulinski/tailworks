package mgmt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// newVersionedEnv is a test mgmt that reports version v.
func newVersionedEnv(t *testing.T, v string) *testEnv {
	return newTestEnvWith(t, func(c *Config) {
		c.Version = v
		c.ReleaseDir = t.TempDir()
	})
}

func TestHostVersionSkew(t *testing.T) {
	t.Parallel()
	e := newVersionedEnv(t, "0.3.0")
	reg := func(id, v string) map[string]any {
		return map[string]any{"capacityNodes": 10, "tiers": []string{"process"}, "version": v, "os": "linux",
			"protocols": map[string]int{"push": 1}, "features": []string{"kv"}}
	}
	// N-1 and N register; N-2 and N+1 are refused.
	e.asHost("h1").ok("PUT", "/v1/hosts/h1", reg("h1", "0.2.4"), nil)
	e.asHost("h2").ok("PUT", "/v1/hosts/h2", reg("h2", "0.3.1"), nil)
	if r := e.asHost("h3").fail("PUT", "/v1/hosts/h3", reg("h3", "0.1.0"), 409, "version_skew"); !strings.Contains(r["message"].(string), "behind") {
		t.Fatalf("N-2: %v", r)
	}
	e.asHost("h4").fail("PUT", "/v1/hosts/h4", reg("h4", "0.4.0"), 409, "version_skew")
	// A host that reports no version is 0.1.x: two lines behind 0.3.
	e.asHost("h5").fail("PUT", "/v1/hosts/h5", map[string]any{"capacityNodes": 10, "tiers": []string{"process"}}, 409, "version_skew")

	var h struct {
		Version  string
		OS       string
		Features []string
	}
	e.c.ok("GET", "/v1/hosts/h1", nil, &h)
	if h.Version != "0.2.4" || h.OS != "linux" || len(h.Features) != 1 {
		t.Fatalf("host record: %+v", h)
	}
	var st struct {
		Version  string
		Versions VersionStatus
	}
	e.c.ok("GET", "/v1/status", nil, &st)
	if st.Version != "0.3.0" || st.Versions.Hosts["0.2.4"] != 1 || st.Versions.Hosts["0.3.1"] != 1 || st.Versions.Schema == nil {
		t.Fatalf("status versions: %+v", st)
	}
	_, _, hdr := e.c.doRaw("GET", "/v1/status", nil, nil)
	if hdr.Get(VersionHeader) != "0.3.0" {
		t.Fatalf("Tw-Version header %q", hdr.Get(VersionHeader))
	}
}

func TestFeatureGatedPlacementAndCordon(t *testing.T) {
	t.Parallel()
	e := newVersionedEnv(t, "0.2.0")
	var tn struct{ TenantID string }
	e.c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	// h-old is a 0.1 host (no features); h-new offers kv.
	e.asHost("h-old").ok("PUT", "/v1/hosts/h-old", map[string]any{"capacityNodes": 1000, "tiers": []string{"process"}}, nil)
	kvBundle := makeBundle(t, map[string]string{
		"tailworks.jsonc": `{"tailnet": {"tags": ["tag:w"]}}`,
		"wrangler.jsonc":  `{"name": "w", "main": "dist/index.js", "kv_namespaces": [{"binding": "CACHE", "id": "cache"}]}`,
		"dist/index.js":   "export default {}",
	})
	plain := makeBundle(t, map[string]string{"tailworks.jsonc": `{"tailnet": {"tags": ["tag:w"]}}`, "dist/index.js": "export default {}"})
	r := e.c.fail("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "kvw", "bundle": kvBundle}, 409, "feature_unavailable")
	if !strings.Contains(r["message"].(string), "lacks feature kv") {
		t.Fatalf("feature refusal: %v", r)
	}
	e.asHost("h-new").ok("PUT", "/v1/hosts/h-new", map[string]any{"capacityNodes": 10, "tiers": []string{"process"},
		"version": "0.2.0", "features": []string{"d1", "kv", "r2", "telemetry"}}, nil)
	var d struct{ Host string }
	e.c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "kvw", "bundle": kvBundle}, &d)
	if d.Host != "h-new" {
		t.Fatalf("kv workload on %s", d.Host)
	}
	// Cordon h-old (the roomier host): a new workload goes to h-new.
	e.c.ok("PUT", "/v1/hosts/h-old/cordon", nil, nil)
	e.c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.TenantID, "workload": "plain", "bundle": plain}, &d)
	if d.Host != "h-new" {
		t.Fatalf("placed on cordoned host: %s", d.Host)
	}
	var h struct{ CordonedAt int64 }
	e.c.ok("DELETE", "/v1/hosts/h-old/cordon", nil, &h)
	if h.CordonedAt != 0 {
		t.Fatal("still cordoned")
	}
	// Drain h-new: the stateless workloads move to h-old, except the kv one,
	// which has nowhere to go.
	var dr DrainResult
	e.c.ok("POST", "/v1/hosts/h-new/drain", map[string]any{"dryRun": true}, &dr)
	if dr.Cordoned || len(dr.Moved) != 1 || dr.Moved[0].Workload != "plain" || dr.Moved[0].To != "h-old" || len(dr.Refused) != 1 || dr.Refused[0].Code != "feature_unavailable" {
		t.Fatalf("dry drain: %+v", dr)
	}
	e.c.ok("POST", "/v1/hosts/h-new/drain", map[string]any{}, &dr)
	if !dr.Cordoned || len(dr.Moved) != 1 || dr.Moved[0].To != "h-old" {
		t.Fatalf("drain: %+v", dr)
	}
	var hn struct{ CordonedAt int64 }
	e.c.ok("GET", "/v1/hosts/h-new", nil, &hn)
	if hn.CordonedAt == 0 {
		t.Fatal("drained host not cordoned")
	}
}

func TestUpgradeRun(t *testing.T) {
	t.Parallel()
	// The target must not be in the built-in catalog (which ends at this
	// tree's VERSION), so it is the next minor; the hosts run a patch of the
	// current minor, inside the skew window.
	to, old := nextMinorAndPatch(t)
	e := newVersionedEnv(t, to)
	reg := func(id, v string) {
		e.asHost(id).ok("PUT", "/v1/hosts/"+id, map[string]any{"capacityNodes": 10, "tiers": []string{"process"}, "version": v, "os": "linux"}, nil)
	}
	reg("h1", old)
	reg("h2", old)
	// The target isn't in the catalog yet (the built-in entries end at this
	// tree's releases).
	e.c.fail("POST", "/v1/upgrades", map[string]any{"to": to, "dryRun": true}, 400, "bad_request")
	var cat struct {
		Current string
		Items   []ReleaseEntry
	}
	e.c.ok("POST", "/v1/releases", map[string]any{"entries": []map[string]any{{"version": to, "date": "2027-01-01", "workerd": "1.20261201.1",
		"notes": []map[string]any{{"kind": "breaking", "source": "x.md", "text": "tw logs prints records"}}}}}, &cat)
	if cat.Current != to || len(cat.Items) < 2 {
		t.Fatalf("catalog: %+v", cat)
	}
	var plan UpgradePlan
	e.c.ok("POST", "/v1/upgrades", map[string]any{"to": to, "dryRun": true, "canary": "h2", "soakSeconds": 0}, &plan)
	if len(plan.Blockers) != 0 || len(plan.Breaking) != 1 {
		t.Fatalf("plan: %+v", plan)
	}
	var kinds []string
	for _, s := range plan.Steps {
		kinds = append(kinds, s.Kind+":"+s.Host)
	}
	if strings.Join(kinds, " ") != "backup: mgmt: contract: host:h2 host:h1" {
		t.Fatalf("steps: %v", kinds)
	}
	// Downgrades and unknown canaries are blockers.
	e.c.ok("POST", "/v1/upgrades", map[string]any{"to": "0.1.0", "dryRun": true}, &plan)
	if len(plan.Blockers) == 0 {
		t.Fatal("downgrade not blocked")
	}

	var run UpgradeRun
	soak := 0
	e.c.okStatus("POST", "/v1/upgrades", map[string]any{"to": to, "canary": "h2", "soakSeconds": soak, "settleSeconds": 60}, &run)
	e.c.fail("POST", "/v1/upgrades", map[string]any{"to": to}, 409, "exists")
	ctx := context.Background()
	drive := func(until func(UpgradeRun) bool) UpgradeRun {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			e.m.driveUpgrades(ctx)
			r, _, err := e.m.GetUpgrade(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if until(r) {
				return r
			}
			time.Sleep(20 * time.Millisecond)
		}
		r, _, _ := e.m.GetUpgrade(ctx, run.ID)
		t.Fatalf("run stuck: %+v", r)
		return r
	}
	// The hosts can't upgrade themselves: the canary's step waits for the
	// operator, cordoned.
	r := drive(func(r UpgradeRun) bool {
		return r.Step < len(r.Steps) && r.Steps[r.Step].Host == "h2" && r.Steps[r.Step].Phase == "wait"
	})
	if !strings.Contains(r.Steps[r.Step].NeedsOperator, "install tw "+to+" on host h2") {
		t.Fatalf("operator step: %+v", r.Steps[r.Step])
	}
	var h struct{ CordonedAt int64 }
	e.c.ok("GET", "/v1/hosts/h2", nil, &h)
	if h.CordonedAt == 0 {
		t.Fatal("canary not cordoned during its upgrade")
	}
	// Pause and resume.
	e.c.ok("POST", "/v1/upgrades/"+run.ID+"/actions", map[string]any{"action": "pause"}, &r)
	if r.State != "paused" {
		t.Fatalf("pause: %s", r.State)
	}
	e.c.fail("POST", "/v1/upgrades/"+run.ID+"/actions", map[string]any{"action": "pause"}, 409, "upgrade_state")
	e.c.ok("POST", "/v1/upgrades/"+run.ID+"/actions", map[string]any{"action": "resume"}, &r)
	// The operator upgrades h2: it re-registers at 0.2.0, settles, is
	// uncordoned; then h1.
	reg("h2", to)
	drive(func(r UpgradeRun) bool {
		return r.Step < len(r.Steps) && r.Steps[r.Step].Host == "h1" && r.Steps[r.Step].Phase == "wait"
	})
	h.CordonedAt = 0
	e.c.ok("GET", "/v1/hosts/h2", nil, &h)
	if h.CordonedAt != 0 {
		t.Fatal("canary still cordoned")
	}
	reg("h1", to)
	r = drive(func(r UpgradeRun) bool { return r.State == "done" })
	for _, s := range r.Steps {
		if s.Status != "done" && s.Status != "skipped" {
			t.Fatalf("step %+v", s)
		}
	}
	var list struct{ Items []UpgradeRun }
	e.c.ok("GET", "/v1/upgrades", nil, &list)
	if len(list.Items) != 1 || list.Items[0].State != "done" {
		t.Fatalf("list: %+v", list)
	}
	// Everything is at the target now: a new plan has no host work.
	e.c.ok("POST", "/v1/upgrades", map[string]any{"to": to, "dryRun": true}, &plan)
	for _, s := range plan.Steps {
		if s.Kind == "host" && s.Status != "skipped" {
			t.Fatalf("host step after upgrade: %+v", s)
		}
	}
}

func TestReleaseArtifacts(t *testing.T) {
	t.Parallel()
	e := newVersionedEnv(t, "0.2.0")
	name := "tailworks-0.2.0-linux-amd64.tar.gz"
	body := []byte("not really a tarball")
	sum := sha256.Sum256(body)
	put := func(n string, b []byte) (int, string) {
		req, _ := http.NewRequest("PUT", "http://tw-mgmt/v1/releases/0.2.0/artifacts/"+n, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set(HostTokenHeader, e.token)
		res, err := e.c.hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(out)
	}
	// The tarball needs its SHA256SUMS first, and must match it.
	if code, out := put(name, body); code != 400 || !strings.Contains(out, "SHA256SUMS") {
		t.Fatalf("without sums: %d %s", code, out)
	}
	if code, out := put("SHA256SUMS", []byte(hex.EncodeToString(sum[:])+"  "+name+"\n")); code != 201 {
		t.Fatalf("sums: %d %s", code, out)
	}
	if code, out := put(name, []byte("tampered")); code != 400 || !strings.Contains(out, "does not match") {
		t.Fatalf("tampered: %d %s", code, out)
	}
	if code, out := put(name, body); code != 201 {
		t.Fatalf("upload: %d %s", code, out)
	}
	// A host downloads it.
	req, _ := http.NewRequest("GET", "http://tw-mgmt/v1/releases/0.2.0/artifacts/"+name, nil)
	req.Header.Set(HostTokenHeader, e.token)
	req.Header.Set("Tw-Host-ID", "h1")
	e.asHost("h1").ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 10, "tiers": []string{"process"}, "version": "0.2.0"}, nil)
	res, err := e.c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !bytes.Equal(got, body) {
		t.Fatalf("download: %d %q", res.StatusCode, got)
	}
	if s := e.m.artifactSum("0.2.0", name); s != hex.EncodeToString(sum[:]) {
		t.Fatalf("artifactSum %q", s)
	}
}

// Workflow instances live in the workload's DO storage on its host: a
// Workflows app is stateful (fenced, and a drain doesn't move it).
func TestBundleStatefulWorkflows(t *testing.T) {
	t.Parallel()
	b := makeBundle(t, map[string]string{
		"tailworks.jsonc": `{"tailnet": {"tags": ["tag:w"]}}`,
		"wrangler.jsonc":  `{"name": "w", "main": "dist/index.js", "workflows": [{"name": "refunds", "binding": "R", "class_name": "Refund"}]}`,
		"dist/index.js":   "export default {}",
	})
	if st, why := BundleStateful(b); !st || !strings.Contains(why, "workflow refunds") {
		t.Fatalf("stateful %v %q", st, why)
	}
}

// nextMinorAndPatch returns the minor release after this tree's VERSION
// (x.(y+1).0) and a patch of the current minor (x.y.5).
func nextMinorAndPatch(t *testing.T) (next, patch string) {
	t.Helper()
	b, err := os.ReadFile("../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	var maj, min, pat int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(b)), "%d.%d.%d", &maj, &min, &pat); err != nil {
		t.Fatalf("VERSION: %v", err)
	}
	return fmt.Sprintf("%d.%d.0", maj, min+1), fmt.Sprintf("%d.%d.5", maj, min)
}
