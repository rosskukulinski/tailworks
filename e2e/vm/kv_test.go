//go:build e2evm && (linux || darwin)

package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestE2EVMKV: Workers KV (ADR-037) on the microVM tier. The
// namespace's Store object runs in the guest's workerd, on the guest's
// encrypted DO volume, and reaches hostd's store API over the guest NIC;
// the binding behaves as on the process tier (worker and facet), and the
// data survives a hostd restart (a new VM).
func TestE2EVMKV(t *testing.T) {
	kernel, rootfs := os.Getenv("TW_VM_KERNEL"), os.Getenv("TW_VM_ROOTFS")
	if kernel == "" || rootfs == "" {
		t.Skip("TW_VM_KERNEL and TW_VM_ROOTFS must be set (make e2e-vm / e2e-vz)")
	}
	be, _, err := newBackend(kernel, rootfs, 2, 0, false)
	if err != nil {
		t.Fatalf("microVM backend unavailable: %v", err)
	}
	t.Cleanup(func() { be.Close() })
	ctl, err := nettest.StartControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ctl.Close)
	e := hostdtest.Start(t, hostdtest.Options{ControlURL: ctl.URL, Fake: fakeStatic(), Ephemeral: true, KillTombstone: 5 * time.Second,
		VM: be, Tenant: mgmtapi.Tenant{Tailnet: "ttest.ts.net", ControlURL: ctl.URL, MintMode: "static", Tier: bundle.TierMicroVM}})
	ctx := context.Background()
	client, err := nettest.NewNode(ctx, ctl.URL, nettest.DummyKey, "ttest-client", filepath.Join(e.Dir, "client"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	hc := client.HTTPClient(60 * time.Second)

	var ip string
	start := func(dep string) {
		t.Helper()
		wl := e.WaitActive("kv", dep, 120*time.Second)
		for _, w := range e.Host.Status().Workloads {
			if w.Workload == "kv" && w.Tier != "microvm" {
				t.Fatalf("kv runs on tier %q, want microvm", w.Tier)
			}
		}
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := client.WaitPeers(wctx, wl.IP); err != nil {
			t.Fatal(err)
		}
		ip = wl.IP
	}
	op := func(path, binding, name string, args ...any) (any, string) {
		t.Helper()
		if args == nil {
			args = []any{}
		}
		b, _ := json.Marshal(map[string]any{"binding": binding, "op": name, "args": args})
		resp, err := hc.Post("http://"+ip+path, "application/json", strings.NewReader(string(b)))
		if err != nil {
			t.Fatalf("%s %s: %v", path, name, err)
		}
		defer resp.Body.Close()
		var m struct {
			R     any    `json:"r"`
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil || (resp.StatusCode != http.StatusOK && m.Error == "") {
			t.Fatalf("%s %s: %d %v", path, name, resp.StatusCode, err)
		}
		return m.R, m.Error
	}
	must := func(path, binding, name string, args ...any) string {
		t.Helper()
		r, e := op(path, binding, name, args...)
		if e != "" {
			t.Fatalf("%s %s.%s%v: %s", path, binding, name, args, e)
		}
		b, _ := json.Marshal(r)
		return string(b)
	}

	t0 := time.Now()
	dep := e.Deploy(hostdtest.Fixture("kv"), "kv", nil)
	start(dep)
	t.Logf("kv active in a microVM after %v", time.Since(t0).Round(time.Millisecond))
	const W, F = "/op", "/do/op"
	t1 := time.Now()
	must(W, "KV", "put", "a", "1", map[string]any{"metadata": map[string]any{"v": 1}, "expirationTtl": 3600})
	t.Logf("first put (Store object start + journal append from the guest): %v", time.Since(t1).Round(time.Millisecond))
	must(F, "KV", "put", "b", "2")
	must(W, "KV", "fill", "p/", 12)
	check := func(when string) {
		t.Helper()
		if got := must(F, "KV", "get", "a"); got != `"1"` {
			t.Fatalf("%s: facet get a = %s", when, got)
		}
		if got := must(W, "KV", "getWithMetadata", "b"); got != `{"cacheStatus":null,"metadata":null,"value":"2"}` {
			t.Fatalf("%s: getWithMetadata b = %s", when, got)
		}
		if got := must(W, "KV", "listAll", map[string]any{"prefix": "p/", "limit": 5}); !strings.Contains(got, `"pages":[5,5,2]`) {
			t.Fatalf("%s: list pages %s", when, got)
		}
		if got := must(W, "KV", "get", []string{"a", "b", "nope"}); got != `{"map":{"a":"1","b":"2","nope":null}}` {
			t.Fatalf("%s: bulk get %s", when, got)
		}
	}
	check("in the microVM")
	if _, e := op(W, "KV", "put", "t", "v", map[string]any{"expirationTtl": 5}); !strings.Contains(e, "at least 60") {
		t.Fatalf("short TTL: %q", e)
	}
	t2 := time.Now()
	for i := 0; i < 20; i++ {
		must(W, "KV", "get", "a")
	}
	t.Logf("20 reads: %v", time.Since(t2).Round(time.Millisecond))

	// hostd restarts: a new VM, the same encrypted volume, and the journal.
	e.Restart()
	start(dep)
	check("after a hostd restart")
	var p mgmtapi.StoreJournalPage
	if err := e.Mgmt.Do(ctx, "GET", fmt.Sprintf("/v1/tenants/%s/workloads/kv/stores/kv/main/journal", e.TenantID), nil, &p); err != nil || p.Head != 14 {
		t.Fatalf("journal: head %d %v (want 14 writes)", p.Head, err)
	}
}
