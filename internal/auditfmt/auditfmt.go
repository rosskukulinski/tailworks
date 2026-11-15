// Package auditfmt renders an audit record's detail as one line of text.
// `twd audit` (cmd/twd) and the admin console (mgmt/ui) both use it, so a
// new audit kind reads the same in both.
package auditfmt

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Describe renders a record of kind with its decoded detail d. name maps an
// object ID to a display name (nil: the ID itself).
func Describe(kind, objectID, actingFor, code string, d map[string]any, name func(id any) string) string {
	if name == nil {
		name = func(id any) string { return fmt.Sprint(id) }
	}
	if s, ok := describeApproval(kind, d); ok {
		return s
	}
	switch kind {
	case "create":
		return fmt.Sprintf("%s/%s tags %s for %s", d["namespace"], d["name"], compact(d["tags"]), orDash(actingFor))
	case "spawn":
		s := fmt.Sprintf("by %s for %s", name(d["parent"]), orDash(actingFor))
		if d["lim"] != nil {
			s += " lim " + compact(d["lim"])
		}
		if d["ttl"] != nil {
			s += fmt.Sprintf(" ttl %v", d["ttl"])
		}
		return s + " tags " + compact(d["tags"])
	case "mint":
		return fmt.Sprintf("key %v tags %s (%v)", d["keyID"], compact(d["tags"]), d["mode"])
	case "ready":
		return fmt.Sprintf("node %v on %v", d["magicDNS"], d["host"])
	case "kill":
		s := "by " + orDash(actingFor)
		if reason, ok := d["reason"].(string); ok && reason != "" {
			s += fmt.Sprintf(" %q", reason)
		}
		if req := fmt.Sprint(d["requested"]); d["requested"] != nil && req != objectID {
			s += " (cascade from " + name(req) + ")"
		}
		if d["pushed"] == true {
			s += " · pushed"
		}
		return s
	case "revoke":
		s := fmt.Sprintf("key %v", d["keyID"])
		if d["pool"] != nil {
			s += fmt.Sprintf(" (pool %v)", d["pool"])
		}
		if d["reason"] != nil {
			s += fmt.Sprintf(": %v", d["reason"])
		}
		return s
	case "credential":
		return fmt.Sprintf("key %v for host %v, epoch %v", d["keyID"], d["host"], d["epoch"])
	case "device_delete":
		if d["error"] != nil {
			return fmt.Sprintf("device %v delete FAILED: %v", d["nodeID"], d["error"])
		}
		return fmt.Sprintf("device %v deleted (%v ms)", d["nodeID"], d["ms"])
	case "deny":
		return fmt.Sprintf("%s %v", code, d["message"])
	// Worker secrets: names only, never values.
	case "secret_put":
		return fmt.Sprintf("secret %v of %v set by %s", d["name"], d["workload"], orDash(actingFor))
	case "secret_delete":
		return fmt.Sprintf("secret %v of %v deleted by %s", d["name"], d["workload"], orDash(actingFor))
	case "secret_release":
		return fmt.Sprintf("secrets %s of %v released to host %v (epoch %v)", compact(d["names"]), d["workload"], d["host"], d["epoch"])
	// Workers KV writes by an admin: the namespace only, never the key.
	case "kv_put":
		return fmt.Sprintf("KV namespace %v of %v: a key written by %s", d["name"], d["workload"], orDash(actingFor))
	case "kv_delete":
		return fmt.Sprintf("KV namespace %v of %v: a key deleted by %s", d["name"], d["workload"], orDash(actingFor))
	// OTLP export (ADR-observability): endpoint and header names only.
	case "telemetry_put":
		return fmt.Sprintf("OTLP export to %v (headers %s, signals %s) set by %s", d["endpoint"], compact(d["headers"]), compact(d["signals"]), orDash(actingFor))
	case "telemetry_delete":
		return fmt.Sprintf("OTLP export removed by %s", orDash(actingFor))
	case "telemetry_release":
		return fmt.Sprintf("OTLP export to %v released to host %v for %v (epoch %v)", d["endpoint"], d["host"], d["workload"], d["epoch"])
	// Public ingress (ADR-029). Host records are best-effort rendered from
	// whichever of these fields they carry; the rest is shown compactly.
	case "public_request":
		s := fmt.Sprintf("%v %v → %v", orAny(d["method"], "?"), orAny(d["path"], "?"), orAny(d["status"], "?"))
		if c := d["code"]; c != nil && c != "" {
			s += fmt.Sprintf(" (%v)", c)
		}
		if ip := orAny(d["ip"], d["clientIP"]); ip != nil {
			s += fmt.Sprintf(" from %v", ip)
		}
		if ms := orAny(d["ms"], d["durationMs"]); ms != nil {
			s += fmt.Sprintf(" %v ms", ms)
		}
		return "public " + s
	case "public_rejected":
		s := fmt.Sprintf("public refused: %v rate-limited, %v too large, %v connections", orAny(d["rateLimited"], 0), orAny(d["tooLarge"], 0), orAny(d["connsRefused"], 0))
		if u := d["uniqueIPApprox"]; u != nil {
			s += fmt.Sprintf(" · ~%v IPs", u)
		}
		if top := d["topIPs"]; top != nil {
			s += " · top " + compact(top)
		}
		return s
	case "public_object_created":
		s := fmt.Sprintf("%v/%v created by a public request", orAny(d["class"], d["namespace"]), d["name"])
		if ip := orAny(d["ip"], d["clientIP"]); ip != nil {
			s += fmt.Sprintf(" from %v", ip)
		}
		return s
	case "expose_on":
		return fmt.Sprintf("%v public routes exposed by %s", d["workload"], orDash(actingFor))
	case "expose_off":
		return fmt.Sprintf("%v public routes unexposed by %s", d["workload"], orDash(actingFor))
	}
	return compact(d)
}

// describeApproval renders the approval audit kinds.
func describeApproval(kind string, d map[string]any) (string, bool) {
	if !strings.HasPrefix(kind, "approval_") {
		return "", false
	}
	what := fmt.Sprintf("%v/%v/%v step %v", d["workload"], d["workflow"], d["instance"], d["step"])
	who := ""
	if ap, ok := d["approver"].(map[string]any); ok {
		who = fmt.Sprint(orAny(ap["user"], ap["node"]))
		if u, _ := ap["user"].(string); u == "" {
			who = fmt.Sprintf("%v %v", ap["node"], ap["tags"])
		}
		who += fmt.Sprintf(" via %v", ap["via"])
	}
	switch kind {
	case "approval_requested":
		s := what + " waits for approval"
		if f := d["facts"]; f != nil {
			s += " " + compact(f)
		}
		return s, true
	case "approval_decision":
		s := fmt.Sprintf("%s %s by %s", what, Decided(fmt.Sprint(d["decision"])), who)
		if c := d["comment"]; c != nil {
			s += fmt.Sprintf(" %q", c)
		}
		return s, true
	case "approval_denied":
		return fmt.Sprintf("%s: %v by %s refused (%v)", what, d["decision"], who, d["code"]), true
	case "approval_timeout":
		return what + " timed out: rejected", true
	}
	return "", false
}

func compact(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// orAny returns the first non-nil value.
func orAny(vs ...any) any {
	for _, v := range vs {
		if v != nil {
			return v
		}
	}
	return nil
}

// Decided is an approval decision in the past tense ("reject" → "rejected").
func Decided(decision string) string {
	if strings.HasSuffix(decision, "e") {
		return decision + "d"
	}
	return decision + "ed"
}
