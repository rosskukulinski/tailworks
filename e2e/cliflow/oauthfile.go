package cliflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/netd"
)

// The OAuth client hermetic tests use: a fake Tailscale API knows it, and
// mgmt accepts its file (a well-formed tskey-client- secret, mode 0600).
// The secret is built at runtime, so no key-shaped literal is committed.
var (
	FakeOAuthID     = "kTestOAuthCNTRL"
	FakeOAuthSecret = "tskey-" + "client-" + FakeOAuthID + "-" + strings.Repeat("S", 24)
)

// WriteFakeOAuthFile writes the fake OAuth client's credential file (0600).
func WriteFakeOAuthFile(t testing.TB, path string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("TS_OAUTH_CLIENT_ID="+FakeOAuthID+"\nTS_OAUTH_CLIENT_SECRET="+FakeOAuthSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// RealOAuthClient creates an OAuth client on a real tailnet for one test
// run, the way an admin does in the console (auth_keys, devices:core,
// policy_file:read; tag tag:tw), with the harness's own API key, which
// tailworks never sees. An OAuth client mints only tags its own tag owns, so for
// suites whose objects carry other tags (e.g. TestTag) ownedTags makes
// tag:tw an owner of each in the tailnet policy first (additive,
// validated, If-Match; a no-op once present). It writes the client to a 0600 file in dir and deletes
// the client when the test ends. Tailworks mints only with OAuth clients
// (ADR-042); the API key stays the harness's tool for
// policy setup, test nodes and leftover cleanup.
func RealOAuthClient(t testing.TB, apiKeyFile, tailnet, dir, description string, ownedTags ...string) string {
	t.Helper()
	return RealOAuthClientScopes(t, apiKeyFile, tailnet, dir, description, nil, ownedTags...)
}

// RealOAuthClientScopes is RealOAuthClient with scopes beyond tailworks'
// minimum (auth_keys, devices:core, policy_file:read), e.g. "services"
// for a workload with a Tailscale Service (docs/services.md).
func RealOAuthClientScopes(t testing.TB, apiKeyFile, tailnet, dir, description string, extraScopes []string, ownedTags ...string) string {
	t.Helper()
	key, err := tsapi.LoadEnvFile(apiKeyFile, "TS_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	api := &tsapi.Client{Tailnet: tailnet, APIKey: key}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if len(ownedTags) > 0 {
		owners := map[string][]string{}
		for _, tag := range ownedTags {
			owners[tag] = []string{"autogroup:admin", "tag:tw"}
		}
		if _, err := policy.ApplyWith(ctx, api, policy.Snippet{TagOwners: owners}, policy.Options{AllowTags: ownedTags}); err != nil {
			t.Fatalf("policy: let tag:tw own %v: %v", ownedTags, netd.RedactError(err))
		}
	}
	scopes := append([]string{"auth_keys", "devices:core", "policy_file:read"}, extraScopes...)
	oc, err := api.CreateOAuthClient(ctx, scopes, []string{"tag:tw"}, description)
	if err != nil {
		t.Fatalf("create the run's OAuth client: %v", netd.RedactError(err))
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := api.DeleteKey(ctx, oc.ID); err != nil {
			t.Logf("delete OAuth client %s: %v", oc.ID, netd.RedactError(err))
		}
	})
	path := filepath.Join(dir, "oauth.env")
	if err := os.WriteFile(path, []byte("TS_OAUTH_CLIENT_ID="+oc.ID+"\nTS_OAUTH_CLIENT_SECRET="+oc.Secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
