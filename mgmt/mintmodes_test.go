package mgmt

import (
	"context"
	"os"
	"testing"
	"time"
)

// envOAuthClient is the OAuth client every testEnv registers with its fake
// Tailscale API (all the scopes mgmt uses) and writes to testEnv.keyFile.
const envOAuthClient = "kTestEnvClient"

var envOAuthScopes = []string{"auth_keys", "devices:core", "policy_file:read", "services"}

// writeTestOAuthFile writes a well-formed 0600 OAuth client file at path
// (client envOAuthClient), for tests whose broker never reaches an API.
func writeTestOAuthFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("TS_OAUTH_CLIENT_SECRET="+fakeClientSecret(envOAuthClient)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRemovedMintModes: mgmt refuses to create a tenant in, or switch one
// to, a removed mint mode (apikey, wif) with mint_mode_removed; a tenant an
// older tailworks stored in one fails to mint with the same code, stays
// patchable, and mints again once switched to oauth.
func TestRemovedMintModes(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	c := e.c
	for _, body := range []map[string]any{
		{"tailnet": "a.ts.net", "mintMode": "apikey", "apiKeyFile": e.keyFile},
		{"tailnet": "a.ts.net", "mintMode": "wif"},
	} {
		c.fail("POST", "/v1/tenants", body, 400, "mint_mode_removed")
	}
	var tn Tenant
	c.ok("POST", "/v1/tenants", map[string]any{"tailnet": "t.ts.net", "mintMode": "oauth", "oauthClientFile": e.keyFile}, &tn)
	c.fail("PATCH", "/v1/tenants/"+tn.ID, map[string]any{"mintMode": "apikey"}, 400, "mint_mode_removed")
	// wifClientID is no longer in the spec, so the validator would flag the
	// client; the server's typed refusal is checked directly.
	if st, eb := direct(t, e, "", "PATCH", "/v1/tenants/"+tn.ID, map[string]any{"wifClientID": "k123"}); st != 400 || eb["code"] != "mint_mode_removed" {
		t.Fatalf("wifClientID patch: %d %v", st, eb)
	}

	// A row an older tailworks wrote in apikey mode.
	ctx := context.Background()
	old := Tenant{ID: "tn_old", Tailnet: "old.ts.net", MintMode: mintRemovedAPIKey, APIKeyFile: "/nonexistent/api.env",
		RegistrationsPerMinute: 10, MaxObjects: 10, CreatedAt: time.Now().UnixMilli()}
	if err := e.store.CreateTenant(ctx, old); err != nil {
		t.Fatal(err)
	}
	_, err := e.m.broker.API(ctx, old, purposeMint)
	if me, ok := err.(*Error); !ok || me.Code != "mint_mode_removed" {
		t.Fatalf("mint with a removed mode: %v", err)
	}
	c.ok("PATCH", "/v1/tenants/tn_old", map[string]any{"maxObjects": 20}, nil)
	var up Tenant
	c.ok("PATCH", "/v1/tenants/tn_old", map[string]any{"mintMode": "oauth", "oauthClientFile": e.keyFile}, &up)
	if up.MintMode != MintOAuth || up.OAuthClientID != envOAuthClient || up.APIKeyFile != "" {
		t.Fatalf("switched tenant %+v", up)
	}
	if _, err := e.m.broker.API(ctx, up, purposeMint); err != nil {
		t.Fatalf("mint after the switch: %v", err)
	}
}
