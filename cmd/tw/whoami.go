package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/approvecap"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// tw whoami (issue #46): who this CLI acts as and what that identity may
// do, from mgmt's GET /v1/whoami. Over `tw link` it is this machine's
// tailnet identity (WhoIs); on the tailworks host it is the local operator. It is
// what a coding agent should run first.

// whoamiResult is `tw whoami --json` (schema/cli.schema.json $defs/whoami).
type whoamiResult struct {
	Kind      string         `json:"kind"` // user | node | host | local
	Actor     string         `json:"actor"`
	Login     string         `json:"login,omitempty"`
	NodeID    string         `json:"nodeID,omitempty"`
	Tags      []string       `json:"tags"`
	Linked    bool           `json:"linked"` // through `tw link` (else this machine's tw up)
	Transport string         `json:"transport"`
	Mgmt      whoamiMgmt     `json:"mgmt"`
	Tenant    *whoamiTenant  `json:"tenant"` // the default tenant; null if this identity can't list tenants
	Tenants   []string       `json:"tenants"`
	Caps      []string       `json:"caps"` // github.com/rosskukulinski/tailworks/cap/* held toward mgmt
	Can       whoamiCanFlags `json:"can"`

	// Console is the admin console URL when this identity may open it
	// (console.go): linked, not --tsnet.
	Console string `json:"console,omitempty"`
}

type whoamiMgmt struct {
	Endpoint string `json:"endpoint"`
	DNSName  string `json:"dnsName,omitempty"`
	Tailnet  string `json:"tailnet,omitempty"`
	HTTPS    bool   `json:"https"`
}

type whoamiTenant struct {
	Tailnet  string `json:"tailnet"`
	TenantID string `json:"tenantID"`
}

// whoamiCanFlags: what the identity may do. Approve and MCPTools are null
// when mgmt is older than this CLI and doesn't say.
type whoamiCanFlags struct {
	Admin    bool     `json:"admin"`
	Approve  *bool    `json:"approve"`
	MCPTools []string `json:"mcpTools"`

	// Tenant admin (mgmt.TenantAdminCap): the tenants and actions its
	// grants allow, and why any other value grants nothing.
	TenantAdmin        []mgmt.TenantGrant `json:"tenantAdmin"`
	InvalidTenantAdmin []string           `json:"invalidTenantAdmin,omitempty"`
}

// whoamiWire is GET /v1/whoami, with the fields newer mgmts add as
// pointers (absent from an older mgmt).
type whoamiWire struct {
	mgmt.WhoAmI
	Approver *bool     `json:"approver"`
	MCPTools *[]string `json:"mcpTools"`
}

func cmdWhoami(args []string) error {
	f := newFlags("whoami")
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return fail("bad_request", "tw whoami takes no arguments", "usage: tw "+usageOf("whoami"))
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if !s.remote() {
		if err := s.requireUp(); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var w whoamiWire
	if err := s.mgmt().DoHeaders(ctx, "GET", "/v1/whoami", nil, &w, map[string]string{"Tw-Actor": actor()}); err != nil {
		return linkErr(s.Link, err)
	}
	res := whoamiFrom(s, w)
	emit(f, res, func() { printWhoami(res) })
	return nil
}

// whoamiFrom builds the result from mgmt's answer and this CLI's state.
func whoamiFrom(s *state, w whoamiWire) whoamiResult {
	res := whoamiResult{Kind: w.Kind, Actor: w.Actor, Login: w.Login, NodeID: w.NodeID, Tags: orEmpty(w.Tags),
		Linked: s.remote(), Transport: s.transport(), Tenants: []string{}, Caps: orEmpty(w.Caps)}
	res.Mgmt = whoamiMgmt{Endpoint: mgmtEndpoint(s), DNSName: w.Mgmt.DNSName, Tailnet: w.Mgmt.Tailnet, HTTPS: w.Mgmt.HTTPS}
	res.Can.Admin = w.Admin
	res.Can.Approve = w.Approver
	if w.MCPTools != nil {
		res.Can.MCPTools = orEmpty(*w.MCPTools)
	}
	res.Can.TenantAdmin, res.Can.InvalidTenantAdmin = w.TenantGrants, w.InvalidTenantGrants
	if res.Can.TenantAdmin == nil {
		res.Can.TenantAdmin = []mgmt.TenantGrant{}
	}
	if s.remote() {
		res.Console = consoleURL(s.Link, w.Admin, len(w.TenantGrants) > 0, w.Approver != nil && *w.Approver)
	}
	if w.Admin || !s.remote() || len(w.TenantGrants) > 0 {
		for _, t := range s.tenants() {
			res.Tenants = append(res.Tenants, t.Tailnet)
		}
		if t, err := s.tenant(""); err == nil {
			res.Tenant = &whoamiTenant{Tailnet: t.Tailnet, TenantID: t.TenantID}
		}
	}
	return res
}

func orEmpty(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// mgmtEndpoint is where this CLI sends mgmt requests.
func mgmtEndpoint(s *state) string {
	switch {
	case s.remote():
		return s.Link.Endpoint
	case s.Cfg.LocalDev:
		return "unix://" + s.mgmtSock()
	}
	if lb := s.loopback(); lb != nil {
		return "http://" + lb.MgmtIP
	}
	return ""
}

func printWhoami(r whoamiResult) {
	who := r.Actor
	switch r.Kind {
	case "local":
		who = "the local operator (" + r.Actor + ")"
	case "node":
		who += " (tagged node " + strings.Join(r.Tags, ",") + ")"
	}
	via := "this machine's tw up"
	if r.Linked {
		via = "tw link (WhoIs)"
	}
	fmt.Printf("you are   %s, via %s\n", who, via)
	ep := r.Mgmt.Endpoint
	if r.Mgmt.DNSName != "" && !strings.Contains(ep, r.Mgmt.DNSName) {
		ep += " (" + r.Mgmt.DNSName + ")"
	}
	fmt.Printf("mgmt      %s · %s\n", ep, r.Transport)
	switch {
	case r.Tenant != nil:
		more := ""
		if len(r.Tenants) > 1 {
			more = fmt.Sprintf(" · %d tenants: %s", len(r.Tenants), strings.Join(r.Tenants, ", "))
		}
		fmt.Printf("tenant    %s (%s)%s\n", r.Tenant.Tailnet, r.Tenant.TenantID, more)
	case !r.Can.Admin && len(r.Can.TenantAdmin) == 0:
		fmt.Printf("tenant    (not shown: listing tenants needs admin)\n")
	default:
		fmt.Printf("tenant    none yet\n")
	}
	fmt.Printf("can       admin %s · approve %s · MCP tools %s\n", yesNo(&r.Can.Admin), yesNo(r.Can.Approve), mcpList(r.Can.MCPTools))
	if !r.Can.Admin {
		for _, g := range r.Can.TenantAdmin {
			fmt.Printf("tenant admin  %s in %s\n", strings.Join(g.Actions, ", "), strings.Join(g.Tenants, ", "))
		}
	}
	for _, bad := range r.Can.InvalidTenantAdmin {
		fmt.Printf("tenant admin  ✗ grants nothing: %s\n", bad)
	}
	if len(r.Caps) > 0 {
		fmt.Printf("caps      %s\n", strings.Join(r.Caps, ", "))
	}
	if r.Console != "" {
		only := ""
		if strings.HasSuffix(r.Console, "/ui/approvals") {
			only = " (the approvals page: the rest needs admin)"
		}
		fmt.Printf("console   %s%s\n", r.Console, only)
	}
	for _, h := range whoamiHints(r) {
		fmt.Printf("› %s\n", h)
	}
}

func yesNo(b *bool) string {
	switch {
	case b == nil:
		return "? (mgmt too old to say)"
	case *b:
		return "yes"
	}
	return "no"
}

func mcpList(ts []string) string {
	if ts == nil {
		return "? (mgmt too old to say)"
	}
	if len(ts) == 0 {
		return "none"
	}
	return strings.Join(ts, ", ")
}

// whoamiHints say how to get what this identity lacks.
func whoamiHints(r whoamiResult) []string {
	var out []string
	if !r.Can.Admin && len(r.Can.TenantAdmin) == 0 {
		out = append(out, "not a tailworks admin: deploy, ls, logs and the rest need the "+mgmt.AdminCap+" grant to "+mgmt.MgmtTag+
			" (or, for your team's tenants only, a "+mgmt.TenantAdminCap+" grant naming them: docs/teams.md)")
	}
	if r.Kind == "local" {
		out = append(out, "approve: the operator console has no personal identity; approve from a `tw link`ed machine with a "+approvecap.Cap+" grant")
		if r.Can.MCPTools != nil && !slices.Contains(r.Can.MCPTools, "deploy") {
			out = append(out, "MCP deploy, kill and undeploy are off on the host unless you add them: tw mcp --allow deploy,kill,undeploy")
		}
	} else if r.Can.MCPTools != nil && !slices.Contains(r.Can.MCPTools, "deploy") {
		out = append(out, "MCP deploy, kill and undeploy need a "+mgmt.MCPCap+" grant naming them (tw mcp --config prints it)")
	}
	return out
}
