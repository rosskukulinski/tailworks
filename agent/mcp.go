package agent

import (
	"encoding/json"
	"strings"
)

// MCPRegistration is how to register tailworks' MCP server (the `tw mcp`
// stdio bridge) with one coding agent: a command to run, or JSON to put in
// the agent's config file. `tw mcp --config` prints these (issue #467).
type MCPRegistration struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Command is a shell command that registers the server ("" when the
	// agent has no such command: use Config).
	Command string `json:"command,omitempty"`
	// Config is the JSON to merge into File.
	Config string `json:"config,omitempty"`
	File   string `json:"file,omitempty"`
	// Detected: the agent looks installed on this machine.
	Detected bool `json:"detected"`
}

// MCPRegistrations returns how to register the stdio command stdio
// (["tw", "mcp", ...]) with every agent tailworks knows: the detected ones
// first, otherwise in Agents order. det is Detect's result (nil: none
// detected).
func MCPRegistrations(stdio []string, det []Detection) []MCPRegistration {
	found := map[string]bool{}
	for _, d := range det {
		found[d.ID] = d.Detected
	}
	cmd := strings.Join(stdio, " ")
	var flags []string // what follows `tw mcp` (--allow …)
	if len(stdio) > 2 {
		flags = stdio[2:]
	}
	var out []MCPRegistration
	for _, a := range Agents {
		r := MCPRegistration{ID: a.ID, Name: a.Name, Detected: found[a.ID]}
		switch a.ID {
		case "claude":
			r.Command = "claude mcp add -s user tw -- " + cmd
		case "codex":
			r.Command = "codex mcp add tw -- " + cmd
		case "gemini":
			// Gemini CLI takes the server's own flags after "--".
			r.Command = "gemini mcp add -s user tw tw mcp"
			if len(flags) > 0 {
				r.Command += " -- " + strings.Join(flags, " ")
			}
		case "cursor":
			r.File = "~/.cursor/mcp.json"
			r.Config = `{"mcpServers": {"tw": {"command": "tw", "args": ` + jsonList(stdio[1:]) + `}}}`
		case "opencode":
			r.File = "~/.config/opencode/opencode.json"
			r.Config = `{"mcp": {"tw": {"type": "local", "command": ` + jsonList(stdio) + `}}}`
		default:
			continue
		}
		out = append(out, r)
	}
	// Detected agents first; the order is otherwise Agents'.
	var first, rest []MCPRegistration
	for _, r := range out {
		if r.Detected {
			first = append(first, r)
		} else {
			rest = append(rest, r)
		}
	}
	return append(first, rest...)
}

func jsonList(xs []string) string {
	b, _ := json.Marshal(xs)
	return string(b)
}
