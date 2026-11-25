package main

// tw upgrade refreshes the agent skills tw wrote (issue #490). The skills
// are embedded in tw and stamped with its version, so only the NEW binary
// can render them: after a healthy upgrade, tw upgrade runs
// `<prefix>/bin/tw agent setup --scope user --yes --refresh`. --refresh
// is hidden (tw upgrade's, not the user's): it updates only files and
// blocks that carry tailworks' marker, in any agent's user-scope location,
// and never creates or skips anything (agent/refresh.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	twagent "github.com/rosskukulinski/tailworks/agent"
)

// cutRefreshFlag removes the hidden --refresh from tw agent setup's args.
func cutRefreshFlag(args []string) ([]string, bool) {
	out, refresh := make([]string, 0, len(args)), false
	for _, a := range args {
		if a == "--refresh" || a == "-refresh" {
			refresh = true
			continue
		}
		out = append(out, a)
	}
	return out, refresh
}

// agentRefreshStep is tw upgrade's plan step for the refresh, or "" when
// no user-scope skill here carries tailworks' marker.
func agentRefreshStep() string {
	env, err := agentEnv()
	if err != nil {
		return ""
	}
	p, err := twagent.MakePlan(env, twagent.Options{Scope: twagent.ScopeUser, Agents: twagent.Agents, Version: version, Refresh: true})
	if err != nil || !p.Stamped() {
		return ""
	}
	return agentRefreshText
}

const agentRefreshText = "refresh the agent skills tw wrote in your home directory (the new tw's agent setup --refresh)"

// refreshAfterUpgrade refreshes the skills when the plan said it would.
// A failure doesn't fail the upgrade: it says how to do it by hand.
func refreshAfterUpgrade(p localPlan, prefix string, say func(string, ...any)) string {
	if !slices.Contains(p.Steps, agentRefreshText) {
		return ""
	}
	msg, err := refreshAgentSkills(prefix)
	if err != nil {
		say("! agent skills not refreshed (%v): run tw agent setup", err)
		return ""
	}
	return msg
}

// refreshAgentSkills runs the new release's tw to refresh the user-scope
// skills, and says what it did (for tw upgrade's output).
func refreshAgentSkills(prefix string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, filepath.Join(prefix, "bin", "tw"), "agent", "setup", "--scope", "user", "--yes", "--refresh", "--json").Output()
	if err != nil {
		return "", fmt.Errorf("tw agent setup --refresh: %w", err)
	}
	var res struct {
		Changes []twagent.Change `json:"changes"`
		Version string           `json:"version"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", fmt.Errorf("tw agent setup --refresh: %w", err)
	}
	n := 0
	for _, c := range res.Changes {
		if c.Action == twagent.Update || c.Action == twagent.Remove {
			n++
		}
	}
	if n == 0 {
		return fmt.Sprintf("agent skills already at %s", res.Version), nil
	}
	return fmt.Sprintf("agent skills refreshed to %s (%d files)", res.Version, n), nil
}
