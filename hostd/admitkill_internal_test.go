package hostd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/internal/publicspec"
)

// admitKillHost is a host with one deployed workload (tn/app) whose
// classes are Room (a node class) and Open (a node class public requests
// may create), a workerd admin endpoint that acknowledges kills, and a mgmt
// that counts object creates (and refuses them, so no node starts).
func admitKillHost(t *testing.T) (*Host, *instance, *atomic.Int32) {
	t.Helper()
	var creates atomic.Int32
	mg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v1/objects" {
			creates.Add(1)
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(503)
		w.Write([]byte(`{"code":"upstream_unavailable","message":"test mgmt"}`))
	}))
	t.Cleanup(mg.Close)
	h, _ := ctrHost(t, func(c *Config) { c.Mgmt = &mgmtapi.Client{Base: mg.URL} })
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }))
	t.Cleanup(admin.Close)
	w := &workload{h: h, tenantID: "tn", name: "app", tenant: &tenant{}}
	h.mu.Lock()
	h.workloads["tn/app"] = w
	h.mu.Unlock()
	app := &bundle.App{Classes: map[string]*bundle.Class{
		"Room": {Name: "Room", Node: true, Egress: bundle.EgressWorkload},
		"Open": {Name: "Open", Node: true, Egress: bundle.EgressWorkload, Public: &publicspec.ClassSpec{Create: true, MaxObjects: 2}},
	}}
	inst := &instance{h: h, w: w, app: app}
	addr := admin.Listener.Addr().String()
	inst.direct = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		},
	}}
	w.active = inst
	return h, inst, &creates
}

// platformCall is one call of the platform's HOSTD binding (hostAPI).
func platformCall(t *testing.T, h *Host, inst *instance, op string, body map[string]any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h.hostAPI(inst).ServeHTTP(rec, httptest.NewRequest("POST", "http://hostd/v1/"+op, bytes.NewReader(b)))
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

// liveObject registers a ready object of tn/app, as a create would have.
func liveObject(h *Host, id, class, name string) *object {
	o := &object{ID: id, NetdID: "n_" + id, TenantID: "tn", Workload: "app", Class: class, Name: name,
		Hostname: name, Status: statusReady, readyCh: make(chan struct{})}
	h.mu.Lock()
	h.objects[o.ID] = o
	h.byKey[o.key()] = o
	h.mu.Unlock()
	return o
}

// TestRT3_H19_AdmitCacheAfterKill (#153, ledger H19): nothing that admitted
// a name before it was killed may create it again once it is killed and
// purged. The DO constructor's ensure (which a cached admit in the
// isolate, a raw binding, or a stub kept from before the kill reaches
// without a new admit) must be refused, and mgmt must never be asked to
// mint a node for it.
func TestRT3_H19_AdmitCacheAfterKill(t *testing.T) {
	t.Parallel()
	as := func(class, name string) map[string]any {
		return map[string]any{"as": map[string]any{"class": class, "name": name}}
	}
	// ensureRefused: the object's own ensure (its DO constructor) creates nothing.
	ensureRefused := func(t *testing.T, h *Host, inst *instance, creates *atomic.Int32, class, name string) {
		t.Helper()
		code, r := platformCall(t, h, inst, "ensure", as(class, name))
		if code != 403 || r["code"] != "create_not_admitted" {
			t.Errorf("ensure %s/%s after kill+purge: %d %v (want 403 create_not_admitted)", class, name, code, r)
		}
		if n := creates.Load(); n != 0 {
			t.Errorf("mgmt was asked to create %d object(s) for a killed, purged name (a node re-minted)", n)
		}
	}

	t.Run("admission pending at the kill", func(t *testing.T) {
		h, inst, creates := admitKillHost(t)
		// Room/a is admitted (a tailnet request or an attested object),
		// then created by a path that consumes no admission (a spawn by
		// an object): the admission is still pending when a is killed.
		if code, r := platformCall(t, h, inst, "admit", map[string]any{"class": "Room", "name": "a", "as": map[string]any{"class": "Room", "name": "parent"}}); code != 200 || r["existing"] != false {
			t.Fatalf("admit Room/a: %d %v", code, r)
		}
		o := liveObject(h, "obj_a", "Room", "a")
		if err := h.killLocal(o.ID, "test kill"); err != nil {
			t.Fatal(err)
		}
		h.Purge()
		ensureRefused(t, h, inst, creates, "Room", "a")
	})

	t.Run("admitted between the kill and the purge", func(t *testing.T) {
		h, inst, creates := admitKillHost(t)
		o := liveObject(h, "obj_b", "Room", "b")
		if err := h.killLocal(o.ID, "test kill"); err != nil {
			t.Fatal(err)
		}
		// A caller whose isolate has no cached admit asks again: the name
		// is killed, so nothing may be admitted for it.
		code, r := platformCall(t, h, inst, "admit", map[string]any{"class": "Room", "name": "b", "as": map[string]any{"class": "Room", "name": "parent"}})
		if code != 410 || r["code"] != "killed" {
			t.Errorf("admit of a killed name: %d %v (want 410 killed)", code, r)
		}
		if code, r := platformCall(t, h, inst, "admit", map[string]any{"class": "Open", "name": "b2"}); code != 200 {
			t.Fatalf("public admit of a new Open/b2: %d %v", code, r) // control: a new name still admits
		}
		ob := liveObject(h, "obj_b2", "Open", "b2")
		if err := h.killLocal(ob.ID, "test kill"); err != nil {
			t.Fatal(err)
		}
		if code, r := platformCall(t, h, inst, "admit", map[string]any{"class": "Open", "name": "b2"}); code != 410 || r["code"] != "killed" {
			t.Errorf("public admit of a killed name: %d %v (want 410 killed)", code, r)
		}
		h.Purge()
		ensureRefused(t, h, inst, creates, "Room", "b")
		ensureRefused(t, h, inst, creates, "Open", "b2")
		// After the purge the name is free again, for a caller that admits it anew.
		if code, r := platformCall(t, h, inst, "admit", map[string]any{"class": "Room", "name": "b", "as": map[string]any{"class": "Room", "name": "parent"}}); code != 200 || r["existing"] != false {
			t.Errorf("admit Room/b after the purge: %d %v (want 200, a new object)", code, r)
		}
	})

	t.Run("publicly created plain DO", func(t *testing.T) {
		h, inst, creates := admitKillHost(t)
		// A public request admits Open/c under public.create; the plain
		// DO's constructor creates it (its ensure carries no actingFor).
		if code, r := platformCall(t, h, inst, "admit", map[string]any{"class": "Open", "name": "c"}); code != 200 || r["existing"] != false {
			t.Fatalf("public admit Open/c: %d %v", code, r)
		}
		o := liveObject(h, "obj_c", "Open", "c")
		if err := h.killLocal(o.ID, "test kill"); err != nil {
			t.Fatal(err)
		}
		if h.pubCreates.has("tn", "app", "Open", "c") {
			t.Errorf("the killed Open/c still holds its public.create admission (and a maxObjects slot)")
		}
		h.Purge()
		ensureRefused(t, h, inst, creates, "Open", "c")
	})
}

// TestTenantPurgeDropsAdmissions (#153, #107): a deleted tenant leaves no
// pending create admission and no public.create record on the host.
func TestTenantPurgeDropsAdmissions(t *testing.T) {
	t.Parallel()
	h, inst, _ := admitKillHost(t)
	if code, r := platformCall(t, h, inst, "admit", map[string]any{"class": "Room", "name": "d", "as": map[string]any{"class": "Room", "name": "parent"}}); code != 200 {
		t.Fatalf("admit Room/d: %d %v", code, r)
	}
	if code, r := platformCall(t, h, inst, "admit", map[string]any{"class": "Open", "name": "e"}); code != 200 {
		t.Fatalf("public admit Open/e: %d %v", code, r)
	}
	h.pubCreates.admit("tn_other", "app", "Open", "kept", 2, time.Now())
	if err := h.purgeTenant("tn"); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	for k := range h.admits {
		if strings.HasPrefix(k, "tn|") {
			t.Errorf("admission left after the tenant purge: %q", k)
		}
	}
	h.mu.Unlock()
	if h.pubCreates.has("tn", "app", "Open", "e") {
		t.Errorf("public.create record left after the tenant purge")
	}
	if !h.pubCreates.has("tn_other", "app", "Open", "kept") {
		t.Errorf("another tenant's public.create record went")
	}
}
