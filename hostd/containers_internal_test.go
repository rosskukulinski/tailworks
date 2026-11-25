package hostd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/hostd/sandbox/sandboxtest"
	"github.com/rosskukulinski/tailworks/internal/container"
)

func ctrHost(t *testing.T, mod func(*Config)) (*Host, *sandboxtest.Procs) {
	t.Helper()
	procs := &sandboxtest.Procs{}
	st, err := sandboxtest.ImageStore(t.TempDir(), []string{"sleep", "3600"}, "tw.test/box:1", "tw.test/other:1")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{HostID: "h", Mgmt: &mgmtapi.Client{}, DataDir: t.TempDir(), WorkerdBin: "/bin/true", Containers: procs, Images: st,
		ContainerQuota: newFakeQuota(), Capacity: &roomy}
	if mod != nil {
		mod(&cfg)
	}
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.ctr.destroyWhere(func(ctrKey) bool { return true }, "test end", 0) })
	return h, procs
}

func ctrInst(h *Host, tenant, wl string) *instance {
	w := &workload{h: h, tenantID: tenant, name: wl}
	h.mu.Lock()
	h.workloads[tenant+"/"+wl] = w
	h.mu.Unlock()
	app := &bundle.App{Classes: map[string]*bundle.Class{"Box": {Name: "Box", Node: true}, "NoCtr": {Name: "NoCtr", Node: true}},
		Containers: map[string]container.Class{"Box": {Class: "Box", Image: "tw.test/box:1", InstanceType: "lite"}}}
	return &instance{h: h, w: w, app: app}
}

func ctrStart(t *testing.T, h *Host, inst *instance, name string, r startReq) error {
	t.Helper()
	k, cc, err := h.ctrKeyFor(inst, &ctrAs{Class: "Box", Name: name})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.ctr.start(context.Background(), inst, k, cc, r)
	return err
}

func codeOf(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// No container backend: start() is refused (ADR-032 D5, no fallback),
// never run some other way.
func TestContainerNoBackendRefuses(t *testing.T) {
	t.Parallel()
	h, _ := ctrHost(t, func(c *Config) { c.Containers = nil })
	err := ctrStart(t, h, ctrInst(h, "t", "w"), "a", startReq{})
	if codeOf(err) != "tier_unavailable" {
		t.Fatalf("want tier_unavailable, got %v", err)
	}
}

// Only the class's own (pinned) images start, and only container classes
// have a container.
func TestContainerImageAndClassScope(t *testing.T) {
	t.Parallel()
	h, procs := ctrHost(t, nil)
	inst := ctrInst(h, "t", "w")
	if err := ctrStart(t, h, inst, "a", startReq{Image: "tw.test/other:1"}); codeOf(err) != "bad_request" || !strings.Contains(err.Error(), "not one of class Box's images") {
		t.Fatalf("undeclared image: %v", err)
	}
	if _, _, err := h.ctrKeyFor(inst, &ctrAs{Class: "NoCtr", Name: "x"}); codeOf(err) != "bad_request" {
		t.Fatalf("class without a container: %v", err)
	}
	if _, _, err := h.ctrKeyFor(inst, nil); codeOf(err) != "bad_request" {
		t.Fatalf("no caller: %v", err)
	}
	if procs.Boots.Load() != 0 {
		t.Fatal("a refused start booted a VM")
	}
	if err := ctrStart(t, h, inst, "a", startReq{}); err != nil {
		t.Fatal(err)
	}
	if err := ctrStart(t, h, inst, "a", startReq{}); codeOf(err) != "container_running" {
		t.Fatalf("second start: %v", err)
	}
	s := procs.Specs[0]
	if s.VCPUs != 1 || s.CPUMax != 1.0/16 || s.MemMiB != 256 || s.Argv[0] != "sleep" {
		t.Fatalf("lite shape / argv: %+v", s)
	}
}

// Host capacity and the mgmt quota both gate start(); a quota error fails
// closed (nothing boots).
func TestContainerCapacityAndQuota(t *testing.T) {
	t.Parallel()
	h, procs := ctrHost(t, func(c *Config) { c.ContainerCapacity = ContainerCapacity{Max: 1} })
	inst := ctrInst(h, "t", "w")
	if err := ctrStart(t, h, inst, "a", startReq{}); err != nil {
		t.Fatal(err)
	}
	if err := ctrStart(t, h, inst, "b", startReq{}); codeOf(err) != "capacity_exhausted" {
		t.Fatalf("over capacity: %v", err)
	}
	h.ctr.destroy(ctrKey{"t", "w", "Box", "a"}, "test", 0)
	if err := ctrStart(t, h, inst, "b", startReq{}); err != nil {
		t.Fatalf("after a stopped: %v", err)
	}
	if procs.Live() != 1 {
		t.Fatalf("live %d", procs.Live())
	}

	// The class's max_instances.
	h3, _ := ctrHost(t, nil)
	inst3 := ctrInst(h3, "t", "w")
	box := inst3.app.Containers["Box"]
	box.MaxInstances = 1
	inst3.app.Containers["Box"] = box
	if err := ctrStart(t, h3, inst3, "a", startReq{}); err != nil {
		t.Fatal(err)
	}
	if err := ctrStart(t, h3, inst3, "b", startReq{}); codeOf(err) != "container_quota_exceeded" {
		t.Fatalf("over max_instances: %v", err)
	}

	// mgmt unreachable: fail closed before anything boots; reachable: the
	// reservation carries the class and the lite size, and stopping the
	// container releases it.
	q := newFakeQuota()
	q.setDown(true)
	h2, procs2 := ctrHost(t, func(c *Config) { c.ContainerQuota = q })
	inst2 := ctrInst(h2, "t", "w")
	if err := ctrStart(t, h2, inst2, "denied", startReq{}); codeOf(err) != "quota_unavailable" || procs2.Boots.Load() != 0 {
		t.Fatalf("quota failure must fail closed: %v (boots %d)", err, procs2.Boots.Load())
	}
	q.setDown(false)
	if err := ctrStart(t, h2, inst2, "ok", startReq{}); err != nil {
		t.Fatal(err)
	}
	h2.ctr.destroy(ctrKey{"t", "w", "Box", "ok"}, "test", 0)
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.released) != 1 || len(q.reserves) != 2 || q.reserves[1].Resources.MemMiB != 256 || q.reserves[1].Class != "Box" || len(q.byKey) != 0 {
		t.Fatalf("reservations %+v released %v", q.reserves, q.released)
	}
}

// ADR-028: a container inherits its parent's fence. Heartbeat loss stops
// the stateful workload's containers (two live copies under one identity
// must not happen) and leaves a stateless workload's running.
func TestContainerFencedWithParent(t *testing.T) {
	t.Parallel()
	h, procs := ctrHost(t, nil)
	stateful := ctrInst(h, "t", "w")
	stateless := ctrInst(h, "t", "w2")
	stateless.w.noFence = true
	for _, in := range []*instance{stateful, stateless} {
		if err := ctrStart(t, h, in, "a", startReq{}); err != nil {
			t.Fatal(err)
		}
	}
	h.log = h.cfg.Logger
	st := time.Now()
	h.fenceStateful("heartbeat lost")
	d := time.Since(st)
	states := map[string]string{}
	for _, c := range h.Containers() {
		states[c.Workload] = c.State
	}
	if states["w"] != "stopped" || states["w2"] != "running" || procs.Live() != 1 {
		t.Fatalf("after fence: %v (live %d)", states, procs.Live())
	}
	if d > time.Second {
		t.Fatalf("fencing the containers took %v", d)
	}
}

// The operator's hard max lifetime and the inactivity timeout stop a
// container no matter what the object does.
func TestContainerTimers(t *testing.T) {
	t.Parallel()
	h, procs := ctrHost(t, func(c *Config) { c.ContainerMaxLife = 700 * time.Millisecond })
	inst := ctrInst(h, "t", "w")
	if err := ctrStart(t, h, inst, "old", startReq{}); err != nil {
		t.Fatal(err)
	}
	k := ctrKey{"t", "w", "Box", "idle"}
	if err := ctrStart(t, h, inst, "idle", startReq{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ctr.op(context.Background(), inst, k, boxClass(inst), &ctrReq{Op: "setInactivityTimeout", MS: "300"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ctr.op(context.Background(), inst, k, boxClass(inst), &ctrReq{Op: "setInactivityTimeout", MS: "21600001"}); codeOf(err) != "bad_request" {
		t.Fatalf("over 6 h: %v", err)
	}
	// Wait for each run's done, which finish closes after it records the
	// state and reason. Not procs.Live() == 0: the process is gone a moment
	// before finish records why, so that raced the "old" stop (issue #239).
	for _, name := range []string{"old", "idle"} {
		c := h.ctr.lookup(ctrKey{"t", "w", "Box", name})
		c.mu.Lock()
		done := c.done
		c.mu.Unlock()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: not stopped after 10 s", name)
		}
	}
	if n := procs.Live(); n != 0 {
		t.Fatalf("%d processes still live", n)
	}
	reasons := map[string]string{}
	for _, c := range h.Containers() {
		reasons[c.Object] = c.State + " " + c.ExitReason
	}
	if reasons["old"] != "stopped max lifetime" || reasons["idle"] != "stopped inactivity timeout" {
		t.Fatalf("%v", reasons)
	}
}

// An exec ID is only good for the container it was started in.
func TestContainerExecIDsAreScoped(t *testing.T) {
	t.Parallel()
	h, _ := ctrHost(t, nil)
	inst := ctrInst(h, "t", "w")
	a, b := ctrKey{"t", "w", "Box", "a"}, ctrKey{"t", "w", "Box", "b"}
	h.ctr.execs["e1"] = &ctrExec{key: a}
	if _, err := h.ctr.op(context.Background(), inst, b, boxClass(inst), &ctrReq{Op: "exec-signal", Exec: "e1", Signal: 9}); codeOf(err) != "unknown_object" {
		t.Fatalf("another object's exec: %v", err)
	}
}

func boxClass(inst *instance) *container.Class {
	c := inst.app.Containers["Box"]
	return &c
}

// TestContainerNoTailnetNode (ADR-032 D4 × fast-identity): starting
// containers creates no object and no node, and marks no class hot for the
// pre-joined node pool (whose pool nodes and warm keys serve object creates
// only), even with the pool on.
func TestContainerNoTailnetNode(t *testing.T) {
	t.Parallel()
	h, procs := ctrHost(t, func(c *Config) { c.PoolNodes = 2 })
	inst := ctrInst(h, "t", "w")
	for _, n := range []string{"a", "b"} {
		if err := ctrStart(t, h, inst, n, startReq{}); err != nil {
			t.Fatal(err)
		}
	}
	if procs.Live() != 2 {
		t.Fatalf("live %d", procs.Live())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.objects) != 0 || len(h.poolDemand) != 0 {
		t.Fatalf("containers made objects %v / pool demand %v", h.objects, h.poolDemand)
	}
}
