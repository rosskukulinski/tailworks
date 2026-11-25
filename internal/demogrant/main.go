// demo-grant edits the two tailnet grants scripts/demo.sh needs, and nothing
// else in the policy. tw itself only ever adds to a policy (tw policy
// --apply), so the demo's "narrow one grant, then put it back" step needs its
// own writer. This is that writer; it is not part of the product.
//
//	go run ./internal/demogrant [flags] show|ensure|narrow|restore|remove
//
// The grants, recognised by their src and dst:
//
//   - the caller grant: who may call the demo agent
//     {"src": [--caller-src], "dst": [--agent-workload-tag], "ip": ["tcp:80"]}
//   - the tool grant: what the agent's objects may do on the MCP server
//     {"src": [--agent-tag], "dst": [--mcp-tag], "ip": ["tcp:80"],
//     "app": {"example.com/cap/mcp": [{"tools": [...], "limits": {...}}]}}
//
// ensure adds whichever is missing (and restores the tools if narrowed);
// narrow takes `refund` out of the tool grant's tools; restore puts it back;
// remove deletes both grants; show prints the tool grant. Every write is
// read (ETag) → edit → validated by the control plane → written with
// If-Match, retried once if the policy changed in between. Comments and
// every other rule are kept.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/headscale"
	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/tailscale/hujson"
)

// spec names the demo's two grants.
type spec struct {
	CallerSrc   string
	AgentWLTag  string
	AgentTag    string
	MCPTag      string
	Cap         string
	FullTools   []string
	NarrowTools []string
}

func (s spec) callerGrant() map[string]any {
	return map[string]any{"src": []any{s.CallerSrc}, "dst": []any{s.AgentWLTag}, "ip": []any{"tcp:80"}}
}

func (s spec) toolGrant(tools []string) map[string]any {
	t := make([]any, len(tools))
	for i, x := range tools {
		t[i] = x
	}
	return map[string]any{
		"src": []any{s.AgentTag},
		"dst": []any{s.MCPTag},
		"ip":  []any{"tcp:80"},
		"app": map[string]any{s.Cap: []any{map[string]any{
			"tools":  t,
			"limits": map[string]any{"refund": map[string]any{"maxUsd": float64(50), "perDayUsd": float64(200)}},
		}}},
	}
}

func (s spec) check() error {
	for _, t := range []string{s.AgentWLTag, s.AgentTag, s.MCPTag} {
		if !policy.IsTwTag(t) || t == "tag:tw" {
			return fmt.Errorf("%q is not a tag:tw-… tag: demo-grant only writes grants between tailworks app tags", t)
		}
	}
	if s.CallerSrc == "" || s.CallerSrc == "*" {
		return fmt.Errorf("--caller-src %q: name a user, group, tag or autogroup:member", s.CallerSrc)
	}
	return nil
}

func strSet(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// findGrant returns the index of the first grant whose src and dst are
// exactly these (-1 if none).
func findGrant(grants []any, src, dst string) int {
	for i, g := range grants {
		m, _ := g.(map[string]any)
		if reflect.DeepEqual(strSet(m["src"]), []string{src}) && reflect.DeepEqual(strSet(m["dst"]), []string{dst}) {
			return i
		}
	}
	return -1
}

func ptrEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// edit returns pol after op, and one line per change ("+ …", "- …"). It
// returns pol itself when there is nothing to do.
func edit(pol []byte, s spec, op string) ([]byte, []string, error) {
	// Standardize works in place (it blanks the comments): give it a copy.
	std, err := hujson.Standardize(append([]byte(nil), pol...))
	if err != nil {
		return nil, nil, fmt.Errorf("policy: %w", err)
	}
	var doc struct {
		Grants []any `json:"grants"`
	}
	if err := json.Unmarshal(std, &doc); err != nil {
		return nil, nil, fmt.Errorf("policy: %w", err)
	}
	ci := findGrant(doc.Grants, s.CallerSrc, s.AgentWLTag)
	ti := findGrant(doc.Grants, s.AgentTag, s.MCPTag)
	line := func(sign string, g any) string { return strings.TrimSpace(sign + " " + pretty(g)) }
	var ops []map[string]any
	var diff []string
	hasGrants := doc.Grants != nil
	add := func(g map[string]any) {
		if !hasGrants {
			ops = append(ops, map[string]any{"op": "add", "path": "/grants", "value": []any{g}})
			hasGrants = true
		} else {
			ops = append(ops, map[string]any{"op": "add", "path": "/grants/-", "value": g})
		}
		diff = append(diff, line("+", g))
	}
	// setTools rewrites only the tools list of the tool grant.
	setTools := func(tools []string) error {
		cur := doc.Grants[ti].(map[string]any)
		app, _ := cur["app"].(map[string]any)
		caps, _ := app[s.Cap].([]any)
		if len(caps) != 1 {
			return fmt.Errorf("the grant %s → %s exists but does not look like the demo's (want one %s entry); edit it by hand, or remove it and run ensure", s.AgentTag, s.MCPTag, s.Cap)
		}
		c0, _ := caps[0].(map[string]any)
		if reflect.DeepEqual(strSet(c0["tools"]), strSet(s.toolGrant(tools)["app"].(map[string]any)[s.Cap].([]any)[0].(map[string]any)["tools"])) {
			return nil
		}
		ops = append(ops, map[string]any{"op": "add", "path": fmt.Sprintf("/grants/%d/app/%s/0/tools", ti, ptrEscape(s.Cap)), "value": tools})
		diff = append(diff, line("-", cur), line("+", s.toolGrant(tools)))
		return nil
	}
	switch op {
	case "ensure":
		if ci < 0 {
			add(s.callerGrant())
		}
		if ti < 0 {
			add(s.toolGrant(s.FullTools))
		} else if err := setTools(s.FullTools); err != nil {
			return nil, nil, err
		}
	case "narrow", "restore":
		if ti < 0 {
			return nil, nil, fmt.Errorf("the policy has no grant %s → %s: run ensure first", s.AgentTag, s.MCPTag)
		}
		tools := s.FullTools
		if op == "narrow" {
			tools = s.NarrowTools
		}
		if err := setTools(tools); err != nil {
			return nil, nil, err
		}
	case "remove":
		// Only what ensure wrote: the caller grant exactly, and the tool
		// grant in its full or narrowed form. Higher index first, so the
		// other index stays valid.
		type rm struct {
			i    int
			want []map[string]any
		}
		rms := []rm{{ci, []map[string]any{s.callerGrant()}}, {ti, []map[string]any{s.toolGrant(s.FullTools), s.toolGrant(s.NarrowTools)}}}
		sort.Slice(rms, func(a, b int) bool { return rms[a].i > rms[b].i })
		for _, r := range rms {
			if r.i < 0 {
				continue
			}
			ok := false
			for _, w := range r.want {
				ok = ok || sameJSON(doc.Grants[r.i], w)
			}
			if !ok {
				return nil, nil, fmt.Errorf("grant %d (%s) was changed since the demo wrote it; remove it by hand", r.i, line("", doc.Grants[r.i]))
			}
			ops = append(ops, map[string]any{"op": "remove", "path": fmt.Sprintf("/grants/%d", r.i)})
			diff = append(diff, line("-", doc.Grants[r.i]))
		}
	default:
		return nil, nil, fmt.Errorf("unknown command %q", op)
	}
	if len(ops) == 0 {
		return pol, nil, nil
	}
	v, err := hujson.Parse(pol)
	if err != nil {
		return nil, nil, fmt.Errorf("policy: %w", err)
	}
	pb, _ := json.Marshal(ops)
	if err := v.Patch(pb); err != nil {
		return nil, nil, fmt.Errorf("policy patch: %w", err)
	}
	v.Format() // as tw policy --apply does
	return v.Pack(), diff, nil
}

// pretty renders a grant on one line with src and dst first, as people
// write them (encoding/json would sort the keys).
func pretty(g any) string {
	m, ok := g.(map[string]any)
	if !ok {
		b, _ := json.Marshal(g)
		return string(b)
	}
	var parts []string
	seen := map[string]bool{}
	put := func(k string) {
		if v, ok := m[k]; ok && !seen[k] {
			seen[k] = true
			b, _ := json.Marshal(v)
			parts = append(parts, fmt.Sprintf("%q: %s", k, b))
		}
	}
	for _, k := range []string{"src", "dst", "ip", "app"} {
		put(k)
	}
	rest := make([]string, 0, len(m))
	for k := range m {
		rest = append(rest, k)
	}
	sort.Strings(rest)
	for _, k := range rest {
		put(k)
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func sameJSON(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	var x, y any
	json.Unmarshal(ab, &x)
	json.Unmarshal(bb, &y)
	return reflect.DeepEqual(x, y)
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "demo-grant:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("demo-grant", flag.ContinueOnError)
	control := fs.String("control", "tailscale", "tailscale or headscale")
	controlURL := fs.String("control-url", "", "headscale: the server URL")
	tailnet := fs.String("tailnet", "", "the tailnet whose policy to edit (required; headscale: its base domain)")
	apiKeyFile := fs.String("api-key-file", "", "0600 file with TS_API_KEY=… (tailscale) or HEADSCALE_API_KEY=… (headscale)")
	oauthFile := fs.String("oauth-client-file", "", "tailscale: 0600 file with TS_OAUTH_CLIENT_SECRET=… (needs Policy File write)")
	tsapiURL := fs.String("tsapi-url", "", "tailscale: API base URL (testing)")
	dry := fs.Bool("dry-run", false, "print the change and write nothing")
	var s spec
	fs.StringVar(&s.CallerSrc, "caller-src", "autogroup:member", "who may call the demo agent")
	fs.StringVar(&s.AgentWLTag, "agent-workload-tag", "", "the demo agent's workload tag")
	fs.StringVar(&s.AgentTag, "agent-tag", "", "the demo agent objects' tag")
	fs.StringVar(&s.MCPTag, "mcp-tag", "", "the demo MCP server's workload tag")
	fs.StringVar(&s.Cap, "cap", "example.com/cap/mcp", "the MCP server's capability name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s.FullTools, s.NarrowTools = []string{"lookup_order", "refund"}, []string{"lookup_order"}
	if fs.NArg() != 1 {
		return errors.New("usage: demo-grant [flags] show|ensure|narrow|restore|remove")
	}
	op := fs.Arg(0)
	if err := s.check(); err != nil {
		return err
	}
	if *tailnet == "" {
		return errors.New("--tailnet is required")
	}
	var api policy.API
	switch *control {
	case "headscale":
		if *controlURL == "" || *apiKeyFile == "" {
			return errors.New("--control=headscale needs --control-url and --api-key-file")
		}
		key, err := tsapi.LoadSecretEnvFile(*apiKeyFile, headscale.APIKeyEnv)
		if err != nil {
			return err
		}
		api = &headscale.Client{BaseURL: *controlURL, APIKey: key, BaseDomain: *tailnet}
	case "tailscale":
		c := &tsapi.Client{BaseURL: *tsapiURL, Tailnet: *tailnet}
		switch {
		case *oauthFile != "":
			f := *oauthFile
			c.Tokens = &tsapi.OAuthSource{BaseURL: *tsapiURL, Load: func() (tsapi.OAuthCredentials, error) { return tsapi.LoadOAuthFile(f) }}
		case *apiKeyFile != "":
			key, err := tsapi.LoadSecretEnvFile(*apiKeyFile, "TS_API_KEY")
			if err != nil {
				return err
			}
			c.APIKey = key
		default:
			return errors.New("--control=tailscale needs --oauth-client-file or --api-key-file")
		}
		api = c
	default:
		return fmt.Errorf("--control %q: want tailscale or headscale", *control)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if op == "show" {
		cur, _, err := api.GetPolicyHuJSON(ctx)
		if err != nil {
			return fmt.Errorf("read policy: %w", err)
		}
		std, err := hujson.Standardize(cur)
		if err != nil {
			return err
		}
		var doc struct {
			Grants []any `json:"grants"`
		}
		if err := json.Unmarshal(std, &doc); err != nil {
			return err
		}
		i := findGrant(doc.Grants, s.AgentTag, s.MCPTag)
		if i < 0 {
			return fmt.Errorf("the policy has no grant %s → %s", s.AgentTag, s.MCPTag)
		}
		fmt.Println(pretty(doc.Grants[i]))
		return nil
	}

	for attempt := 0; ; attempt++ {
		cur, etag, err := api.GetPolicyHuJSON(ctx)
		if err != nil {
			return fmt.Errorf("read policy: %w", err)
		}
		next, diff, err := edit(cur, s, op)
		if err != nil {
			return err
		}
		if len(diff) == 0 {
			fmt.Println("nothing to change: the policy already has it")
			return nil
		}
		for _, l := range diff {
			fmt.Println(l)
		}
		if *dry {
			fmt.Println("dry run: nothing written")
			return nil
		}
		if err := api.ValidatePolicy(ctx, next); err != nil {
			return fmt.Errorf("the control plane refused the edited policy (nothing written): %w", err)
		}
		if _, err = api.SetPolicy(ctx, next, etag); err == nil {
			fmt.Printf("policy written to %s (validated, If-Match)\n", *tailnet)
			return nil
		}
		if !policy.IsPreconditionFailed(err) || attempt > 0 {
			return fmt.Errorf("write policy: %w", err)
		}
	}
}
