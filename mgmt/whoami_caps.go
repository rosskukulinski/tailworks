package mgmt

import (
	"net/http"
	"sort"
	"strings"
)

// twCaps are the github.com/rosskukulinski/tailworks/cap/* capabilities p holds toward mgmt (WhoIs
// CapMap), sorted; [] for the local operator and hosts, which hold none.
func twCaps(p Principal) []string {
	seen := map[string]bool{}
	for _, c := range p.Caps {
		if strings.HasPrefix(c, "github.com/rosskukulinski/tailworks/cap/") {
			seen[c] = true
		}
	}
	for c := range p.CapValues {
		if strings.HasPrefix(c, "github.com/rosskukulinski/tailworks/cap/") {
			seen[c] = true
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// mcpToolNames are the MCP tools p may call on /mcp without a
// Tw-MCP-Tools header, in tools/list order ([] if none).
func (s *Server) mcpToolNames(p Principal) []string {
	allowed := s.mcpAllowed(p, http.Header{})
	out := []string{}
	for _, t := range mcpTools {
		if allowed[t.Name] {
			out = append(out, t.Name)
		}
	}
	return out
}
