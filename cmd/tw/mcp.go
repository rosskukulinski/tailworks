package main

// tw mcp (issue #25, ADR-035): tailworks' MCP server for coding agents.
//
//	tw mcp [--allow kill,undeploy,deploy]   stdio MCP server: a bridge to mgmt's /mcp
//	tw mcp --config                         how to register it with an agent
//
// On a terminal (or with --json) tw mcp without --config prints the
// registration help instead of waiting on stdin (issue #466): a person who
// types `tw mcp` wants to know how to hook it up; an agent runs it with
// stdin a pipe.
//
// The bridge reads newline-delimited JSON-RPC from stdin and POSTs each
// message to mgmt's /mcp over the transport every other command uses (the
// loopback on the tailworks host, the user's own Tailscale from a `tw link`ed
// machine), so the agent needs no credentials. Who may call which tool is
// decided by mgmt (mgmt/mcp.go): on a linked machine, the user's
// github.com/rosskukulinski/tailworks/cap/mcp grant; on the tailworks host, the operator's --allow.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	twagent "github.com/rosskukulinski/tailworks/agent"
	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// mcpReadTools are the tools every MCP caller gets (mgmt/mcp.go).
var mcpReadTools = []string{"whoami", "status", "ls", "logs", "audit"}

// mcpIsTerminal says whether stdin is a terminal (then tw mcp prints the
// --config help rather than run the bridge); tests replace it.
var mcpIsTerminal = isTerminal

func cmdMCP(args []string) error {
	f := newFlags("mcp")
	allow := f.String("allow", "", "also allow these tools: deploy, kill, undeploy (comma-separated). On the tw host this is what enables them; from a linked machine your github.com/rosskukulinski/tailworks/cap/mcp grant decides, and this only narrows")
	config := f.Bool("config", false, "print how to register tailworks' MCP server with a coding agent, and exit (the default on a terminal and with --json)")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return fail("bad_request", "tw mcp takes no arguments", "usage: tw "+usageOf("mcp"))
	}
	if show, note := mcpShowConfig(*config, *f.json, mcpIsTerminal); show {
		if note != "" {
			fmt.Fprintln(os.Stderr, note)
		}
		*config = true
	}
	var tools []string
	if *allow != "" {
		known := map[string]bool{"deploy": true, "kill": true, "undeploy": true, "*": true}
		tools = append(tools, mcpReadTools...)
		for _, t := range strings.Split(*allow, ",") {
			t = strings.TrimSpace(t)
			if !known[t] {
				return fail("bad_request", "--allow: unknown tool "+t, "the tools that need --allow: deploy, kill, undeploy (or *)")
			}
			tools = append(tools, t)
		}
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	// A linked machine's user may hold only github.com/rosskukulinski/tailworks/cap/mcp, which admits
	// /mcp and not /v1/whoami: don't probe /v1 there.
	if !s.remote() {
		if err := s.requireUp(); err != nil {
			return err
		}
	}
	if *config {
		return mcpConfig(f, s, *allow)
	}
	hdr := map[string]string{"Tw-Actor": actor()}
	if len(tools) > 0 {
		hdr[mgmt.MCPToolsHeader] = strings.Join(tools, ",")
	}
	return mcpBridge(s.mgmt(), hdr, os.Stdin, os.Stdout)
}

// mcpShowConfig decides whether tw mcp prints the --config help instead of
// running the bridge, and the stderr line that says why. The bridge speaks
// JSON-RPC on stdin/stdout for an agent; a person at a terminal, or a
// --json caller, gets how to register it. terminal is only asked when it
// matters.
func mcpShowConfig(config, jsonOut bool, terminal func() bool) (bool, string) {
	switch {
	case config:
		return true, ""
	case jsonOut:
		return true, ""
	case terminal():
		return true, "tw mcp: stdin is a terminal, so here is how to register the server (your agent runs tw mcp with stdin a pipe; by hand: printf '<json-rpc>\\n' | tw mcp)"
	}
	return false, ""
}

// mcpBridge relays newline-delimited JSON-RPC between in/out and mgmt's
// /mcp. A refusal from mgmt (no grant at all, mgmt down) becomes a JSON-RPC
// error carrying the tailworks error, so the agent sees the code and the fix.
func mcpBridge(c *mgmtapi.Client, hdr map[string]string, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "parse error: " + err.Error()}})
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		var resp []byte
		err := c.DoHeaders(ctx, "POST", mgmt.MCPPath, json.RawMessage(line), &resp, hdr)
		cancel()
		if len(msg.ID) == 0 || string(msg.ID) == "null" {
			continue // a notification: nothing to answer
		}
		if err != nil {
			e := jsonError(asCLIErr(err))
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32000, "message": e.Code + ": " + e.Message, "data": e}})
			continue
		}
		if _, err := out.Write(append([]byte(strings.TrimSpace(string(resp))), '\n')); err != nil {
			return nil // the agent went away
		}
	}
	return nil
}

type mcpConfigResult struct {
	URL      string            `json:"url"`
	Stdio    []string          `json:"stdio"`
	Tools    []string          `json:"tools"`
	Commands map[string]string `json:"commands"`
	// Agents: how to register the server with each supported agent,
	// detected ones first (issue #467).
	Agents []twagent.MCPRegistration `json:"agents"`

	Grant     string `json:"grant,omitempty"`
	GrantNote string `json:"grantNote,omitempty"`
}

// mcpConfig prints how to register the server: the stdio bridge (works
// wherever tailworks works) and the URL (for agents on a tailnet machine).
func mcpConfig(f *flags, s *state, allow string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res := mcpConfigResult{Stdio: []string{"tw", "mcp"}, Commands: map[string]string{}}
	if s.remote() {
		res.URL = strings.TrimSuffix(s.Link.Endpoint, "/") + mgmt.MCPPath
	} else {
		w, err := s.whoami(ctx)
		if err != nil {
			return err
		}
		scheme := "http"
		if w.Mgmt.HTTPS {
			scheme = "https"
		}
		res.URL = scheme + "://" + w.Mgmt.DNSName + mgmt.MCPPath
	}
	if allow != "" {
		res.Stdio = append(res.Stdio, "--allow", allow)
	}
	// Who mgmt says we are, and what we may call: the whoami tool.
	var who struct {
		Result struct {
			StructuredContent struct {
				Kind       string   `json:"kind"`
				Tools      []string `json:"tools"`
				HowToGrant string   `json:"howToGrant"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	hdr := map[string]string{}
	if allow != "" {
		hdr[mgmt.MCPToolsHeader] = strings.Join(append(append([]string{}, mcpReadTools...), strings.Split(allow, ",")...), ",")
	}
	call := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "whoami", "arguments": map[string]any{}}}
	if err := s.mgmt().DoHeaders(ctx, "POST", mgmt.MCPPath, call, &who, hdr); err != nil {
		return linkErr(s.Link, err)
	}
	wc := who.Result.StructuredContent
	res.Tools = append([]string{}, wc.Tools...)
	var det []twagent.Detection
	if env, err := agentEnv(); err == nil {
		det = twagent.Detect(env)
	}
	res.Agents = twagent.MCPRegistrations(res.Stdio, det)
	for _, a := range res.Agents {
		res.Commands[a.ID] = a.Command
		if a.Command == "" {
			res.Commands[a.ID] = a.Config
		}
	}
	res.Commands["json"] = `{"mcpServers": {"tw": {"command": "tw", "args": ` + jsonList(res.Stdio[1:]) + `}}}`
	if wc.Kind != "local" && wc.HowToGrant != "" {
		res.Grant = wc.HowToGrant
		res.GrantNote = "the tools you don't have need a github.com/rosskukulinski/tailworks/cap/mcp grant naming them; add only those the agent should have"
	}
	emit(f, res, func() {
		fmt.Printf("tw MCP server · tools you may call: %s\n\n", strings.Join(res.Tools, ", "))
		fmt.Printf("register it with your agent (stdio: runs through this tw CLI, no credentials):\n")
		for _, a := range res.Agents {
			if a.Command != "" {
				fmt.Printf("  %-12s %s\n", a.Name, a.Command)
			} else {
				fmt.Printf("  %-12s in %s: %s\n", a.Name, a.File, a.Config)
			}
		}
		fmt.Printf("  %-12s %s\n\n", "other agents", res.Commands["json"])
		fmt.Printf("HTTP (an agent on a machine on your tailnet):\n  %s\n", res.URL)
		if res.Grant != "" {
			fmt.Printf("\n%s:\n  %s\n", res.GrantNote, res.Grant)
		} else if allow == "" {
			fmt.Printf("\non this host, kill, undeploy and deploy are off unless you add them: tw mcp --allow kill,undeploy\n")
		}
	})
	return nil
}

func jsonList(xs []string) string {
	b, _ := json.Marshal(xs)
	return string(b)
}
