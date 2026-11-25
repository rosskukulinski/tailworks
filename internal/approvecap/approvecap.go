// Package approvecap is the github.com/rosskukulinski/tailworks/cap/approve app capability: who may
// decide a Workflow approval (ADR-040, docs/approvals.md).
// hostd enforces it before it delivers a decision; mgmt uses the same rules
// to show a caller the approvals it may decide.
package approvecap

import (
	"encoding/json"
	"fmt"
	"math"
)

// Cap is the app capability. Its values (grants) may narrow it:
//
//	{"workloads": [...], "workflows": [...], "steps": [...],
//	 "decisions": ["approve", "reject"], "limits": {"<param>": <max>}}
//
// A missing field does not narrow ("*" in a list matches anything). limits
// compare against the instance's numeric params (facts); a grant with a
// limit the instance has no number for does not match (fail closed). The
// grants are a union: any one matching grant allows the decision.
const Cap = "github.com/rosskukulinski/tailworks/cap/approve"

// Target is the approval being decided.
type Target struct {
	Workload, Workflow, Step string
	Facts                    map[string]float64
}

// Grant is one value of Cap.
type Grant struct {
	Workloads []string           `json:"workloads"`
	Workflows []string           `json:"workflows"`
	Steps     []string           `json:"steps"`
	Decisions []string           `json:"decisions"`
	Limits    map[string]float64 `json:"limits"`
}

func listOK(l []string, v string) bool {
	if l == nil {
		return true
	}
	for _, x := range l {
		if x == "*" || x == v {
			return true
		}
	}
	return false
}

// Check reports whether some grant allows decision ("approve" | "reject")
// on t; if none does, why names the field each grant failed on.
func Check(grants []json.RawMessage, t Target, decision string) (ok bool, why []string) {
	for _, raw := range grants {
		var g Grant
		if err := json.Unmarshal(raw, &g); err != nil {
			why = append(why, "unreadable grant")
			continue
		}
		switch {
		case !listOK(g.Workloads, t.Workload):
			why = append(why, "workloads")
		case !listOK(g.Workflows, t.Workflow):
			why = append(why, "workflows")
		case !listOK(g.Steps, t.Step):
			why = append(why, "steps")
		case !listOK(g.Decisions, decision):
			why = append(why, "decisions")
		default:
			within := true
			for k, max := range g.Limits {
				v, has := t.Facts[k]
				if !has || math.IsNaN(v) || v > max {
					within = false
					why = append(why, "limits."+k)
					break
				}
			}
			if within {
				return true, nil
			}
		}
	}
	return false, why
}

// MayDecide reports whether any decision on t is open to grants.
func MayDecide(grants []json.RawMessage, t Target) bool {
	a, _ := Check(grants, t, "approve")
	r, _ := Check(grants, t, "reject")
	return a || r
}

// Snippet is the policy grant to add so src may decide t (dst: the
// workload's tag for its own node, tag:tw-mgmt for tw approve / the UI).
func Snippet(src string, t Target) string {
	b, _ := json.Marshal(map[string]any{"src": []string{src}, "dst": []string{"<the workload's tag>", "tag:tw-mgmt"}, "app": map[string]any{
		Cap: []map[string]any{{"workloads": []string{t.Workload}, "workflows": []string{t.Workflow}}}}})
	return string(b)
}

// Denied is the cap_missing message.
func Denied(src, decision string, t Target, nGrants int, why []string) string {
	m := fmt.Sprintf("%s may not %s %s/%s step %q: no %s grant", src, decision, t.Workload, t.Workflow, t.Step, Cap)
	if nGrants > 0 {
		m += fmt.Sprintf(" matches (%v)", why)
	}
	return m
}
