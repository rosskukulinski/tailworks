package policy

import "testing"

// TestAllowsAll (issue #389): only an unconditional allow-all grant or ACL
// counts.
func TestAllowsAll(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		pol  string
		want bool
	}{
		{"default grant", `{"grants": [{"src": ["*"], "dst": ["*"], "ip": ["*"]}], // comment
}`, true},
		{"legacy acl", `{"acls": [{"action": "accept", "src": ["*"], "dst": ["*:*"]}]}`, true},
		{"empty", ``, false},
		{"no rules", `{"tagOwners": {"tag:tw": ["autogroup:admin"]}}`, false},
		{"narrow grant", `{"grants": [{"src": ["group:eng"], "dst": ["*"], "ip": ["*"]}]}`, false},
		{"port-limited", `{"grants": [{"src": ["*"], "dst": ["*"], "ip": ["tcp:443"]}]}`, false},
		{"posture", `{"grants": [{"src": ["*"], "dst": ["*"], "ip": ["*"], "srcPosture": ["posture:x"]}]}`, false},
		{"acl port-limited", `{"acls": [{"action": "accept", "src": ["*"], "dst": ["*:443"]}]}`, false},
		{"acl proto", `{"acls": [{"action": "accept", "src": ["*"], "dst": ["*:*"], "proto": "tcp"}]}`, false},
		{"unparseable", `{"grants": [`, false},
	} {
		if got := AllowsAll([]byte(c.pol)); got != c.want {
			t.Errorf("%s: AllowsAll = %v, want %v", c.name, got, c.want)
		}
	}
}
