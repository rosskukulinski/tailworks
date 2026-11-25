package mgmt

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/obs"
)

// fakeMCPAPI is a /v1 stand-in for one tool call: one tenant, and every
// other request's URL recorded.
func fakeMCPAPI(seen *[]*url.URL) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tenants" {
			w.Write([]byte(`{"items":[{"tenantID":"t1","tailnet":"x.ts.net"}]}`))
			return
		}
		*seen = append(*seen, r.URL)
		w.Write([]byte(`{}`))
	})
}

func mcpRun(t *testing.T, tool string, args map[string]any) *url.URL {
	t.Helper()
	var seen []*url.URL
	c := &mcpCtx{ctx: context.Background(), api: fakeMCPAPI(&seen), p: Principal{Kind: "mcp"}}
	tl, ok := mcpToolByName(tool)
	if !ok {
		t.Fatalf("no tool %s", tool)
	}
	if _, err := tl.run(c, args); err != nil {
		t.Fatalf("%s %v: %v", tool, args, err)
	}
	if len(seen) != 1 {
		t.Fatalf("%s: %d /v1 calls, want 1", tool, len(seen))
	}
	return seen[0]
}

// TestMCPLogsFilterParity: every obs.Filter key (the `tw logs` record
// filters) is a property of the MCP logs tool and reaches the /logs query
// string, so the tool can't fall behind `tw logs` again.
func TestMCPLogsFilterParity(t *testing.T) {
	t.Parallel()
	var full obs.Filter
	v := reflect.ValueOf(&full).Elem()
	for i := range v.NumField() {
		switch f := v.Field(i); f.Kind() {
		case reflect.String:
			f.SetString("5xx") // valid for status, a plain string for the rest
		case reflect.Bool:
			f.SetBool(true)
		default:
			t.Fatalf("obs.Filter.%s: kind %s; extend this test", v.Type().Field(i).Name, f.Kind())
		}
	}
	keys := full.Values()
	if len(keys) != v.NumField() {
		t.Fatalf("obs.Filter.Values() has %d keys for %d fields", len(keys), v.NumField())
	}
	tl, _ := mcpToolByName("logs")
	props := tl.Input["properties"].(map[string]any)
	args := map[string]any{"workload": "w"}
	for k := range keys {
		if _, ok := props[k]; !ok {
			t.Errorf("obs.Filter key %q is not a logs tool property", k)
		}
		if k == "errors" {
			args[k] = true
		} else {
			args[k] = "5xx"
		}
	}
	q := mcpRun(t, "logs", args).Query()
	got, err := obs.ParseFilter(q)
	if err != nil {
		t.Fatal(err)
	}
	if got != full {
		t.Errorf("logs query %v parses to %+v, want %+v", q, got, full)
	}
}

// TestMCPAuditKill: audit forwards human (like tw audit --human); kill
// cascades by default (like tw kill) and cascade=false opts out.
func TestMCPAuditKill(t *testing.T) {
	t.Parallel()
	if u := mcpRun(t, "audit", map[string]any{"human": "alice@example.com"}); u.Query().Get("human") != "alice@example.com" {
		t.Errorf("audit human: %s", u)
	}
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"object": "o1", "reason": "r"}, "true"},
		{map[string]any{"object": "o1", "reason": "r", "cascade": true}, "true"},
		{map[string]any{"object": "o1", "reason": "r", "cascade": false}, ""},
	} {
		if u := mcpRun(t, "kill", tc.args); u.Query().Get("cascade") != tc.want {
			t.Errorf("kill %v: %s, want cascade=%q", tc.args, u, tc.want)
		}
	}
}
