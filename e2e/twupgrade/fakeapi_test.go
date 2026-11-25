//go:build e2etwupgrade

package twupgrade

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// appTag is the fixture app's workload tag.
const appTag = "tag:tw-wl-upg"

// fakeAPI is the subset of the Tailscale API `tw up` and mgmt use (the
// fake OAuth client, single-use keys, the policy, devices), plus GitHub's
// "latest release" for `tw version --check` (TW_RELEASE_API), which reports
// the version the running tw up's mgmt answers with.
type fakeAPI struct {
	mu     sync.Mutex
	policy []byte
	n      int
	keys   map[string]bool // id → revoked
	devDel map[string]bool
	tokens map[string]bool
	latest string // the "latest release" tag
}

func newFakeAPI(t *testing.T, latest string) *httptest.Server {
	t.Helper()
	base := `{"tagOwners": {"tag:tw": ["autogroup:admin"]}}`
	p, err := policy.MergeWith([]byte(base), policy.InfraSnippet(7474), policy.Options{})
	if err != nil {
		t.Fatal(err)
	}
	p, err = policy.MergeWith(p, policy.Snippet{
		TagOwners: map[string][]string{appTag: {"autogroup:admin", "tag:tw"}},
		Grants:    []map[string]any{policy.AdminGrant("autogroup:admin", mgmt.MgmtTag, mgmt.AdminCap)},
	}, policy.Options{AllowTags: []string{appTag}, AllowSrcs: []string{"autogroup:admin"}})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAPI{policy: p, keys: map[string]bool{}, devDel: map[string]bool{}, tokens: map[string]bool{}, latest: latest}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	if strings.HasSuffix(p, "/releases/latest") {
		json.NewEncoder(w).Encode(map[string]string{"tag_name": f.latest, "html_url": "https://example.invalid/" + f.latest})
		return
	}
	if p == "/api/v2/oauth/token" {
		r.ParseForm()
		if r.Form.Get("client_id") != cliflow.FakeOAuthID || r.Form.Get("client_secret") != cliflow.FakeOAuthSecret {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		tok := fmt.Sprintf("oauth-access-%d", len(f.tokens)+1)
		f.tokens[tok] = true
		json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "expires_in": 3600})
		return
	}
	if !f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")] {
		w.WriteHeader(401)
		return
	}
	switch {
	case r.Method == "GET" && strings.HasSuffix(p, "/devices"):
		w.WriteHeader(403) // no devices:core read: tw uses --tailnet
	case r.Method == "GET" && strings.HasSuffix(p, "/acl"):
		w.Header().Set("ETag", `"e1"`)
		w.Write(f.policy)
	case r.Method == "POST" && strings.HasSuffix(p, "/acl/validate"):
		w.Write([]byte(`{}`))
	case r.Method == "POST" && strings.HasSuffix(p, "/acl"):
		w.WriteHeader(500) // tw never writes the policy without --apply-policy
	case r.Method == "POST" && strings.HasSuffix(p, "/keys"):
		f.n++
		id := fmt.Sprintf("k%d", f.n)
		f.keys[id] = false
		json.NewEncoder(w).Encode(map[string]string{"id": id, "key": nettest.DummyKey})
	case strings.Contains(p, "/keys/"):
		id := p[strings.LastIndex(p, "/")+1:]
		revoked, ok := f.keys[id]
		switch {
		case !ok || (r.Method == "GET" && revoked):
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"not found"}`))
		case r.Method == "DELETE":
			f.keys[id] = true
		default:
			json.NewEncoder(w).Encode(map[string]any{"id": id, "invalid": true}) // consumed by the join
		}
	case strings.HasPrefix(p, "/api/v2/device/"):
		id := strings.TrimPrefix(p, "/api/v2/device/")
		if r.Method == "DELETE" {
			f.devDel[id] = true
			return
		}
		if f.devDel[id] {
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"not found"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": id, "nodeId": id})
	default:
		w.WriteHeader(404)
	}
}
