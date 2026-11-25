package policy

import "slices"

// AllowsAll reports whether the policy already lets every device reach
// every port on every other device: the allow-all rule a new tailnet's
// default policy starts with, as a grant ({"src": ["*"], "dst": ["*"],
// "ip": ["*"]}) or a legacy ACL ({"action": "accept", "src": ["*"],
// "dst": ["*:*"]}). On such a tailnet, network-only grants between
// tailworks' tags add nothing; app-capability grants are still needed
// (issue #389).
//
// Only an unconditional rule counts: one with a posture, via or any field
// beyond these is not treated as allow-all. A policy that can't be parsed
// is not allow-all.
func AllowsAll(pol []byte) bool {
	var d struct {
		ACLs   []map[string]any `json:"acls"`
		Grants []map[string]any `json:"grants"`
	}
	if parseStd(pol, &d) != nil {
		return false
	}
	for _, g := range d.Grants {
		if onlyKeys(g, "src", "dst", "ip") && hasWildcard(g["src"], "*") && hasWildcard(g["dst"], "*") && hasWildcard(g["ip"], "*") {
			return true
		}
	}
	for _, a := range d.ACLs {
		if onlyKeys(a, "action", "src", "dst") && a["action"] == "accept" &&
			hasWildcard(a["src"], "*") && hasWildcard(a["dst"], "*:*") {
			return true
		}
	}
	return false
}

func onlyKeys(m map[string]any, keys ...string) bool {
	for k := range m {
		if !slices.Contains(keys, k) {
			return false
		}
	}
	return true
}

func hasWildcard(v any, w string) bool {
	return slices.Contains(strs(v), w)
}
