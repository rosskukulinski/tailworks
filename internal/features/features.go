// Package features names the host-side capabilities a deployment can need
// (ADR-041 §7). hostd advertises Host() when it
// registers; mgmt places a deployment only on hosts that advertise every
// feature Required() finds in its bundle, so a feature that needs new
// hostd code can merge and roll out host by host.
//
// A feature string is added here when its hostd code lands, one per line
// in sorted position (AGENTS.md rule 3). Features that predate version
// reporting (tailworks 0.1.x: workerd, DOs, cron, public ingress, containers,
// secrets) are not listed: every host has them, and tiers and the
// "containers" capability already gate placement for those.
package features

import (
	"slices"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
	"github.com/rosskukulinski/tailworks/internal/compatflags"
)

// Feature strings. A platform store kind (hostd/bundle RegisterStoreKind)
// is its own feature: "d1", "kv", "r2", …
const (
	// Assets is wrangler "assets": static files served by a generated
	// router Worker (ADR-050). An older hostd would load the
	// bundle's assets/ files as modules, or not serve them.
	Assets = "assets"
	// ExperimentalFlags is a host that allows compatibility flags workerd
	// marks experimental (tw-hostd -allow-experimental-flags, #337). A
	// bundle that lists one (internal/compatflags) needs it; every other
	// host refuses the deployment (experimental_flag). It depends on the
	// host's flags, not its build, so hostd adds it to Host() itself.
	ExperimentalFlags = "experimental-flags"
	// InternetExitNode is a host whose internet path is a tailnet exit
	// node (-worker-internet=exit-node=…, ADR-054). A bundle
	// with tailnet.egress.via "exit-node" needs it. It depends on the
	// host's flags, not its build, so hostd adds it to Host() itself.
	InternetExitNode = "internet-exit-node"
	// InternetPorts is a tailnet.egress.internet entry that names a port
	// ("smtp.example.com:25"), the override of the internet port policy
	// (ADR-052 follow-up, 2026-11-24). An older hostd reads such an entry
	// as a host name that matches nothing.
	InternetPorts = "internet-ports"
	// Queues is queue producers and consumers (ADR-039).
	Queues = "queues"
	// ServiceBindings is wrangler "services" (ADR-053). An
	// older hostd leaves env.<binding> undefined.
	ServiceBindings = "service-bindings"
	// TailnetHTTPS is tailworks.jsonc tailnet.https: the workload node
	// also serves :443 with its ts.net certificate (issue
	// #360). An older hostd ignores the key and serves http:// only.
	TailnetHTTPS = "tailnet-https"
	// Telemetry is OTLP export, request records and /metrics (ADR observability).
	Telemetry = "telemetry"
	// WorkerInternet is tailnet.egress.internet's boolean form (true or
	// false) and Worker internet egress (ADR-049). An
	// older hostd can't parse a boolean there; the list form needs nothing.
	WorkerInternet = "worker-internet"
	// Workflows is wrangler "workflows" on workerd's engine, with approvals
	// by tailnet identity (ADR-040).
	Workflows = "workflows"
)

// Host is what this build's hostd offers: every registered platform store
// kind plus the static features above, sorted.
func Host() []string {
	out := []string{
		Assets,
		InternetPorts,
		Queues,
		ServiceBindings,
		TailnetHTTPS,
		Telemetry,
		WorkerInternet,
		Workflows,
	}
	out = append(out, bundle.StoreKindNames()...)
	slices.Sort(out)
	return slices.Compact(out)
}

// Required is what a parsed app needs from its host, sorted.
func Required(app *bundle.App) []string {
	var out []string
	if app.Assets != nil {
		out = append(out, Assets)
	}
	for _, s := range app.Stores {
		out = append(out, s.Kind)
	}
	if len(app.Workflows) > 0 {
		out = append(out, Workflows)
	}
	if app.Queues != nil && (len(app.Queues.Producers) > 0 || len(app.Queues.Consumers) > 0) {
		out = append(out, Queues)
	}
	if app.Internet.Boolean() {
		out = append(out, WorkerInternet)
	}
	if app.Internet.NamesPort() {
		out = append(out, InternetPorts)
	}
	if len(app.Services) > 0 {
		out = append(out, ServiceBindings)
	}
	if app.EgressVia == bundle.ViaExitNode {
		out = append(out, InternetExitNode)
	}
	if app.TailnetHTTPS {
		out = append(out, TailnetHTTPS)
	}
	if len(compatflags.Filter(app.CompatFlags)) > 0 {
		out = append(out, ExperimentalFlags)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// RequiredByBundle unpacks and parses a deploy bundle and returns
// Required. A bundle that doesn't parse needs nothing extra (deploy's own
// validation reports it).
func RequiredByBundle(b []byte) []string {
	files, err := bundle.Unpack(b)
	if err != nil {
		return nil
	}
	app, err := bundle.Parse(files)
	if err != nil || app == nil {
		return nil
	}
	return Required(app)
}

// Missing returns the features in need that have does not list.
func Missing(need, have []string) []string {
	var out []string
	for _, f := range need {
		if !slices.Contains(have, f) {
			out = append(out, f)
		}
	}
	return out
}
