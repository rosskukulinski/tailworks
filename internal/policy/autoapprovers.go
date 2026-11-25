package policy

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"tailscale.com/tailcfg"
)

// AutoApprovers is the part of a policy's autoApprovers tailworks may add
// to (issue #295): autoApprovers.services, which lets a workload's tags
// host its Tailscale Service. Without it control never routes the Service
// to the workload node. routes and exitNode are never written.
type AutoApprovers struct {
	// Services maps a svc: name to the tags allowed to host it.
	Services map[string][]string `json:"services,omitempty"`
}

// checkAutoApprovers: every key is a valid svc: name and every approver
// a tailworks tag (never autogroup:*, groups, users or other tags), so the
// entry can only let tailworks' own nodes host a Service.
func checkAutoApprovers(a AutoApprovers, okTag func(string) bool) error {
	for _, name := range slices.Sorted(maps.Keys(a.Services)) {
		if !strings.HasPrefix(name, "svc:") {
			return fmt.Errorf("policy snippet: refusing autoApprovers.services key %q: tailworks only adds svc: names", name)
		}
		if err := tailcfg.ServiceName(name).Validate(); err != nil {
			return fmt.Errorf("policy snippet: refusing autoApprovers.services key %q: %v", name, err)
		}
		if len(a.Services[name]) == 0 {
			return fmt.Errorf("policy snippet: autoApprovers.services %q has no approvers", name)
		}
		for _, who := range a.Services[name] {
			if !okTag(who) {
				return fmt.Errorf("policy snippet: refusing approver %q for %q: tailworks only lets %s* tags host a Service", who, name, TwTagPrefix)
			}
		}
	}
	return nil
}

// missingOwners is want minus have, per key: a key have lacks comes whole,
// an existing one only with the values it lacks (appended by Merge;
// existing values are never removed or reordered). nil if nothing is
// missing.
func missingOwners(have, want map[string][]string) map[string][]string {
	out := map[string][]string{}
	for k, vs := range want {
		cur, ok := have[k]
		if !ok {
			out[k] = vs
			continue
		}
		var add []string
		for _, v := range vs {
			if !slices.Contains(cur, v) && !slices.Contains(add, v) {
				add = append(add, v)
			}
		}
		if len(add) > 0 {
			out[k] = add
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ownersOps are the JSON Patch ops adding miss under the object at path
// (e.g. /tagOwners): the whole object if it is absent, else each missing
// key, else each missing value appended to an existing key.
func ownersOps(find func(ptr string) bool, path string, miss map[string][]string) []map[string]any {
	if len(miss) == 0 {
		return nil
	}
	if !find(path) {
		return []map[string]any{{"op": "add", "path": path, "value": miss}}
	}
	var ops []map[string]any
	for _, k := range slices.Sorted(maps.Keys(miss)) {
		p := path + "/" + ptrEscape(k)
		if !find(p) {
			ops = append(ops, map[string]any{"op": "add", "path": p, "value": miss[k]})
			continue
		}
		for _, v := range miss[k] {
			ops = append(ops, map[string]any{"op": "add", "path": p + "/-", "value": v})
		}
	}
	return ops
}

// autoApproversOps adds the missing services entries, creating
// /autoApprovers and /autoApprovers/services when the policy has neither.
func autoApproversOps(find func(ptr string) bool, miss map[string][]string) []map[string]any {
	if len(miss) == 0 {
		return nil
	}
	if !find("/autoApprovers") {
		return []map[string]any{{"op": "add", "path": "/autoApprovers", "value": AutoApprovers{Services: miss}}}
	}
	return ownersOps(find, "/autoApprovers/services", miss)
}

// diffOwners checks that after is before plus exactly added, for a map of
// lists like tagOwners: existing keys keep their values (added ones
// appended), and new keys are exactly the snippet's.
func diffOwners(name string, before, after map[string]any, added map[string][]string) []string {
	var diff []string
	for k, bo := range before {
		ao, ok := after[k]
		if !ok {
			diff = append(diff, "- "+name+"."+k+" (removed)")
			continue
		}
		if equalJSON(bo, ao) {
			continue
		}
		// Allowed change: the snippet's values appended after the existing ones.
		bl, _ := bo.([]any)
		want := append([]any(nil), bl...)
		for _, v := range added[k] {
			want = append(want, v)
		}
		if !equalJSON(want, ao) {
			diff = append(diff, "~ "+name+"."+k+" (modified: "+compactJSON(bo)+" → "+compactJSON(ao)+")")
		}
	}
	for k, ao := range after {
		if _, ok := before[k]; ok {
			continue
		}
		want, ok := added[k]
		if !ok || !equalJSON(want, ao) {
			diff = append(diff, "+ "+name+"."+k+" = "+compactJSON(ao)+" (not in the snippet)")
		}
	}
	return diff
}

// diffAutoApprovers: only autoApprovers.services may change, and only by
// the snippet's additions; routes, exitNode and anything else stay as
// they were.
func diffAutoApprovers(before, after map[string]any, added AutoApprovers) []string {
	var diff []string
	for k, bv := range before {
		if k == "services" {
			continue
		}
		if av, ok := after[k]; !ok {
			diff = append(diff, "- autoApprovers."+k+" (removed)")
		} else if !equalJSON(bv, av) {
			diff = append(diff, "~ autoApprovers."+k+" (modified)")
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok && k != "services" {
			diff = append(diff, "+ autoApprovers."+k+" (added)")
		}
	}
	bs, _ := before["services"].(map[string]any)
	as, _ := after["services"].(map[string]any)
	return append(diff, diffOwners("autoApprovers.services", bs, as, added.Services)...)
}

func equalJSON(a, b any) bool { return reflect.DeepEqual(normalize(a), normalize(b)) }
