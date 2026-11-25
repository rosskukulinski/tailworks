package mgmt

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHeadscale is the slice of Headscale's /api/v1 that the profile uses.
type fakeHeadscale struct {
	srv *httptest.Server

	mu      sync.Mutex
	keys    int
	tags    [][]string
	expired []string
	deleted []string
}

func newFakeHeadscale(t *testing.T) *fakeHeadscale {
	f := &fakeHeadscale{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer hskey-api-mgmt" {
			w.WriteHeader(401)
			io.WriteString(w, `{"code":16,"message":"Unauthorized"}`)
			return
		}
		var body struct {
			ID      string   `json:"id"`
			AclTags []string `json:"aclTags"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/preauthkey":
			f.keys++
			f.tags = append(f.tags, body.AclTags)
			json.NewEncoder(w).Encode(map[string]any{"preAuthKey": map[string]any{"id": strconv.Itoa(f.keys), "key": "hskey-auth-" + strconv.Itoa(f.keys), "aclTags": body.AclTags}})
		case r.Method == "POST" && r.URL.Path == "/api/v1/preauthkey/expire":
			f.expired = append(f.expired, body.ID)
			io.WriteString(w, `{}`)
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v1/node/"):
			f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, "/api/v1/node/"))
			io.WriteString(w, `{}`)
		case r.Method == "GET" && r.URL.Path == "/api/v1/policy":
			json.NewEncoder(w).Encode(map[string]any{"policy": `{
  // Headscale profile: no autogroup:admin, no postures, no svc:
  "tagOwners": {"tag:tw": [], "tag:tw-agent-support": ["tag:tw"]},
  "grants": [],
}`})
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"code":5,"message":"Not Found"}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHeadscale) snapshot() (keys int, deleted []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keys, append([]string(nil), f.deleted...)
}

// TestHeadscaleProfileTenant: a mintMode "headscale" tenant mints object
// keys as tagged Headscale pre-auth keys through its own API key at its
// controlURL, kill Tier 2 deletes the Headscale node, and what the profile
// can't serve (Services, Funnel) is refused with profile_unsupported.
func TestHeadscaleProfileTenant(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	e.m.cfg.DeviceDeleteDelay = 0
	hs := newFakeHeadscale(t)
	c, h1 := e.c, e.asHost("h1")
	keyFile := filepath.Join(filepath.Dir(e.keyFile), "headscale.env")
	os.WriteFile(keyFile, []byte("HEADSCALE_API_KEY=hskey-api-mgmt\n"), 0o600)

	base := map[string]any{"tailnet": "hs.example.test", "mintMode": "headscale", "apiKeyFile": keyFile, "controlURL": hs.srv.URL}
	without := func(k string) map[string]any {
		m := map[string]any{}
		for kk, v := range base {
			if kk != k {
				m[kk] = v
			}
		}
		return m
	}
	c.fail("POST", "/v1/tenants", without("controlURL"), 400, "bad_request")
	c.fail("POST", "/v1/tenants", without("apiKeyFile"), 400, "bad_request")
	pub := without("")
	pub["publicIngress"] = "allow"
	c.fail("POST", "/v1/tenants", pub, 400, "profile_unsupported")
	var tn Tenant
	c.ok("POST", "/v1/tenants", base, &tn)
	if tn.MintMode != MintHeadscale || tn.ControlURL != hs.srv.URL {
		t.Fatalf("tenant %+v", tn)
	}
	c.fail("PATCH", "/v1/tenants/"+tn.ID, map[string]any{"publicIngress": "allow"}, 400, "profile_unsupported")

	h1.ok("PUT", "/v1/hosts/h1", map[string]any{"capacityNodes": 100, "tiers": []string{"process"}}, nil)
	// tailnet.service needs Tailscale Services: refused before any side effect.
	svc := makeBundle(t, map[string]string{"tailworks.jsonc": `{ "tailnet": { "tags": ["tag:tw-web"], "service": true } }`, "dist/index.js": ""})
	eb := c.fail("POST", "/v1/deployments", map[string]any{"tenantID": tn.ID, "workload": "web", "bundle": svc}, 400, "profile_unsupported")
	if !strings.Contains(eb["message"].(string), "MagicDNS") {
		t.Errorf("service refusal doesn't say where the workload is: %v", eb["message"])
	}
	c.fail("POST", "/v1/deployments?dryRun=true", map[string]any{"tenantID": tn.ID, "workload": "web", "bundle": svc}, 400, "profile_unsupported")
	bundle := makeBundle(t, map[string]string{"tailworks.jsonc": testTwJSONC, "dist/index.js": "export default {}"})
	// The preflight reads the Headscale policy; its tagOwners snippet has no
	// autogroup:admin (Headscale rejects it).
	var pf PreflightResult
	c.ok("POST", "/v1/deployments?dryRun=true", map[string]any{"tenantID": tn.ID, "workload": "support", "bundle": bundle}, &pf)
	if len(pf.MissingTagOwners) == 0 || slices.Contains(pf.MissingTagOwners, "tag:tw-agent-support") || !strings.Contains(pf.TagOwnersSnippet, `["tag:tw"]`) || strings.Contains(pf.TagOwnersSnippet, "autogroup") {
		t.Fatalf("preflight %+v", pf)
	}

	c.ok("POST", "/v1/deployments", map[string]any{"tenantID": tn.ID, "workload": "support", "bundle": bundle}, nil)
	var obj CreateObjectResponse
	h1.ok("POST", "/v1/objects", map[string]any{"tenantID": tn.ID, "namespace": "SupportAgent", "objectName": "alice"}, &obj)
	var cred Credential
	h1.ok("POST", "/v1/objects/"+obj.ObjectID+"/credentials?epoch=1", nil, &cred)
	if !strings.HasPrefix(cred.AuthKey, "hskey-auth-") {
		t.Fatalf("credential %+v: want a Headscale pre-auth key", cred)
	}
	hs.mu.Lock()
	gotTags := hs.tags[len(hs.tags)-1]
	hs.mu.Unlock()
	if len(gotTags) != 1 || gotTags[0] != "tag:tw-agent-support" {
		t.Fatalf("minted with tags %v", gotTags)
	}
	// A Headscale node's StableNodeID is its decimal node ID.
	h1.ok("PATCH", "/v1/objects/"+obj.ObjectID, map[string]any{"status": "ready", "epoch": 1, "nodeID": "12", "stableNodeID": "12", "magicDNS": obj.ObjectID + ".hs.example.test"}, nil)
	c.ok("DELETE", "/v1/objects/"+obj.ObjectID+killQuery(false, "test"), nil, nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, del := hs.snapshot(); len(del) == 1 {
			if del[0] != "12" {
				t.Fatalf("deleted node %v, want 12", del)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("kill Tier 2 never deleted the Headscale node")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
