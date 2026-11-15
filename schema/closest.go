package schema

// Closest returns the candidate nearest to k (case-insensitive edit
// distance within ⌈len/3⌉, at least 2), or "" when none is close: the same
// rule as the "did you mean" of twd.jsonc's unknown keys.
func Closest(cands []string, k string) string {
	m := make(map[string]*node, len(cands))
	for _, c := range cands {
		m[c] = nil
	}
	return closestKey(m, k)
}
