// Package svcbind parses wrangler "services" (service bindings,
// ADR-service-bindings): env.<binding> calls another workload of the same
// tenant (fetch and JS RPC to a WorkerEntrypoint). hostd (bundle) and mgmt
// (which resolves a binding's target for a host) read the same rules.
package svcbind

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Binding is one wrangler "services" entry.
type Binding struct {
	// Binding is the env name (a JS identifier).
	Binding string `json:"binding"`
	// Service is the target workload's name in the same tenant.
	Service string `json:"service"`
	// Entrypoint is a named export of the target's main module (a
	// WorkerEntrypoint class); "" is its default export.
	Entrypoint string `json:"entrypoint,omitempty"`
}

var (
	identRE    = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
	workloadRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

type wranglerServices struct {
	Services []struct {
		Binding     string          `json:"binding"`
		Service     string          `json:"service"`
		Entrypoint  string          `json:"entrypoint"`
		Environment string          `json:"environment"`
		Props       json.RawMessage `json:"props"`
	} `json:"services"`
}

// FromWrangler reads "services" from a standardized (plain JSON)
// wrangler config. It returns nil when there are none.
func FromWrangler(std []byte) ([]Binding, error) {
	var w wranglerServices
	if err := json.Unmarshal(std, &w); err != nil {
		return nil, fmt.Errorf("services: %w", err)
	}
	if len(w.Services) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	out := make([]Binding, 0, len(w.Services))
	for i, s := range w.Services {
		where := fmt.Sprintf("services[%d]", i)
		if !identRE.MatchString(s.Binding) || strings.HasPrefix(s.Binding, "__TW") {
			return nil, fmt.Errorf("%s: binding %q must be a JS identifier (names starting with __TW are reserved)", where, s.Binding)
		}
		if seen[s.Binding] {
			return nil, fmt.Errorf("%s: binding %s is declared twice", where, s.Binding)
		}
		seen[s.Binding] = true
		if !workloadRE.MatchString(s.Service) {
			return nil, fmt.Errorf("%s (%s): service %q must name a workload of this tenant (a DNS label: [a-z0-9-], ≤63)", where, s.Binding, s.Service)
		}
		if s.Entrypoint != "" && (!identRE.MatchString(s.Entrypoint) || s.Entrypoint == "default" || strings.HasPrefix(s.Entrypoint, "__tw") || strings.HasPrefix(s.Entrypoint, "Host_")) {
			return nil, fmt.Errorf("%s (%s): entrypoint %q must name an export of %s's main module (a JS identifier; omit it for the default export)", where, s.Binding, s.Entrypoint, s.Service)
		}
		if s.Environment != "" {
			return nil, fmt.Errorf("%s (%s): environment is not supported on tailworks (a workload has one environment; bind the workload by name)", where, s.Binding)
		}
		if len(s.Props) > 0 && string(s.Props) != "null" {
			return nil, fmt.Errorf("%s (%s): props are not supported on tailworks yet", where, s.Binding)
		}
		out = append(out, Binding{Binding: s.Binding, Service: s.Service, Entrypoint: s.Entrypoint})
	}
	return out, nil
}

// Find returns the binding named name, or nil.
func Find(bs []Binding, name string) *Binding {
	for i := range bs {
		if bs[i].Binding == name {
			return &bs[i]
		}
	}
	return nil
}

// ValidEntrypoint reports whether e is an entrypoint name a call may
// carry ("" is the default export).
func ValidEntrypoint(e string) bool {
	return e == "" || (identRE.MatchString(e) && e != "default" && !strings.HasPrefix(e, "__tw") && !strings.HasPrefix(e, "Host_"))
}

// ValidMethod reports whether m is a method name an RPC call may carry.
// Names the runtime reserves (constructor, fetch and the other handlers,
// platform-internal __tw_*) are refused.
func ValidMethod(m string) bool {
	if !identRE.MatchString(m) || strings.HasPrefix(m, "__") {
		return false
	}
	switch m {
	case "constructor", "fetch", "connect", "scheduled", "queue", "tail", "tailStream", "trace", "email", "test", "alarm",
		"webSocketMessage", "webSocketClose", "webSocketError", "then", "dup", "toJSON", "ctx", "env":
		return false
	}
	return true
}
