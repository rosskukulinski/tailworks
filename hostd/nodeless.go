package hostd

import (
	"context"
	"sort"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// Classes with no node of their own (#359).
//
// A Durable Object class gets a node per object only when mgmt declares
// it: tailnet.objects lists it with tags (ADR-006, tailworks-jsonc.md).
// bundle.Parse marks every durable_objects class Node unless tailworks.jsonc
// says "node": false, so a class missing from tailnet.objects (a
// tailworks.jsonc with only the workload's tags, as e2e/compat writes) was
// a node class here and undeclared at mgmt. Every create of one of its
// objects was refused (unknown_namespace), and retried, with a pool
// refill, on every call to the object; hostd never knew the object, so a
// Workflow calling back into it (the Agents SDK's AgentWorkflow) was
// refused it as for a public request. fakemgmt declares every class, so tw
// dev never showed it.
//
// Such a class keeps its egress mode (and so its storage: a facet stays a
// facet) but has no node: hostd never asks mgmt for one, admission doesn't
// apply (admitCreate), and a facet's egress leaves as the workload node,
// as a "node": false class's does.

// nodesFromMgmt clears Node on the classes of app that mgmt's assignment
// does not declare, and returns their names (sorted).
func nodesFromMgmt(app *bundle.App, declared []mgmtapi.NamespaceSpec) []string {
	in := map[string]bool{}
	for _, ns := range declared {
		in[ns.Class] = true
	}
	var off []string
	for name, c := range app.Classes {
		if c.Node && !in[name] {
			c.Node = false
			off = append(off, name)
		}
	}
	sort.Strings(off)
	return off
}

// nodelessClass is ensure's refusal for a class of w with no node: no mgmt
// object, no node, no pool. nil for any other class.
func nodelessClass(w *workload, class string) *apiError {
	if class == "" {
		return nil
	}
	w.mu.Lock()
	app := w.app
	w.mu.Unlock()
	if app == nil {
		return nil
	}
	if c := app.Classes[class]; c != nil && !c.Node {
		return &apiError{Code: "bad_request", Detail: "class " + class + " has no tailnet node (not in tailnet.objects, or \"node\": false)"}
	}
	return nil
}

// facetEgressObject is the object a facet's fetch() or socket leaves as:
// its own node, or the workload node for a class with none.
func (h *Host) facetEgressObject(ctx context.Context, inst *instance, class, name string) (*object, error) {
	if c := inst.app.Classes[class]; c != nil && !c.Node {
		o := inst.w.currentWL()
		if o == nil {
			return nil, &apiError{Code: "identity_pending", Detail: "workload node not created yet"}
		}
		return o, nil
	}
	return h.ensure(ctx, inst.w, class, name, nil, nil, "")
}

// admitWorkflow answers "admit" from a Workflow's guarded env, which has no
// create ticket (ADR-040: a Workflow reaches existing objects only). An
// object of a class with no node needs no admission (as in admitCreate):
// nothing is minted, and hostd never knows whether one "exists", so the
// public rule (admitPublic: such classes only under public.create) left an
// AgentWorkflow unable to reach its own Agent once the runtime's admit
// cache had expired. Everything else is admitPublic's answer.
func (h *Host) admitWorkflow(w *workload, app *bundle.App, class, name string) (existing bool, err *apiError) {
	if c := app.Classes[class]; c != nil && !c.Node && name != "" {
		return true, nil
	}
	return h.admitPublic(w, app, class, name)
}

// nodelessEnsure answers the platform's "ensure" for an object of a class
// with no node (a facet's Host_ wrapper ensures before every call): there
// is nothing to create, so it succeeds without one.
func nodelessEnsure(app *bundle.App, class, name string) (map[string]any, bool) {
	c := app.Classes[class]
	if c == nil || c.Node || name == "" {
		return nil, false
	}
	return map[string]any{"kind": "object", "class": class, "name": name, "node": false}, true
}
