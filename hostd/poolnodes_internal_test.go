package hostd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/netd"
)

// Tenant code reaches objects by (class, name); it must never be able to
// create or act as a pool node, which a later object is handed.
func TestEnsureRefusesReservedClasses(t *testing.T) {
	t.Parallel()
	h := &Host{}
	for _, cls := range []string{PoolClass, "_workload"} {
		_, err := h.ensure(context.Background(), &workload{tenantID: "tn", name: "w"}, cls, "SupportAgent/x", nil, nil, "")
		if e, ok := err.(*apiError); !ok || e.Code != "bad_request" {
			t.Fatalf("ensure(class %q) = %v, want bad_request", cls, err)
		}
	}
}

// A pool node serves nothing: ingress to it is refused before workerd.
func TestIngressRefusesPoolNodes(t *testing.T) {
	t.Parallel()
	p := &object{ID: "pool-abc", NetdID: "pool-abc", TenantID: "tn", Workload: "w", Class: PoolClass, Name: "SupportAgent/x", Status: statusReady, live: true}
	h := &Host{objects: map[string]*object{p.ID: p}, byNetd: map[string]*object{p.NetdID: p},
		workloads: map[string]*workload{"tn/w": {tenantID: "tn", name: "w"}}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://pool/", nil)
	req.Header.Set(netd.HeaderObject, p.NetdID)
	req.Header.Set(netd.HeaderCF, `{}`)
	h.ingressHandler(&tenant{id: "tn"}).ServeHTTP(rec, req)
	if rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "unknown_object") {
		t.Fatalf("ingress to a pool node: %d %s", rec.Code, rec.Body)
	}
}

// Idle pool nodes are retired only when their class went cold here AND
// they are old enough (a restart forgets demand; it must not drop a warm
// pool), never while reserved for an assignment in flight.
func TestIdlePoolSelection(t *testing.T) {
	t.Parallel()
	now := time.Now()
	mk := func(id, class string, age time.Duration, reserved bool) *object {
		return &object{ID: id, TenantID: "tn", Workload: "w", Class: PoolClass, Name: class + "/" + id, Status: statusReady,
			live: true, reserved: reserved, Created: now.Add(-age)}
	}
	objs := []*object{
		mk("old-cold", "Cold", 2*time.Hour, false),
		mk("young-cold", "Cold", 10*time.Minute, false),
		mk("old-hot", "Hot", 2*time.Hour, false),
		mk("old-cold-reserved", "Cold", 2*time.Hour, true),
		mk("old-stale-demand", "Stale", 2*time.Hour, false),
		{ID: "not-a-pool", TenantID: "tn", Workload: "w", Class: "Cold", Name: "x", Status: statusReady, Created: now.Add(-2 * time.Hour)},
	}
	h := &Host{objects: map[string]*object{}, poolDemand: map[string]time.Time{
		poolKey("tn", "w", "Hot"):   now.Add(-5 * time.Minute),
		poolKey("tn", "w", "Stale"): now.Add(-2 * time.Hour),
	}}
	for _, o := range objs {
		h.objects[o.ID] = o
	}
	var got []string
	for _, o := range h.idlePoolsLocked(now) {
		got = append(got, o.ID)
	}
	slices.Sort(got)
	if want := []string{"old-cold", "old-stale-demand"}; !slices.Equal(got, want) {
		t.Fatalf("retire %v, want %v", got, want)
	}
}

// Only Durable Object classes use or warm a pool: never the workload entry
// node, a pool node, or the public (Funnel) node.
func TestPoolable(t *testing.T) {
	t.Parallel()
	for class, want := range map[string]bool{"SupportAgent": true, "": false, PoolClass: false, "_workload": false, "_public": false} {
		if got := poolable(class); got != want {
			t.Errorf("poolable(%q) = %v, want %v", class, got, want)
		}
	}
}

// A host restart rejoins idle pool nodes from saved state, like the
// objects they will be handed to (ADR-031), but only for a class that
// still has its own node in the app being started. (Before the fix, a
// pool node never matched: its Class is PoolClass, not one of app's.)
func TestRejoinsWithPoolNodes(t *testing.T) {
	t.Parallel()
	app := &bundle.App{Classes: map[string]*bundle.Class{
		"SupportAgent": {Name: "SupportAgent", Node: true},
		"Counter":      {Name: "Counter"},
	}}
	for _, tc := range []struct {
		o    *object
		want bool
	}{
		{&object{ID: "entry"}, true},
		{&object{ID: "agent", Class: "SupportAgent", Name: "a"}, true},
		{&object{ID: "counter", Class: "Counter", Name: "c"}, false},
		{&object{ID: "gone", Class: "Removed", Name: "g"}, false},
		{&object{ID: "pool-agent", Class: PoolClass, Name: "SupportAgent/x"}, true},
		{&object{ID: "pool-counter", Class: PoolClass, Name: "Counter/x"}, false},
		{&object{ID: "pool-gone", Class: PoolClass, Name: "Removed/x"}, false},
		{&object{ID: "pool-bad", Class: PoolClass}, false},
	} {
		if got := rejoinsWith(tc.o, app); got != tc.want {
			t.Errorf("rejoinsWith(%s) = %v, want %v", tc.o.ID, got, tc.want)
		}
	}
}
