package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// TestCLILs (#452, #459): tw ls lists live objects and running
// deployments (--all adds killed and stopped), says when the ceiling cut
// the list, and tw ls <workload> keeps the workload, its previews and its
// replicas.
func TestCLILs(t *testing.T) {
	e := newPubEnv(t) // public_test.go: a real tw-mgmt
	ctx := context.Background()
	dep := func(id, workload string, ns mgmt.ClassConfig, stopped int64) {
		t.Helper()
		ns.Class = mgmt.WorkloadNamespace
		if err := e.st.CreateDeployment(ctx, mgmt.Deployment{ID: id, TenantID: e.tenantID, Workload: workload, HostID: "h1", Namespaces: []mgmt.ClassConfig{ns}, CreatedAt: 1, StoppedAt: stopped, HostStatus: "ready"}); err != nil {
			t.Fatal(err)
		}
	}
	dep("d-web", "web", mgmt.ClassConfig{}, 0)
	dep("d-prev", "web--preview-pr1", mgmt.ClassConfig{PreviewOf: "web"}, 0)
	dep("d-rep", "web-r2", mgmt.ClassConfig{ReplicaOf: "web"}, 0)
	dep("d-api", "api", mgmt.ClassConfig{}, 0)
	dep("d-old", "old", mgmt.ClassConfig{}, 5)

	n := int64(0)
	add := func(id, workload, ns, name, status, parent string) {
		t.Helper()
		n++
		if err := e.st.CreateObject(ctx, mgmt.Object{ID: id, TenantID: e.tenantID, Workload: workload, Namespace: ns, Name: name, Hostname: id, RootID: id, ParentID: parent, Status: status, CreatedAt: n}); err != nil {
			t.Fatal(err)
		}
	}
	// Killed tombstones first: the oldest, so they used to eat the page.
	add("room-dead1", "web", "Room", "dead1", mgmt.StatusKilled, "")
	add("room-dead2", "web", "Room", "dead2", mgmt.StatusKilled, "")
	add("wl-web", "web", mgmt.WorkloadNamespace, "web", mgmt.StatusReady, "")
	add("room-lobby", "web", "Room", "lobby", mgmt.StatusReady, "wl-web")
	add("room-orphan", "web", "Room", "orphan", mgmt.StatusReady, "room-dead1")
	add("wl-prev", "web--preview-pr1", mgmt.WorkloadNamespace, "web--preview-pr1", mgmt.StatusReady, "")
	add("wl-rep", "web-r2", mgmt.WorkloadNamespace, "web-r2", mgmt.StatusReady, "")
	add("wl-api", "api", mgmt.WorkloadNamespace, "api", mgmt.StatusReady, "")

	type lsOut struct {
		Deployments []mgmt.DeploymentView `json:"deployments"`
		Objects     []mgmt.Object         `json:"objects"`
		Truncated   *bool                 `json:"truncated"`
	}
	ls := func(args ...string) lsOut {
		t.Helper()
		out, err := e.call(t, cmdLs, append(args, "--json")...)
		var r lsOut
		if err != nil || json.Unmarshal([]byte(out), &r) != nil || r.Truncated == nil {
			t.Fatalf("tw ls %v: %v\n%s", args, err, out)
		}
		return r
	}
	ids := func(r lsOut) (deps, objs []string) {
		for _, d := range r.Deployments {
			deps = append(deps, d.Workload)
		}
		for _, o := range r.Objects {
			objs = append(objs, o.ID)
		}
		slices.Sort(deps)
		return deps, objs
	}
	check := func(name string, r lsOut, wantDeps, wantObjs string, truncated bool) {
		t.Helper()
		d, o := ids(r)
		if strings.Join(d, ",") != wantDeps || strings.Join(o, ",") != wantObjs || *r.Truncated != truncated {
			t.Errorf("%s:\n deployments %v, want %s\n objects %v, want %s\n truncated %v, want %v", name, d, wantDeps, o, wantObjs, *r.Truncated, truncated)
		}
	}

	check("tw ls", ls(), "api,web,web--preview-pr1,web-r2", "wl-web,room-lobby,room-orphan,wl-prev,wl-rep,wl-api", false)
	check("tw ls --all", ls("--all"), "api,old,web,web--preview-pr1,web-r2", "room-dead1,room-dead2,wl-web,room-lobby,room-orphan,wl-prev,wl-rep,wl-api", false)
	check("tw ls --limit 2", ls("--limit", "2"), "api,web,web--preview-pr1,web-r2", "wl-web,room-lobby", true)
	check("tw ls --limit 0", ls("--limit", "0"), "api,web,web--preview-pr1,web-r2", "wl-web,room-lobby,room-orphan,wl-prev,wl-rep,wl-api", false)
	check("tw ls web", ls("web"), "web,web--preview-pr1,web-r2", "wl-web,room-lobby,room-orphan,wl-prev,wl-rep", false)
	check("tw ls web --limit 3", ls("web", "--limit", "3"), "web,web--preview-pr1,web-r2", "wl-web,room-lobby,room-orphan", true)
	check("tw ls web --limit 5", ls("web", "--limit", "5"), "web,web--preview-pr1,web-r2", "wl-web,room-lobby,room-orphan,wl-prev,wl-rep", false)
	check("tw ls api", ls("api"), "api", "wl-api", false)
	check("tw ls old", ls("old"), "", "", false) // stopped: known, nothing live
	check("tw ls old --all", ls("old", "--all"), "old", "", false)

	out, err := e.call(t, cmdLs, "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"preview of web (alias pr1) · tw promote web pr1", "room-dead1"} { // the killed parent: its ID
		if !strings.Contains(out, want) {
			t.Errorf("tw ls web lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "more") {
		t.Errorf("tw ls web says it was cut short:\n%s", out)
	}
	if out, err = e.call(t, cmdLs, "--limit", "2"); err != nil || !strings.Contains(out, "… and 4 more live objects (tw ls --limit 0 lists them all)") {
		t.Errorf("tw ls --limit 2: %v\n%s", err, out)
	}

	if _, err := e.call(t, cmdLs, "nope"); cliCode(err) != "unknown_workload" {
		t.Errorf("tw ls nope: %v", err)
	}
	if _, err := e.call(t, cmdLs, "web", "api"); cliCode(err) != "bad_request" {
		t.Errorf("tw ls web api: %v", err)
	}
}
