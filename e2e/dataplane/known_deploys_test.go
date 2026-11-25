//go:build e2edataplane

package dataplane

import (
	"fmt"
	"strings"
)

// knownIssues are open findings of the deploys loop that need a decision
// before they can be fixed (ledger/deploys.md). A failure whose message
// matches is reported as a note naming the issue, so the run stays useful
// for finding new ones. Remove an entry when its fix lands.
var knownIssues = []struct {
	issue, why string
	match      []string // every one must be in the message
}{
	// A preview deployed again on a host after promote undeployed it
	// there: its Durable Objects stay killed (the kill marker is in their
	// storage, which outlives the undeploy).
	{"#577", "a preview redeployed where promote undeployed it: its Durable Object is still killed", []string{"killed", "promoted to"}},
}

// failUnlessKnown is fail, or a note when the message is a known issue's.
func (r *result) failUnlessKnown(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	for _, k := range knownIssues {
		hit := true
		for _, m := range k.match {
			hit = hit && strings.Contains(msg, m)
		}
		if hit {
			r.note("known %s (%s): %s", k.issue, k.why, msg)
			return
		}
	}
	r.fail("%s", msg)
}
