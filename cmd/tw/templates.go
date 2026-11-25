package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// The tw init templates (js/packages/create-tailworks/templates). Four are
// starting points, shown in help and the README; the rest are demos,
// patterns and other runtimes, listed by `tw init --list` and linked from
// their guides. Every name stays accepted by --template.
// TestInitTemplatesTable keeps this table equal to the templates directory.
type initTemplate struct {
	Name          string `json:"name"`
	Purpose       string `json:"purpose"`
	Needs         string `json:"needs,omitempty"`
	Guide         string `json:"guide,omitempty"`
	StartingPoint bool   `json:"starting_point"`
}

var initTemplates = []initTemplate{
	{
		Name:          "hello",
		Purpose:       "a Worker that answers with its verified caller (the default)",
		StartingPoint: true,
	},
	{
		Name:          "agent",
		Purpose:       "an Agents SDK Agent: each instance a Durable Object with its own node",
		StartingPoint: true,
	},
	{
		Name:          "mcp",
		Purpose:       "an MCP server whose tools are allowed per caller by a tailnet grant",
		StartingPoint: true,
	},
	{
		Name:          "static-site",
		Purpose:       "files in public/ served next to a Worker (wrangler assets)",
		StartingPoint: true,
	},
	{
		Name:    "ai-agent",
		Purpose: "an AI SDK agent that calls an MCP server's tools over the tailnet",
		Needs:   "the mcp template deployed as workload \"mcp\" and a grant",
		Guide:   "its README.md",
	},
	{
		Name:    "refund-approval",
		Purpose: "a Workflow that waits for a person's approval",
		Guide:   "docs/approvals.md",
	},
	{
		Name:    "lock",
		Purpose: "a lease-based lock and leader election with fencing tokens",
		Guide:   "docs/locks.md",
	},
	{
		Name:    "node-workflow",
		Purpose: "a Vercel Workflow on the Node.js process tier",
		Needs:   "Node.js ≥ 24 and npm install",
		Guide:   "docs/node-tier.md",
	},
	{
		Name:    "python",
		Purpose: "a Python Worker with a Python Durable Object",
		Guide:   "docs/python.md",
	},
}

// startingPoints is the template list help and usage lines show.
func startingPoints(sep string) string {
	var names []string
	for _, t := range initTemplates {
		if t.StartingPoint {
			names = append(names, t.Name)
		}
	}
	return strings.Join(names, sep)
}

// templatesFooter is the templates line of `tw help` and `tw help --all`.
func templatesFooter() string {
	return "templates (tw init --template): " + strings.Replace(startingPoints(" | "), "hello", "hello (default)", 1) + "   (tw init --list: every template)"
}

// cmdInitList is `tw init --list`.
func cmdInitList(f *flags) error {
	emit(f, map[string]any{"templates": initTemplates}, func() {
		for _, sec := range []struct {
			title string
			start bool
		}{{"Starting points", true}, {"Demos, patterns and other runtimes", false}} {
			fmt.Printf("%s:\n", sec.title)
			for _, t := range initTemplates {
				if t.StartingPoint != sec.start {
					continue
				}
				line := t.Purpose
				if t.Needs != "" {
					line += "; needs " + t.Needs
				}
				if t.Guide != "" {
					line += " (" + t.Guide + ")"
				}
				fmt.Printf("  %-16s %s\n", t.Name, line)
			}
			fmt.Println()
		}
		fmt.Println("tw init <dir> --template <name>")
	})
	return nil
}

// printInitNext is the next-step hint after tw init: try it locally, then
// deploy (writing the policy lines it needs, after asking).
func printInitNext(dir, tmpl string) {
	cd := "cd " + dir + " && "
	if tmpl == "node-workflow" {
		// The Node tier bundles the app's own dependencies with the
		// machine's Node.js; nothing is vendored for it.
		cd += "npm install && "
	}
	fmt.Printf("  %-40s # try it here, no tailnet\n", cd+"tw dev")
	fmt.Printf("  %-40s # shows the policy lines, asks, writes, deploys\n", "tw deploy --apply-policy")
	switch tmpl {
	case "node-workflow":
		fmt.Printf("  needs Node.js ≥ 24 and npm on this machine; `npm install` fetches the template's dependencies, so this one can't be built or deployed offline\n")
	case "ai-agent":
		// Its tools are a second app's; on its own it only refuses.
		fmt.Printf("  its tools need the mcp template deployed as workload \"mcp\" and a grant: see %s\n", filepath.Join(dir, "README.md"))
	}
}
