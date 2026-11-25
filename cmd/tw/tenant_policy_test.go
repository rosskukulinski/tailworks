package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
)

// TestTenantPolicyTailscaleNeverWrites (issue #475): a Tailscale tenant's
// policy is only read. Its owner tag present is enough (whatever its
// owners); a missing one is reported with the paste line, and
// --apply-policy --yes still writes nothing. tag:tw-mgmt, tag:tw-host and
// the host ⇄ mgmt grants are never asked of a tenant tailnet.
func TestTenantPolicyTailscaleNeverWrites(t *testing.T) {
	for name, c := range map[string]struct {
		pol, owner string
		ok         bool
	}{
		"tag:tw present":          {freshPolicy, "tag:tw", true},
		"owned by a group":        {`{"tagOwners": {"tag:tw": ["group:eng"]}}`, "tag:tw", true},
		"owner tag present":       {`{"tagOwners": {"tag:tw-t-pay": ["group:pay"]}}`, "tag:tw-t-pay", true},
		"tag:tw missing":          {`{"tagOwners": {"tag:db": ["autogroup:admin"]}}`, "tag:tw", false},
		"owner tag missing":       {freshPolicy, "tag:tw-t-pay", false},
		"empty policy, no owners": {`{}`, "tag:tw", false},
	} {
		e := newUpApplyEnv(t, c.pol)
		var res tenantAddResult
		f := newFlags("tenant add")
		if err := tenantPolicy(context.Background(), f, &res, e.c, []byte(c.pol), true, controlTailscale, c.owner, true, true); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.PolicyOK != c.ok || res.Applied {
			t.Errorf("%s: %+v, want PolicyOK=%v and nothing applied", name, res, c.ok)
		}
		if !c.ok && (!strings.Contains(res.PolicyDiff, `"`+c.owner+`"`) || !strings.Contains(res.PolicyDiff, "autogroup:admin")) {
			t.Errorf("%s: paste line %q", name, res.PolicyDiff)
		}
		if strings.Contains(res.PolicyDiff, "tag:tw-mgmt") || strings.Contains(res.PolicyDiff, "tag:tw-host") || strings.Contains(res.PolicyDiff, "grants") {
			t.Errorf("%s: asks a tenant tailnet for the operator's infra: %s", name, res.PolicyDiff)
		}
		if _, writes, posts := e.now(); writes != 0 || posts != 0 {
			t.Errorf("%s: %d POSTs to a Tailscale tenant's policy", name, posts)
		}
	}
	// Unreadable (no policy_file:read): the first mint proves ownership.
	var res tenantAddResult
	if err := tenantPolicy(context.Background(), newFlags("tenant add"), &res, nil, nil, false, controlTailscale, "tag:tw", false, false); err != nil || !res.PolicyOK {
		t.Fatalf("unreadable: %+v %v", res, err)
	}
}

// TestTenantPolicyHeadscale (issue #475): a Headscale tenant needs only
// "tag:tw": [], written with --apply-policy after the confirm, and an
// existing entry is left alone.
func TestTenantPolicyHeadscale(t *testing.T) {
	const pol = `{"tagOwners": {"tag:db": []}}`
	e := newUpApplyEnv(t, pol)
	oldT := confirmIsTerminal
	t.Cleanup(func() { confirmIsTerminal = oldT })
	confirmIsTerminal = func() bool { return false }

	var res tenantAddResult
	if err := tenantPolicy(context.Background(), newFlags("tenant add"), &res, e.c, []byte(pol), true, controlHeadscale, "tag:tw", false, false); err != nil {
		t.Fatal(err)
	}
	if res.PolicyOK || strings.TrimSpace(res.PolicyDiff) == "" || strings.Contains(res.PolicyDiff, "autogroup:") {
		t.Fatalf("not applied: %+v", res)
	}
	res = tenantAddResult{}
	err := tenantPolicy(context.Background(), newFlags("tenant add"), &res, e.c, []byte(pol), true, controlHeadscale, "tag:tw", true, false)
	if cliCode(err) != "confirmation_required" {
		t.Fatalf("--apply-policy without a terminal or --yes: %v", err)
	}
	res = tenantAddResult{}
	if err := tenantPolicy(context.Background(), newFlags("tenant add"), &res, e.c, []byte(pol), true, controlHeadscale, "tag:tw", true, true); err != nil || !res.Applied || !res.PolicyOK {
		t.Fatalf("--apply-policy --yes: %+v %v", res, err)
	}
	after, writes, _ := e.now()
	if writes != 1 || !strings.Contains(after, `"tag:tw"`) || strings.Contains(after, "tag:tw-mgmt") || strings.Contains(after, "grants") {
		t.Fatalf("wrote %d times:\n%s", writes, after)
	}

	res = tenantAddResult{}
	if err := tenantPolicy(context.Background(), newFlags("tenant add"), &res, e.c, []byte(after), true, controlHeadscale, "tag:tw", true, true); err != nil || res.Applied || !res.PolicyOK {
		t.Fatalf("present: %+v %v", res, err)
	}
}

// TestTenantDefaultOAuthFile (issue #538): a prompted tenant's client is
// saved next to oauth.env, named by the sanitised tenant ID.
func TestTenantDefaultOAuthFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	if got := tenantDefaultOAuthFile(tenantIDFor("My_Other.ts.net")); got != "/cfg/tailworks/tn_my-other-oauth.env" {
		t.Fatalf("got %s", got)
	}
}

// TestTenantAddNoOAuthFileWithoutTerminal (issue #538): without a terminal
// (or with --json, --state-dir or --owner-tag) the flag is still required.
func TestTenantAddNoOAuthFileWithoutTerminal(t *testing.T) {
	for _, args := range [][]string{
		{"x.ts.net"},
		{"x.ts.net", "--json"},
		{"x.ts.net", "--state-dir", t.TempDir()},
		{"x.ts.net", "--owner-tag", "tag:tw-t-a", "--name-prefix", "a"},
	} {
		err := cmdTenantAdd(args)
		jsonOut = false
		if cliCode(err) != "bad_request" || !strings.Contains(err.Error(), "--oauth-client-file") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if err := cmdTenantAdd([]string{"x.ts.net", "--registrations-per-minute", "5", "--oauth-client-file", "/nonexistent"}); cliCode(err) != "bad_request" {
		t.Errorf("--registrations-per-minute (#537, now tw tenant set only): %v", err)
	}
}

// TestTenantCheckPastedOAuth (issue #538): a pasted secret lands in a new
// file, so the path check can't see it is the operator's client: the
// client ID is compared instead. A client that can't get a token is
// refused too (the caller then removes the file).
func TestTenantCheckPastedOAuth(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/oauth/token" && r.PostFormValue("client_secret") == cliflow.FakeOAuthSecret {
			w.Write([]byte(`{"access_token":"oauth-access-1","token_type":"Bearer","expires_in":3600}`))
			return
		}
		w.WriteHeader(401)
	}))
	defer api.Close()
	p := writeFakeOAuthFile(t, filepath.Join(t.TempDir(), "tn_b-oauth.env"))
	s := &state{Dir: t.TempDir(), Cfg: Config{Tailnet: "a.ts.net", TenantID: "tn_a", TSAPIURL: api.URL}}
	if err := tenantCheckPastedOAuth(s, "tn_b", p); err != nil {
		t.Fatalf("a new client: %v", err)
	}
	s.Cfg.OAuthClientID = cliflow.FakeOAuthID
	if err := tenantCheckPastedOAuth(s, "tn_b", p); cliCode(err) != "state_conflict" {
		t.Fatalf("the operator's client: %v", err)
	}
	s.Cfg.OAuthClientID = ""
	os.WriteFile(p, []byte("TS_OAUTH_CLIENT_SECRET=tskey-client-kOther-"+strings.Repeat("x", 24)+"\n"), 0o600)
	if err := tenantCheckPastedOAuth(s, "tn_b", p); cliCode(err) != "oauth_exchange_failed" {
		t.Fatalf("a wrong secret: %v", err)
	}
}
