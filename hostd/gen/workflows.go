package gen

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// Workflows (ADR-040). workerd's built-in engine, the
// bundled Worker /workerd/workflows.js (workerd 1.20261114.1+, run with
// --experimental), runs a deployment's Workflows:
//
//   - service "workflows" is the engine: its Engine class is the actor class
//     of every Workflow's instance namespace, its WorkflowBinding entrypoint
//     the Workflow API. Its instance storage is the workload's do-storage
//     disk, so instances live where the workload's objects do: on the lease
//     holder, fenced with it, and in the microVM's encrypted volume. Each
//     instance is a SQLite-backed Durable Object with no tailnet node.
//   - the tenant service names the engine in workflowsEngine (the Workflow
//     classes are its exports), and binds each Workflow as wrangler would,
//     a cloudflare-internal:workflows-api wrapped binding, except that the
//     inner fetcher is the platform's WorkflowGate, not the engine.
//   - WorkflowGate forwards every call to the engine and keeps approvals
//     tailworks': app code may not send an approval event, and no event it sends
//     carries an "approver" (platform.js).
//   - the platform binds the engine directly (WF_<name>) to deliver the
//     approvals hostd has authorised and audited.

// workflowsCompatDate is the engine worker's compatibility date (as
// workerd's own wrangler mapping sets it).
const workflowsCompatDate = "2026-01-01"

const preserveNonRetryable = "workflows_preserve_non_retryable_error_message"

// workflowNamespaceKey is the engine's instance namespace of a Workflow
// (workerd cluster::compose::workflow_namespace_key).
func workflowNamespaceKey(name string) string { return "miniflare-workflows-" + name }

func gateDesignator(name string) string {
	props, _ := json.Marshal(map[string]string{"workflow": name})
	return fmt.Sprintf("(name = \"platform\", entrypoint = \"WorkflowGate\", props = (json = %s))", cq(string(props)))
}

// workflowsService is the services[] entry ("" without Workflows).
func workflowsService(app *bundle.App) string {
	if len(app.Workflows) == 0 {
		return ""
	}
	return "    (name = \"workflows\", worker = .workflows),\n"
}

// workflowsWorker is the engine worker's definition ("" without Workflows).
func workflowsWorker(app *bundle.App) string {
	if len(app.Workflows) == 0 {
		return ""
	}
	var c strings.Builder
	cfg := map[string]map[string]any{}
	for _, w := range app.Workflows {
		s := map[string]any{}
		if w.Steps > 0 {
			s["steps"] = w.Steps
		}
		if len(w.Retention) > 0 {
			s["retention"] = json.RawMessage(w.Retention)
		}
		cfg[w.Name] = s
	}
	cfgb, _ := json.Marshal(cfg)
	fmt.Fprintf(&c, "const workflows :Workerd.Worker = (\n  compatibilityDate = %s,\n", cq(workflowsCompatDate))
	// The engine reports a step's NonRetryableError as the app's flags ask
	// (the same rule as workerd's wrangler mapping).
	has := func(f string) bool {
		for _, g := range app.CompatFlags {
			if g == f {
				return true
			}
		}
		return false
	}
	if has(preserveNonRetryable) || (app.CompatDate >= "2026-05-14" && !has("workflows_replace_non_retryable_error_message")) {
		fmt.Fprintf(&c, "  compatibilityFlags = [%s],\n", cq(preserveNonRetryable))
	}
	c.WriteString("  modules = [ (name = \"workflows.js\", bundled = \"/workerd/workflows.js\") ],\n")
	c.WriteString("  durableObjectStorage = (localDisk = \"do-storage\"),\n  bindings = [\n")
	for _, w := range app.Workflows {
		fmt.Fprintf(&c, "    (name = %s, durableObjectNamespace = (className = %s, serviceName = \"tenant\")),\n", cq("workflow:"+w.Name), cq(workflowNamespaceKey(w.Name)))
	}
	fmt.Fprintf(&c, "    (name = \"WORKFLOWS\", json = %s),\n", cq(string(cfgb)))
	c.WriteString("  ],\n  globalOutbound = \"deny\",\n);\n\n")
	return c.String()
}

// workflowsTenantBindings are the tenant's env.<binding> entries.
func workflowsTenantBindings(app *bundle.App) string {
	var c strings.Builder
	for _, w := range app.Workflows {
		fmt.Fprintf(&c, "    (name = %s, wrapped = (moduleName = \"cloudflare-internal:workflows-api\", innerBindings = [ (name = \"fetcher\", service = %s) ])),\n", cq(w.Binding), gateDesignator(w.Name))
	}
	return c.String()
}

// workflowsEngine is the tenant worker's workflowsEngine field ("" without
// Workflows).
func workflowsEngine(app *bundle.App) string {
	if len(app.Workflows) == 0 {
		return ""
	}
	var c strings.Builder
	c.WriteString("  workflowsEngine = (\n    actorClass = (name = \"workflows\", entrypoint = \"Engine\"),\n    workflows = [\n")
	for _, w := range app.Workflows {
		fmt.Fprintf(&c, "      (className = %s, name = %s, bindingService = %s),\n", cq(w.Class), cq(w.Name), gateDesignator(w.Name))
	}
	c.WriteString("    ],\n  ),\n")
	return c.String()
}

// workflowsPlatformBindings bind the engine's Workflow API into the
// platform (WF_<name>), for WorkflowGate and approval delivery.
func workflowsPlatformBindings(app *bundle.App) string {
	var c strings.Builder
	for _, w := range app.Workflows {
		props, _ := json.Marshal(map[string]string{"workflow": w.Name})
		fmt.Fprintf(&c, "    (name = %s, service = (name = \"workflows\", entrypoint = \"WorkflowBinding\", props = (json = %s))),\n", cq("WF_"+w.Name), cq(string(props)))
	}
	return c.String()
}

// workflowsMainExports wrap each Workflow class in the tenant main module:
// the runtime reports approval waits to the platform and guards env.
func workflowsMainExports(app *bundle.App) string {
	var c strings.Builder
	for _, w := range app.Workflows {
		fmt.Fprintf(&c, "export const %s = wrapWorkflowClass(T[%s], %s, %s, %s, B);\n", w.Class, jsq(w.Class), jsq(w.Class), jsq(w.Name), jsq(w.Binding))
	}
	return c.String()
}

// workflowsFacetBindings are the facet env's Workflow bindings (binding →
// Workflow name), which the runtime turns into Workflow clients over
// WorkflowGate.
func workflowsFacetBindings(app *bundle.App) string {
	m := map[string]string{}
	for _, w := range app.Workflows {
		m[w.Binding] = w.Name
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// workflowsFacetPrelude declares WF in the facet main module ("" without
// Workflows, so apps without them generate as before).
func workflowsFacetPrelude(app *bundle.App) string {
	if len(app.Workflows) == 0 {
		return ""
	}
	return "const WF = " + workflowsFacetBindings(app) + ";\n"
}

// workflowsFacetOpt is wrapFacetClass's workflows option: inside an
// options object ("workflows: WF, ") or as the whole argument (", {
// workflows: WF }"); "" without Workflows.
func workflowsFacetOpt(app *bundle.App, inObject bool) string {
	if len(app.Workflows) == 0 {
		return ""
	}
	if inObject {
		return "workflows: WF, "
	}
	return ", { workflows: WF }"
}

// workflowsConfig is tw-config.json's "workflows".
type workflowCfg struct {
	Name    string `json:"name"`
	Binding string `json:"binding"`
	Class   string `json:"class"`
}

func workflowsConfig(app *bundle.App) []workflowCfg {
	out := []workflowCfg{}
	for _, w := range app.Workflows {
		out = append(out, workflowCfg{w.Name, w.Binding, w.Class})
	}
	return out
}
