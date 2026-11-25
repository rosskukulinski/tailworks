//go:build sdk

package sdk

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
)

// TestAgentWorkflowCallsBack (#359): an Agents SDK Agent starts an
// AgentWorkflow with runWorkflow, and the Workflow calls back into its Agent
// (reportProgress → onWorkflowProgress, an RPC from a step,
// reportComplete), on the real mgmt as well as fakemgmt. The Agent classes
// have no node of their own: TaskAgent isn't in tailnet.objects (what
// e2e/compat deploys Cloudflare's agents examples with), and
// InternalTaskAgent is "node": false.
//
// Deployed, the Workflow stayed queued: on the real mgmt, which declares only
// tailnet.objects, hostd's creates of TaskAgent objects were refused, so the
// Workflow's getAgentByName was refused the Agent (public_create_denied).
// fakemgmt (tw dev) declares every class, so only a real-mgmt run sees it.
func TestAgentWorkflowCallsBack(t *testing.T) {
	for _, backend := range hostdtest.Backends {
		t.Run(backend, func(t *testing.T) {
			s := startSuiteOn(t, backend)
			dep := s.a.Deploy(app("agentwf"), "agentwf", nil)
			wl := s.a.WaitActive("agentwf", dep, 90*time.Second)
			s.waitPeer(wl.IP)
			base := "http://" + wl.IP
			for _, kebab := range []string{"task-agent", "internal-task-agent"} {
				code, r := s.getJSON(base + "/agents/" + kebab + "/a1/start")
				id, _ := r["id"].(string)
				if code != 200 || id == "" {
					t.Fatalf("%s: runWorkflow: %d %v", kebab, code, r)
				}
				var log []string
				eventually(t, 45*time.Second, kebab+": the Workflow's callbacks reach its Agent", func() (bool, any) {
					_, r := s.getJSON(base + "/agents/" + kebab + "/a1/log?id=" + id)
					log = log[:0]
					for _, v := range asSlice(r["log"]) {
						log = append(log, fmt.Sprint(v))
					}
					return slices.Contains(log, "complete"), r
				})
				if !slices.Contains(log, "validate") || !slices.ContainsFunc(log, func(s string) bool { return strings.HasPrefix(s, "progress ") }) {
					t.Fatalf("%s: log %v (want progress, validate and complete)", kebab, log)
				}
			}
		})
	}
}

func asSlice(v any) []any { s, _ := v.([]any); return s }
