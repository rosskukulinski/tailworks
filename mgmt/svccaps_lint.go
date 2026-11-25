package mgmt

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
)

// serviceCapWarnings (H22, #155): app capabilities granted to the workload's
// tags don't apply to calls through its Tailscale Service. The workload sees
// the caps of grants whose dst is the svc: name on a VIP flow (WhoIs is
// scoped to the address the caller dialed, tssvc.WhoIs), so a caller whose
// caps are only on a tag grant gets an empty caps() through the Service.
// One warning per (src, cap) that a tag grant has and no svc: grant does,
// with the grant to add. Never fails the preflight: the caps are the
// operator's own policy, and calls to the node's own name still get them.
func serviceCapWarnings(pol *Policy, service string, wlTags []string) []string {
	if pol == nil || service == "" || len(wlTags) == 0 {
		return nil
	}
	type key struct{ src, cap string }
	onTag := map[key][]json.RawMessage{}
	onSvc := map[key]bool{}
	for _, g := range pol.Grants {
		if len(g.App) == 0 {
			continue
		}
		toSvc := slices.Contains(g.Dst, service)
		toTag := false
		for _, t := range wlTags {
			if slices.Contains(g.Dst, t) {
				toTag = true
			}
		}
		for _, src := range g.Src {
			for c, vals := range g.App {
				k := key{src, c}
				if toSvc {
					onSvc[k] = true
				}
				if toTag {
					onTag[k] = append(onTag[k], vals...)
				}
			}
		}
	}
	keys := make([]key, 0, len(onTag))
	for k := range onTag {
		if !onSvc[k] && !onSvc[key{"*", k.cap}] {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].src != keys[j].src {
			return keys[i].src < keys[j].src
		}
		return keys[i].cap < keys[j].cap
	})
	var out []string
	for _, k := range keys {
		g, _ := json.Marshal(map[string]any{"src": []string{k.src}, "dst": []string{service}, "ip": []string{"tcp:80"},
			"app": map[string][]json.RawMessage{k.cap: onTag[k]}})
		out = append(out, fmt.Sprintf("%s has %s on the workload tags but not on %s, so calls through the Service won't see it in caps(); add to \"grants\": %s",
			k.src, k.cap, service, g))
	}
	return out
}
