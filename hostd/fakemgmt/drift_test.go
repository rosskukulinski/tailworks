package fakemgmt_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/fakemgmt"
	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// hostRoutesNotFaked are the spec operations hostd may call (x-tw-role
// includes "host") that fakemgmt does not serve, each with the reason.
// One per line, sorted. Adding a host route to api/openapi.yaml without
// adding it to fakemgmt (or here) fails TestRouteDrift.
var hostRoutesNotFaked = map[string]string{
	"GET /v1/container-reservations":              "a listing for operators; hostd only reserves and releases",
	"GET /v1/hosts/{id}":                          "host detail for operators; hostd never reads it",
	"GET /v1/images/aliases":                      "read by tw deploy, not hostd",
	"GET /v1/images/public":                       "read by tw deploy, not hostd",
	"GET /v1/objects/{id}/certificate":            "hostd fetches certificates by node (GET /v1/certificates/{stableNodeID})",
	"GET /v1/releases/{version}/artifacts/{name}": "hostd self-upgrade only; make e2e-tw-upgrade runs it against the real mgmt",
	"PUT /v1/hosts/{id}/paths":                    "sent only for assignments with placementTargets, which fakemgmt never sets; make e2e-placement covers it",
}

// TestRouteDrift checks fakemgmt's route table against api/openapi.yaml
// (#108): every route fakemgmt registers is a spec operation; every
// operation a host may call is served by fakemgmt (or listed in
// hostRoutesNotFaked); and what fakemgmt answers on each of its routes (a
// probe with placeholder IDs, as a host and as an admin) has a status and
// body shape the spec declares for it.
func TestRouteDrift(t *testing.T) {
	roles, err := v1.OperationRoles()
	if err != nil {
		t.Fatal(err)
	}
	spec := map[string]string{} // normalized "METHOD /path" → spec key
	for k := range roles {
		spec[normRoute(k)] = k
	}
	fake := fakeRoutes(t)
	served := map[string]bool{}
	for _, r := range fake {
		served[normRoute(r)] = true
		if _, ok := spec[normRoute(r)]; !ok {
			t.Errorf("fakemgmt serves %s, which is not in api/openapi.yaml", r)
		}
	}
	var missing []string
	for k, role := range roles {
		if !slices.Contains(strings.Split(role, "|"), "host") {
			continue
		}
		_, listed := hostRoutesNotFaked[k]
		switch {
		case !served[normRoute(k)] && !listed:
			missing = append(missing, k)
		case served[normRoute(k)] && listed:
			t.Errorf("hostRoutesNotFaked lists %s, which fakemgmt now serves: drop it from the list", k)
		}
	}
	for k := range hostRoutesNotFaked {
		if _, ok := roles[k]; !ok {
			t.Errorf("hostRoutesNotFaked lists %s, which is not in api/openapi.yaml", k)
		}
	}
	sort.Strings(missing)
	for _, k := range missing {
		t.Errorf("spec route %s (role %s) is missing from fakemgmt: serve it there, or list it in hostRoutesNotFaked with the reason", k, roles[k])
	}

	// Status shape: probe each served route through the spec validator.
	val, err := v1.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	fm := fakemgmt.New(fakemgmt.Config{})
	for _, r := range fake {
		key, ok := spec[normRoute(r)]
		if !ok {
			continue
		}
		method, path, _ := strings.Cut(key, " ")
		for _, host := range []string{"", "h1"} {
			req := probe(method, path, host)
			route, err := val.ValidateRequest(probe(method, path, host))
			if route == nil {
				t.Errorf("%s: the validator does not route it: %v", key, err)
				continue
			}
			rec := httptest.NewRecorder()
			fm.ServeHTTP(rec, req)
			if rec.Code == 404 && strings.Contains(rec.Body.String(), "is not a /v1 route") {
				t.Errorf("%s: fakemgmt registers it but its mux does not route it", key)
				continue
			}
			if err := val.ValidateResponse(req, route, pathParams(path), rec.Code, rec.Header(), rec.Body.Bytes()); err != nil {
				t.Errorf("%s (host %q): fakemgmt's answer is not the spec's: %v", key, host, err)
			}
		}
	}
}

var paramRE = regexp.MustCompile(`\{[^}]+\}`)

// normRoute drops path parameter names ({id} and {tenantID} are one route).
func normRoute(r string) string { return paramRE.ReplaceAllString(r, "{}") }

// probe is a request for a spec operation with placeholder path
// parameters and an empty JSON object body.
func probe(method, path, host string) *http.Request {
	var body io.Reader
	if method == "POST" || method == "PUT" || method == "PATCH" {
		body = bytes.NewReader([]byte("{}"))
	}
	r := httptest.NewRequest(method, "http://mgmt"+paramRE.ReplaceAllString(path, "x"), body)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if host != "" {
		r.Header.Set("Tw-Host-ID", host)
	}
	return r
}

func pathParams(path string) map[string]string {
	m := map[string]string{}
	for _, p := range paramRE.FindAllString(path, -1) {
		m[strings.Trim(p, "{}")] = "x"
	}
	return m
}

// fakeRoutes are the "METHOD /path" patterns fakemgmt's mux registers
// (every m.HandleFunc / m.Handle with a literal pattern in the package;
// the catch-all "/" and the OCI registry's "/v2/" are not /v1 routes).
func fakeRoutes(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var out []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok || len(c.Args) != 2 {
				return true
			}
			sel, ok := c.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
				return true
			}
			lit, ok := c.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			pat, _ := strconv.Unquote(lit.Value)
			if strings.Contains(pat, " ") {
				out = append(out, pat)
			}
			return true
		})
	}
	if len(out) < 30 {
		t.Fatalf("found only %d fakemgmt routes: has New stopped registering literal patterns?", len(out))
	}
	sort.Strings(out)
	return out
}
