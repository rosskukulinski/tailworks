//go:build sdk

package sdk

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/hostd/mgmtapi"
)

// TestOAuthProviderKV runs @cloudflare/workers-oauth-provider unmodified
// on tailworks with its OAUTH_KV on the Workers KV binding (ADR-037):
// dynamic client registration, the authorization code flow with PKCE,
// tokens (stored with expirationTtl), refresh, a protected API call, and
// the library's own list() calls. Everything it stored is in the
// workload's KV namespace, journaled in mgmt.
func TestOAuthProviderKV(t *testing.T) {
	s := startSuite(t)
	dep := s.a.Deploy(app("oauthkv"), "oauthkv", nil)
	wl := s.a.WaitActive("oauthkv", dep, 60*time.Second)
	s.waitPeer(wl.IP)
	base := "http://" + wl.IP
	const redirect = "https://client.example/cb"
	noFollow := *s.hc
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	form := func(path string, v url.Values) (int, map[string]any) {
		t.Helper()
		code, _, b := s.do("POST", base+path, []byte(v.Encode()), "content-type", "application/x-www-form-urlencoded")
		return code, parse(b)
	}

	// 1. Dynamic client registration (a public client).
	code, _, b := s.do("POST", base+"/register", []byte(js(map[string]any{
		"redirect_uris": []string{redirect}, "client_name": "tw-e2e", "token_endpoint_auth_method": "none",
	})), "content-type", "application/json")
	reg := parse(b)
	clientID, _ := reg["client_id"].(string)
	if code != 201 || clientID == "" {
		t.Fatalf("register: %d %s", code, b)
	}

	// 2. Authorize (PKCE S256): the app approves and redirects with a code.
	verifier := strings.Repeat("v3r1f13r-", 6)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "scope": {"read"},
		"state": {"st-1"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	resp, err := noFollow.Get(base + "/authorize?" + q.Encode())
	if err != nil && !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != 302 || loc == nil || !strings.HasPrefix(loc.String(), redirect) || loc.Query().Get("state") != "st-1" || loc.Query().Get("code") == "" {
		t.Fatalf("authorize: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// 3. Token exchange.
	code, tok := form("/token", url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")},
		"redirect_uri": {redirect}, "client_id": {clientID}, "code_verifier": {verifier}})
	access, _ := tok["access_token"].(string)
	refresh, _ := tok["refresh_token"].(string)
	if code != 200 || access == "" || refresh == "" || tok["token_type"] != "bearer" {
		t.Fatalf("token: %d %v", code, tok)
	}

	// 4. The protected API sees the grant's props.
	whoami := func(token string) (int, map[string]any) {
		return s.getJSON(base+"/api/whoami", "authorization", "Bearer "+token)
	}
	if code, r := whoami(access); code != 200 || get(r, "props", "user") != "alice" || get(r, "props", "via") != "tw-kv" {
		t.Fatalf("whoami: %d %v", code, r)
	}
	if code, r := whoami("alice:nope:" + strings.Repeat("x", 32)); code != 401 {
		t.Fatalf("bad token: %d %v", code, r)
	}

	// 5. Refresh: a new access token that works.
	code, tok2 := form("/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {clientID}})
	access2, _ := tok2["access_token"].(string)
	if code != 200 || access2 == "" || access2 == access {
		t.Fatalf("refresh: %d %v", code, tok2)
	}
	if code, r := whoami(access2); code != 200 || get(r, "props", "user") != "alice" {
		t.Fatalf("whoami after refresh: %d %v", code, r)
	}

	// 6. The library's own list() calls (by prefix) and what it stored.
	if code, r := s.getJSON(base + "/listed"); code != 200 || r["clients"] != float64(1) || js(r["grants"]) != js([]string{clientID}) {
		t.Fatalf("listClients/listUserGrants: %d %v", code, r)
	}
	code, kv := s.getJSON(base + "/kv")
	if code != 200 {
		t.Fatalf("kv: %d %v", code, kv)
	}
	var clients, grants, tokens int
	for k, v := range kv {
		switch {
		case strings.HasPrefix(k, "client:"):
			clients++
		case strings.HasPrefix(k, "grant:alice:"):
			grants++
		case strings.HasPrefix(k, "token:alice:"):
			tokens++
			if x, _ := get(v.(map[string]any), "expiration").(float64); int64(x) <= time.Now().Unix() {
				t.Errorf("token %s has no expiration (expirationTtl): %v", k, v)
			}
		}
	}
	if clients != 1 || grants != 1 || tokens < 1 {
		t.Fatalf("OAUTH_KV keys: %d clients, %d grants, %d tokens: %v", clients, grants, tokens, kv)
	}

	// The data is the workload's KV namespace "oauth", journaled in mgmt.
	var p mgmtapi.StoreJournalPage
	if err := s.a.Mgmt.Do(context.Background(), "GET", "/v1/tenants/"+s.a.TenantID+"/workloads/oauthkv/stores/kv/oauth/journal", nil, &p); err != nil || p.Head == 0 {
		t.Fatalf("journal: %v %v", err, p)
	}
	t.Logf("OAUTH_KV: %d keys, journal head %d (%d entries kept)", len(kv), p.Head, len(p.Entries))

	// 7. A replayed authorization code is refused, and (RFC 6749 §4.1.2)
	// the provider revokes the grant: its KV entries are deleted, so the
	// tokens stop working.
	if code, r := form("/token", url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")},
		"redirect_uri": {redirect}, "client_id": {clientID}, "code_verifier": {verifier}}); code != 400 {
		t.Fatalf("code reuse: %d %v", code, r)
	}
	if code, r := whoami(access2); code != 401 {
		t.Fatalf("token after the grant was revoked: %d %v", code, r)
	}
	if code, kv := s.getJSON(base + "/kv"); code != 200 || len(kv) != 1 {
		t.Fatalf("OAUTH_KV after revocation: %d %v (want only the client)", code, kv)
	}
}
