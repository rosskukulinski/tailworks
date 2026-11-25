package hostd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// TestNodesFromMgmt (#359): a class gets a node only when mgmt declares
// it; an undeclared one keeps its egress mode (a facet stays a facet, so
// its storage stays where it is).
func TestNodesFromMgmt(t *testing.T) {
	t.Parallel()
	app := &bundle.App{Classes: map[string]*bundle.Class{
		"Declared": {Name: "Declared", Node: true, Egress: bundle.EgressObject},
		"Unlisted": {Name: "Unlisted", Node: true, Egress: bundle.EgressObject},
		"Internal": {Name: "Internal", Node: false, Egress: bundle.EgressWorkload},
	}}
	off := nodesFromMgmt(app, []mgmtapi.NamespaceSpec{{Class: "Declared", Tags: []string{"tag:a"}}})
	if !reflect.DeepEqual(off, []string{"Unlisted"}) {
		t.Fatalf("classes without a node: %v, want [Unlisted]", off)
	}
	if !app.Classes["Declared"].Node || app.Classes["Unlisted"].Node || app.Classes["Internal"].Node {
		t.Fatalf("Node: Declared %v Unlisted %v Internal %v", app.Classes["Declared"].Node, app.Classes["Unlisted"].Node, app.Classes["Internal"].Node)
	}
	if !app.Classes["Unlisted"].Facet() {
		t.Fatal("an undeclared facet class must stay a facet (its storage)")
	}
}

// TestNodelessClassAdmitAndEnsure (#359): an object of a class with no node
// is never created at mgmt (the facet's ensure answers without one, and
// ensure refuses), and a Workflow (no ticket) may reach it, while a public
// request still may not (public.create).
func TestNodelessClassAdmitAndEnsure(t *testing.T) {
	t.Parallel()
	h, inst, creates := admitKillHost(t)
	inst.app.Classes["Agent"] = &bundle.Class{Name: "Agent", Node: false, Egress: bundle.EgressObject}
	inst.w.app = inst.app

	code, r := platformCall(t, h, inst, "ensure", map[string]any{"as": map[string]any{"class": "Agent", "name": "a1"}, "wake": true})
	if code != 200 || r["node"] != false {
		t.Fatalf("ensure of a node-less facet object: %d %v", code, r)
	}
	if _, err := h.ensure(context.Background(), inst.w, "Agent", "a1", nil, nil, ""); err == nil {
		t.Fatal("ensure of a class with no node: want an error, not an object")
	}
	if n := creates.Load(); n != 0 {
		t.Fatalf("mgmt was asked to create %d object(s) of a class with no node", n)
	}

	code, r = platformCall(t, h, inst, "admit", map[string]any{"class": "Agent", "name": "a1", "workflow": true})
	if code != 200 || r["existing"] != true {
		t.Fatalf("a Workflow's admit of a node-less object: %d %v", code, r)
	}
	code, r = platformCall(t, h, inst, "admit", map[string]any{"class": "Agent", "name": "a1"})
	if code != 403 || r["code"] != "public_create_denied" {
		t.Fatalf("a public admit of a node-less object: %d %v (want 403 public_create_denied)", code, r)
	}
	// A node class: a Workflow reaches existing objects only (ADR-040).
	code, r = platformCall(t, h, inst, "admit", map[string]any{"class": "Room", "name": "new", "workflow": true})
	if code != 403 || r["code"] != "public_create_denied" {
		t.Fatalf("a Workflow's admit of a new node-class object: %d %v (want 403 public_create_denied)", code, r)
	}
	liveObject(h, "o1", "Room", "old")
	code, r = platformCall(t, h, inst, "admit", map[string]any{"class": "Room", "name": "old", "workflow": true})
	if code != 200 || r["existing"] != true {
		t.Fatalf("a Workflow's admit of an existing node-class object: %d %v", code, r)
	}
}

// TestRefusedCreateNoPoolDemand (#359): a create mgmt refuses is not
// demand for a pool of its class. Before, every refused create of an
// undeclared class started a pool refill that mgmt refused as well, once a
// second while anything called the object.
func TestRefusedCreateNoPoolDemand(t *testing.T) {
	t.Parallel()
	var creates atomic.Int32
	mg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		creates.Add(1)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(404)
		w.Write([]byte(`{"code":"unknown_namespace","message":"test mgmt: no deployment declares it"}`))
	}))
	t.Cleanup(mg.Close)
	h, _ := ctrHost(t, func(c *Config) { c.Mgmt = &mgmtapi.Client{Base: mg.URL}; c.PoolNodes = 2 })
	w := &workload{h: h, tenantID: "tn", name: "app", tenant: &tenant{}}
	if _, err := h.create(context.Background(), w, "Room", "r1", nil, nil, "", ""); err == nil {
		t.Fatal("create: want mgmt's refusal")
	}
	h.mu.Lock()
	n := len(h.poolDemand)
	h.mu.Unlock()
	if n != 0 || creates.Load() != 1 {
		t.Fatalf("after a refused create: pool demand %d (want 0), mgmt creates %d (want 1)", n, creates.Load())
	}
}
