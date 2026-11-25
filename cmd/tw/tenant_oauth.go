package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rosskukulinski/tailworks/internal/tsapi"
	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/rosskukulinski/tailworks/netd"
	"golang.org/x/term"
)

// tw tenant add without --oauth-client-file (issue #538): on a terminal it
// asks for the tenant tailnet's OAuth client secret the way the first
// tw up does and saves it to $XDG_CONFIG_HOME/tailworks/<tenant>-oauth.env
// (0600), next to the operator's oauth.env. The gate is tw up's: the
// default state dir only, a terminal, no --json. A team tenant
// (--owner-tag) keeps naming its file: its client is scoped to the team's
// tag, not tag:tw, which the prompt's steps describe. A saved file is
// reused, never overwritten. What mgmt and tw.json store is unchanged:
// the file's path.

// tenantOAuthPromptable reports whether tw tenant add may ask for the
// OAuth client instead of requiring --oauth-client-file. The state dir is
// checked again once it is open (tenantPromptOAuthFile).
func tenantOAuthPromptable(f *flags, ownerTag, namePrefix string) bool {
	return !*f.json && ownerTag == "" && namePrefix == "" && *f.stateDir == "" &&
		os.Getenv("TW_STATE_DIR") == "" && term.IsTerminal(int(os.Stdin.Fd()))
}

// tenantNeedsOAuthFile is the error when the OAuth client can be neither
// read from a flag nor asked for.
func tenantNeedsOAuthFile(tailnet string) error {
	return fail("bad_request", "tw tenant add needs --oauth-client-file (an OAuth client of "+tailnet+")").
		withFix("tw tenant add "+tailnet+" --oauth-client-file <file>",
			"the file is 0600 with TS_OAUTH_CLIENT_SECRET=tskey-client-… (the client ID is read from the secret); on a terminal, tw tenant add "+tailnet+" asks for it and saves it")
}

// tenantDefaultOAuthFile is where a prompted tenant's OAuth client is
// saved: next to the operator's oauth.env, named by the tenant ID (which
// tenantIDFor sanitises, so the tailnet can't steer the path).
func tenantDefaultOAuthFile(tid string) string {
	p := defaultOAuthFile() // up_defaults.go
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), tid+"-oauth.env")
}

// tenantPromptOAuthFile resolves the OAuth client file of a tenant added
// without --oauth-client-file: the saved file if there is one, else the
// secret asked for on the terminal. A pasted secret is checked before it
// is kept: it must not be a client another tenant (or the operator)
// already uses, and it must get a token; otherwise the new file is removed
// so the next run asks again.
func tenantPromptOAuthFile(s *state, f *flags, tailnet string) (string, error) {
	tid := tenantIDFor(tailnet)
	p := tenantDefaultOAuthFile(tid)
	if !isDefaultStateDir(s.Dir, *f.stateDir) || p == "" || tid == "tn_" {
		return "", tenantNeedsOAuthFile(tailnet)
	}
	if fileExists(p) {
		tightenSecretFile(p) // up_defaults.go
		fmt.Printf("› oauth    using %s\n", p)
		return p, nil
	}
	asked, err := promptOAuthSecret(p, os.Stdout, "tw tenant add "+tailnet, tailnet)
	if err != nil {
		return "", err
	}
	if !asked {
		return "", tenantNeedsOAuthFile(tailnet)
	}
	if err := tenantCheckPastedOAuth(s, tid, p); err != nil {
		os.Remove(p)
		return "", err
	}
	return p, nil
}

// tenantCheckPastedOAuth refuses a pasted client that already serves
// another tenant: with --oauth-client-file the same file path is refused
// (cmdTenantAdd), but a pasted secret lands in a new file, so the client
// ID is compared instead. It then checks the client gets a token, so a
// mistyped secret is not saved.
func tenantCheckPastedOAuth(s *state, tid, path string) error {
	creds, err := tsapi.LoadOAuthFile(path)
	if err != nil {
		return fail("bad_request", "can't read the OAuth client from "+path, netd.Redact(err.Error()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conflict := func(tailnet string) error {
		return fail("state_conflict", "OAuth client "+creds.ClientID+" already serves tenant "+tailnet,
			"an OAuth client belongs to one tailnet; each tenant needs its own").
			withFix("create an OAuth client in this tailnet's admin console, then tw tenant add again")
	}
	if s.Cfg.OAuthClientID != "" && creds.ClientID == s.Cfg.OAuthClientID && s.Cfg.TenantID != tid {
		return conflict(s.Cfg.Tailnet)
	}
	ts, _ := listAll[mgmt.Tenant](ctx, s.mgmt(), "/v1/tenants", 0)
	for _, t := range ts {
		if t.OAuthClientID == creds.ClientID && t.ID != tid {
			return conflict(t.Tailnet)
		}
	}
	src := &tsapi.OAuthSource{BaseURL: s.Cfg.TSAPIURL, Load: func() (tsapi.OAuthCredentials, error) { return creds, nil }}
	if _, err := src.Token(ctx); err != nil {
		return fail("oauth_exchange_failed", "the OAuth client "+creds.ClientID+" can't get an access token", netd.Redact(err.Error())).
			withFix("check the Client Secret you pasted (nothing was saved), then tw tenant add again")
	}
	return nil
}
