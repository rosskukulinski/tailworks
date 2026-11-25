package mgmt

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rosskukulinski/tailworks/internal/tsapi"
)

// TailnetAPI is the per-tenant Tailscale API surface mgmt uses.
type TailnetAPI interface {
	CreateKey(ctx context.Context, o tsapi.KeyOptions) (tsapi.Key, error)
	DeleteKey(ctx context.Context, id string) error
	DeleteDevice(ctx context.Context, id string) error
	GetPolicy(ctx context.Context, etag string) ([]byte, string, bool, error)
}

// Credential purposes. One credential serves every purpose today (the
// OAuth client's scopes decide what it may do); callers still name the one
// they need, so a failure says which.
const (
	purposeMint      = "mint"
	purposeLifecycle = "lifecycle"
	purposePolicy    = "policy"
)

// Broker builds per-tenant API clients:
//   - headscale: the tenant's Headscale API key file, against the Headscale
//     server at its controlURL, for every purpose (headscale.go);
//   - oauth: the tenant's OAuth client (secret in a 0600 file on this host,
//     re-read at each exchange) for every purpose: client-credentials grant
//     at /api/v2/oauth/token; a 401 drops the token and retries once. What
//     the client may do is its scopes' business (auth_keys always;
//     devices:core for device deletes, policy_file:read for preflight).
//
// Access tokens are cached until shortly before they expire.
type Broker struct {
	BaseURL string // Tailscale API, default tsapi.DefaultBaseURL
	HTTP    *http.Client
	Clock   Clock

	mu    sync.Mutex
	oauth map[string]*tsapi.OAuthSource // tenantID|clientID|file
}

func (b *Broker) clock() Clock {
	if b.Clock == nil {
		return realClock{}
	}
	return b.Clock
}

// API returns a client for the tenant and purpose.
func (b *Broker) API(ctx context.Context, t Tenant, purpose string) (TailnetAPI, error) {
	c := &tsapi.Client{BaseURL: b.BaseURL, Tailnet: t.Tailnet, HTTP: b.HTTP}
	switch t.MintMode {
	case MintOAuth:
		c.Tokens = b.oauthSource(t)
		return c, nil
	case MintHeadscale:
		return b.headscaleAPI(t)
	}
	if removedMintMode(t.MintMode) {
		return nil, errMintModeRemoved(t)
	}
	return nil, fmt.Errorf("unknown mint mode %q", t.MintMode)
}

// oauthSource returns the tenant's cached OAuth token source.
func (b *Broker) oauthSource(t Tenant) *tsapi.OAuthSource {
	key := t.ID + "|" + t.OAuthClientID + "|" + t.OAuthClientFile
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.oauth[key]; ok {
		return s
	}
	if b.oauth == nil {
		b.oauth = map[string]*tsapi.OAuthSource{}
	}
	file, wantID := t.OAuthClientFile, t.OAuthClientID
	s := &tsapi.OAuthSource{BaseURL: b.BaseURL, HTTP: b.HTTP, Now: func() time.Time { return b.clock().Now() },
		Load: func() (tsapi.OAuthCredentials, error) {
			c, err := tsapi.LoadOAuthFile(file)
			if err != nil {
				return c, fmt.Errorf("tenant %s oauth client: %w", t.ID, err)
			}
			if wantID != "" && c.ClientID != wantID {
				return tsapi.OAuthCredentials{}, fmt.Errorf("tenant %s oauth client: %s now holds client %s, not %s (update the tenant)", t.ID, file, c.ClientID, wantID)
			}
			return c, nil
		}}
	b.oauth[key] = s
	return s
}

// checkOAuthFile validates an oauth tenant's credential file (absolute,
// 0600, a tskey-client- secret) and returns the client ID (wantID, or the
// one the file names). The secret is not kept.
func checkOAuthFile(file, wantID string) (string, error) {
	if file == "" {
		return "", errf(400, "bad_request", "mintMode oauth needs oauthClientFile (a 0600 file with TS_OAUTH_CLIENT_ID and TS_OAUTH_CLIENT_SECRET)")
	}
	if !filepath.IsAbs(file) {
		return "", errf(400, "bad_request", "oauthClientFile must be an absolute path")
	}
	c, err := tsapi.LoadOAuthFile(file)
	if err != nil {
		return "", errf(400, "bad_request", "oauthClientFile: %v", err)
	}
	if wantID != "" && wantID != c.ClientID {
		return "", errf(400, "bad_request", "oauthClientID %s does not match the client in %s (%s)", wantID, file, c.ClientID)
	}
	return c.ClientID, nil
}

// Forget drops a tenant's cached access tokens (after its client ID or
// credential file changed, or to force a fresh exchange).
func (b *Broker) Forget(tenantID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k := range b.oauth {
		if strings.HasPrefix(k, tenantID+"|") {
			delete(b.oauth, k)
		}
	}
}
