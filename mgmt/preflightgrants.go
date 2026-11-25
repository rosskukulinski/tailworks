package mgmt

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/policy"
)

// MissingGrant is one grant that covers every src tag of the app missing
// the same (dst, ip): an app with a workload tag and an object class tag
// gets one grant to tag:db with both sources, not two.
type MissingGrant struct {
	Srcs    []string `json:"srcs"`
	Dst     string   `json:"dst"`
	IP      string   `json:"ip"`
	Snippet string   `json:"snippet"`
}

// MissingGrants groups pf.Missing by (dst, ip), in first-seen order.
func (pf PreflightResult) MissingGrants() []MissingGrant {
	var out []MissingGrant
	idx := map[string]int{}
	for _, m := range pf.Missing {
		ip := m.IP
		if ip == "" {
			ip = missingIP(m)
		}
		k := m.Dst + "\x00" + ip
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, MissingGrant{Dst: m.Dst, IP: ip})
		}
		out[i].Srcs = append(out[i].Srcs, m.Src)
	}
	for i := range out {
		g := &out[i]
		g.Snippet = policy.EntryHuJSON(grantEntry(g.Srcs, g.Dst, g.IP)) // preflightsnippet.go
	}
	return out
}

// missingIP is the port of a PreflightMissing's snippet (tcp:80 if it
// can't be read), for a result from a mgmt older than PreflightMissing.IP
// (the CLI may be newer than mgmt).
func missingIP(m PreflightMissing) string {
	var g struct {
		IP []string `json:"ip"`
	}
	if json.Unmarshal([]byte(m.Snippet), &g) == nil && len(g.IP) > 0 {
		return g.IP[0]
	}
	return "tcp:80"
}

// grantLabel is a grant as "src, src → dst ip" for a person to read.
func grantLabel(g map[string]any) string {
	join := func(k string) string {
		var parts []string
		switch v := g[k].(type) {
		case []any:
			for _, x := range v {
				parts = append(parts, fmt.Sprint(x))
			}
		case []string:
			parts = v
		}
		return strings.Join(parts, ", ")
	}
	l := join("src") + " → " + join("dst")
	if ip := join("ip"); ip != "" {
		l += " " + ip
	}
	return l
}
