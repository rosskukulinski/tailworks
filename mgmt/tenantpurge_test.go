package mgmt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
)

// failingCustody fails every Delete while fail is set (a crash part way
// through a tenant delete).
type failingCustody struct {
	Custody
	fail atomic.Bool
}

func (c *failingCustody) Delete(ctx context.Context, name string) error {
	if c.fail.Load() {
		return errors.New("custody unavailable")
	}
	return c.Custody.Delete(ctx, name)
}

// tenantWorld is a tenant with every kind of data: a deployed workload
// with KV, D1 and R2 journals, queue messages, secrets (also of a
// workload never deployed), a volume key, notification and telemetry
// settings, and objects.
type tenantWorld struct {
	e     *testEnv
	cust  *failingCustody
	files map[string]bool // custody files before any tenant existed
}

func newTenantWorld(t *testing.T) *tenantWorld {
	allowLoopbackNotify(t)
	w := &tenantWorld{}
	w.e = newTestEnvWith(t, func(c *Config) {
		w.cust = &failingCustody{Custody: c.Custody}
		c.Custody = w.cust
	})
	w.e.asHost("h1").ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	w.files = w.custodyFiles(t)
	return w
}

func (w *tenantWorld) custodyFiles(t *testing.T) map[string]bool {
	t.Helper()
	des, err := os.ReadDir(filepath.Join(filepath.Dir(filepath.Dir(w.e.sock)), "custody", "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, d := range des {
		out[d.Name()] = true
	}
	return out
}

// populate creates the tenant's data and returns its ID.
func (w *tenantWorld) populate(t *testing.T, tailnet string) string {
	t.Helper()
	ctx := context.Background()
	e := w.e
	c, h1 := e.c, e.asHost("h1")
	tn := v1Tenant(t, e, tailnet, nil)
	bundle := makeBundle(t, map[string]string{"tailworks.jsonc": testTwJSONC, "wrangler.jsonc": queueWrangler, "dist/index.js": "export default {}"})
	var dep DeployResponse
	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn, "workload": "support", "bundle": bundle}, &dep)
	for _, kind := range []string{"kv", "d1", "r2"} {
		p := fmt.Sprintf("/v1/tenants/%s/workloads/support/stores/%s/data/journal?epoch=%d", tn, kind, dep.Epoch)
		h1.ok("POST", p, map[string]any{"after": 0, "entries": []any{entry("a", storeValue), entry("", "log")}}, nil)
	}
	h1.ok("POST", fmt.Sprintf("/v1/tenants/%s/queues/jobs/messages?workload=support&epoch=%d", tn, dep.Epoch),
		map[string]any{"messages": []any{qmsg("job-1", "text"), qmsg("job-2", "text")}}, nil)
	for _, wl := range []string{"support", "never-deployed"} {
		c.ok("PUT", "/v1/tenants/"+tn+"/workloads/"+wl+"/secrets/API_KEY", map[string]string{"value": secretValue}, nil)
	}
	c.ok("PUT", "/v1/tenants/"+tn+"/notifications", map[string]any{"webhook": map[string]any{"url": "http://127.0.0.1:9/hook/" + hookSecret}}, nil)
	c.ok("PUT", "/v1/tenants/"+tn+"/telemetry", map[string]any{"otlp": map[string]any{"endpoint": "http://otel:4318", "headers": map[string]string{"authorization": "Bearer t"}}}, nil)
	if _, err := e.m.VolumeKey(ctx, tn, "support", "h1", dep.Epoch); err != nil {
		t.Fatal(err)
	}
	var root CreateObjectResponse
	h1.ok("POST", "/v1/objects", map[string]any{"tenantID": tn, "namespace": "SupportAgent", "objectName": "alice"}, &root)
	var cred Credential
	h1.ok("POST", fmt.Sprintf("/v1/objects/%s/credentials?epoch=%d", root.ObjectID, dep.Epoch), nil, &cred)
	return tn
}

// tenantRows counts the rows of tenant id in every table that has a
// tenant_id column (except the append-only audit chain), plus the journal
// and message tables keyed by incarnation, leases and secret choices.
func tenantRows(t *testing.T, st *SQLStore, id string) map[string]int {
	t.Helper()
	ctx := context.Background()
	q := "SELECT m.name FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type='table' AND p.name='tenant_id'"
	if st.d == dialectPostgres {
		q = "SELECT table_name FROM information_schema.columns WHERE table_schema='public' AND column_name='tenant_id'"
	}
	tables, err := st.column(ctx, st.db, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) < 10 {
		t.Fatalf("tables with tenant_id: %v", tables)
	}
	out := map[string]int{}
	count := func(name, q string, args ...any) {
		var n int
		if err := st.db.QueryRowContext(ctx, st.q(q), args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if n > 0 {
			out[name] = n
		}
	}
	for _, tb := range tables {
		if tb == "audit" || tb == "tenant_purges" { // the record; the hosts' to-do (checked apart)
			continue
		}
		count(tb, "SELECT COUNT(*) FROM "+tb+" WHERE tenant_id=?", id)
	}
	count("tenants", "SELECT COUNT(*) FROM tenants WHERE id=?", id)
	count("store_entries", "SELECT COUNT(*) FROM store_entries WHERE incarnation IN (SELECT incarnation FROM stores WHERE tenant_id=?)", id)
	count("queue_messages", "SELECT COUNT(*) FROM queue_messages WHERE incarnation IN (SELECT incarnation FROM queues WHERE tenant_id=?)", id)
	count("leases", "SELECT COUNT(*) FROM leases WHERE substr(name, 1, ?) = ?", len(id)+1, id+"/")
	count("secret_choice", "SELECT COUNT(*) FROM secret_choice WHERE name=?", oboChoiceName(id))
	return out
}

// TestDeleteTenantPurgesAPI (issue #107): `tw tenant rm --force` leaves no
// row of the tenant in any table, no custody entry, and every host is told
// to remove its files; another tenant is untouched. A delete that fails
// part way (custody down) leaves the tenant, and running it again finishes.
func TestDeleteTenantPurgesAPI(t *testing.T) {
	ctx := context.Background()
	w := newTenantWorld(t)
	e := w.e
	tn := w.populate(t, "a.ts.net")
	other := w.populate(t, "b.ts.net")

	rows := tenantRows(t, e.store, tn)
	for _, tb := range []string{"tenants", "stores", "store_entries", "queues", "queue_messages", "objects", "deployments", "leases"} {
		if rows[tb] == 0 {
			t.Fatalf("setup: no %s rows: %v", tb, rows)
		}
	}
	otherRows := tenantRows(t, e.store, other)
	var refs tenantCustodyRefs
	refs, err := e.store.TenantCustodyRefs(ctx, tn)
	if err != nil || len(refs.Indexed) < 8 {
		t.Fatalf("custody index: %v %v", refs.Indexed, err)
	}
	names := e.m.tenantCustodyNames(ctx, tn, refs)
	var have []string
	for _, n := range names {
		if _, err := e.m.custody.Get(ctx, n); err == nil {
			have = append(have, n)
		}
	}
	for _, want := range []string{secretsCustodyName(tn, "never-deployed"), secretsCustodyName(tn, "support"), volumeKeyName(tn, "support"),
		notifyCustodyName(tn), telemetryCustodyName(tn), storeKeyCustodyName(tn, refs.StoreIncarnations[0]), queueKeyCustodyName(tn, refs.QueueIncarnations[0])} {
		if !containsName(have, want) {
			t.Fatalf("setup: custody has no %s (have %v)", want, have)
		}
	}

	// Custody fails part way: the delete fails and the tenant stays.
	w.cust.fail.Store(true)
	if st, _ := e.c.do("DELETE", "/v1/tenants/"+tn+"?force=true", nil, nil); st != 500 {
		t.Fatalf("delete with custody down: %d", st)
	}
	w.cust.fail.Store(false)
	if _, err := e.store.GetTenant(ctx, tn); err != nil {
		t.Fatalf("tenant gone after a failed delete: %v", err)
	}
	// Run again: it finishes.
	var res TenantDeleteResult
	e.c.do("DELETE", "/v1/tenants/"+tn+"?force=true", nil, &res)
	if _, err := e.store.GetTenant(ctx, tn); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tenant still there: %v", err)
	}
	if left := tenantRows(t, e.store, tn); len(left) != 0 {
		t.Fatalf("rows left for the deleted tenant: %v", left)
	}
	for _, n := range names {
		if _, err := w.cust.Custody.Get(ctx, n); !errors.Is(err, ErrNotFound) {
			t.Errorf("custody entry left: %s (%v)", n, err)
		}
	}
	// Custody files: only the other tenant's are new.
	otherRefs, _ := e.store.TenantCustodyRefs(ctx, other)
	after := w.custodyFiles(t)
	var extra []string
	for f := range after {
		if !w.files[f] {
			extra = append(extra, f)
		}
	}
	sort.Strings(extra)
	if len(extra) != len(otherRefs.Indexed) {
		t.Fatalf("custody files: %d new, the other tenant indexed %d (%v)", len(extra), len(otherRefs.Indexed), otherRefs.Indexed)
	}
	if got := tenantRows(t, e.store, other); fmt.Sprint(got) != fmt.Sprint(otherRows) {
		t.Fatalf("the other tenant's rows changed: %v → %v", otherRows, got)
	}

	// The audit record counts what went.
	recs, _, err := e.m.QueryAudit(ctx, AuditFilter{TenantID: tn, Kinds: []string{"tenant_delete"}})
	if err != nil || len(recs) != 1 {
		t.Fatalf("tenant_delete audit: %d %v", len(recs), err)
	}
	var detail struct {
		Removed map[string]int64 `json:"removed"`
	}
	json.Unmarshal(recs[0].Detail, &detail)
	for _, k := range []string{"storeEntries", "stores", "queueMessages", "queues", "deployments", "custodyEntries", "hostsToPurge"} {
		if detail.Removed[k] == 0 {
			t.Errorf("audit removed[%s] = 0: %v", k, detail.Removed)
		}
	}
	if detail.Removed["stores"] != 3 || detail.Removed["queueMessages"] != 2 {
		t.Errorf("audit removed: %v", detail.Removed)
	}

	// The host is told to purge the tenant until it reports it done.
	h1 := e.asHost("h1")
	var hb struct {
		PurgeTenants []string `json:"purgeTenants"`
	}
	h1.ok("PUT", "/v1/hosts/h1/status", map[string]any{"liveNodes": 0, "leases": []any{}}, &hb)
	if fmt.Sprint(hb.PurgeTenants) != fmt.Sprint([]string{tn}) {
		t.Fatalf("purgeTenants: %v", hb.PurgeTenants)
	}
	hb.PurgeTenants = nil
	h1.ok("PUT", "/v1/hosts/h1/status", map[string]any{"liveNodes": 0, "leases": []any{}, "purgedTenants": []string{tn}}, &hb)
	if len(hb.PurgeTenants) != 0 {
		t.Fatalf("purgeTenants after the report: %v", hb.PurgeTenants)
	}
	if recs, _, _ := e.m.QueryAudit(ctx, AuditFilter{TenantID: tn, Kinds: []string{"tenant_purge_host"}}); len(recs) != 1 {
		t.Fatalf("tenant_purge_host audit: %d", len(recs))
	}
	if pending, err := e.store.TenantPurges(ctx, "h1"); err != nil || len(pending) != 0 {
		t.Fatalf("tenant_purges left: %v %v", pending, err)
	}

	// Deleting it again: 404.
	e.c.fail("DELETE", "/v1/tenants/"+tn+"?force=true", nil, 404, "not_found")
}

func containsName(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// TestCustodyTenant: custody names index under their tenant.
func TestCustodyTenant(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"tw/tn_a/workloads/w/secrets": "tn_a",
		"tw/tn_a/obo-root":            "tn_a",
		"tw/tn_a":                     "",
		"other/tn_a/x":                "",
	} {
		if got := custodyTenant(name); got != want {
			t.Errorf("custodyTenant(%q) = %q, want %q", name, got, want)
		}
	}
	if strings.Contains(secretsCustodyName("tn_a", "w"), "//") {
		t.Fatal("bad name")
	}
}
