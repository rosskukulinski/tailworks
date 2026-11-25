package docs

import "testing"

// TestSearchFindsFlags: a flag name finds the generated CLI reference
// (cli.md, `make cli-docs`) in the top hits.
func TestSearchFindsFlags(t *testing.T) {
	for _, q := range []string{
		"wait-public",
		"allow-missing-grants",
	} {
		found := false
		var got []string
		for _, h := range Search(q, 5) {
			found = found || h.Doc == "cli.md"
			got = append(got, h.Ref())
		}
		if !found {
			t.Errorf("search %q: want cli.md in the top 5, got %v", q, got)
		}
	}
}

// TestSearchCLIOnce: a flag several subcommands share (tw queues' --delay)
// is one cli.md hit, not one per subcommand: a subcommand with the same
// flags as an earlier section links there instead (issue #437).
func TestSearchCLIOnce(t *testing.T) {
	n := 0
	var got []string
	for _, h := range Search("delay", 10) {
		if h.Doc == "cli.md" {
			n++
		}
		got = append(got, h.Ref())
	}
	if n > 1 {
		t.Errorf("search delay: %d cli.md hits in the top 10, want at most 1: %v", n, got)
	}
}
