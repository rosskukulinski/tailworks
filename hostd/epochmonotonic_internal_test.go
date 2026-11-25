package hostd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// A deploy of an older assignment that was held up (its bundle fetch hung
// on a slow mgmt replica) must not lower the workload's epoch once a newer
// assignment has raised it (#584, found by e2e/dataplane's deploys loop:
// one copy answered at epoch 147, then 145). The epoch is what fencing
// tokens are built on (docs/locks.md), so it may only go up.
func TestDeployNeverLowersEpoch(t *testing.T) {
	t.Parallel()
	raw, err := bundle.Pack(map[string][]byte{
		"tw-bundle.json":  []byte(`{"main":"dist/index.js"}`),
		"dist/index.js":   []byte(`export default { fetch() { return new Response("x") } }`),
		"wrangler.jsonc":  []byte(`{"name":"dp","main":"dist/index.js","compatibility_date":"2026-04-01"}`),
		"tailworks.jsonc": []byte(`{"name":"dp","tailnet":{"tags":["tag:t"]}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if files, err := bundle.Unpack(raw); err != nil {
		t.Fatal(err)
	} else if _, err := bundle.Parse(files); err != nil {
		t.Fatal(err)
	}
	var (
		wl      *workload
		mu      sync.Mutex
		atGrant []int64 // the workload's epoch when the deploy asks for its secrets
	)
	mg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/bundle"):
			w.Header().Set("content-type", "application/x-tar")
			w.Write(raw)
		default: // the secret grant and anything after: this host lost the lease at 145
			if strings.HasSuffix(r.URL.Path, "/secret-grants") {
				mu.Lock()
				atGrant = append(atGrant, wl.epochNow())
				mu.Unlock()
			}
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(409)
			json.NewEncoder(w).Encode(map[string]string{"code": "lease_lost", "message": "test mgmt"})
		}
	}))
	t.Cleanup(mg.Close)
	h, _ := ctrHost(t, func(c *Config) { c.Mgmt = &mgmtapi.Client{Base: mg.URL} })
	w := stallWorkload(h, "dp", true)
	wl = w
	h.mu.Lock()
	h.tenants["tn"] = w.tenant
	h.mu.Unlock()

	// The sync loop has taken the newer assignment (epoch 147)...
	w.wantDeploy(mgmtapi.Assignment{DeploymentID: "dp_147", TenantID: "tn", Workload: "dp", Epoch: 147})
	// ...and the held-up deploy of epoch 145 finishes now.
	h.deploy(w, mgmtapi.Assignment{DeploymentID: "dp_145", TenantID: "tn", Workload: "dp", Epoch: 145,
		BundleURL: "/v1/deployments/dp_145/bundle"})
	mu.Lock()
	defer mu.Unlock()
	if len(atGrant) == 0 {
		t.Fatal("the held-up deploy never asked for its secrets: the test no longer reaches the epoch update")
	}
	for _, e := range append(atGrant, w.epochNow()) {
		if e != 147 {
			t.Fatalf("epoch during and after a held-up deploy of epoch 145: %v then %d, want 147 (it must never go down)", atGrant, w.epochNow())
		}
	}
}
